// Package socket implements the daemon's local Unix-socket channel. The
// daemon admits only connections whose peer credentials name the machine's
// developer (or root); there are no passwords or tokens on this channel.
//
// Frames are newline-delimited JSON. A connection's first frame selects its
// role: "request" for a one-shot CLI call, or "register" for a development
// session, which keeps the connection open, sends "state" updates, and
// answers "request" frames the daemon sends back.
package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	// EnvPath overrides the socket path for the daemon and its clients.
	EnvPath = "WEBPORT_SOCKET_PATH"

	maxFrameBytes = 16 << 20

	TypeRequest  = "request"
	TypeResponse = "response"
	TypeRegister = "register"
	TypeState    = "state"
)

// DefaultPath returns the platform socket path, or the WEBPORT_SOCKET_PATH
// override. Installed daemons create these directories as root-owned or
// service-owned locations that other users cannot write.
func DefaultPath() string {
	if value := os.Getenv(EnvPath); value != "" {
		return value
	}
	if runtime.GOOS == "darwin" {
		return "/usr/local/var/webport/webport.sock"
	}
	return "/run/webport/webport.sock"
}

// Frame is one protocol message.
type Frame struct {
	Type      string          `json:"type"`
	ID        uint64          `json:"id,omitempty"`
	Operation string          `json:"operation,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	OK        bool            `json:"ok,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// Conn reads and writes frames. Writes are safe for concurrent use.
type Conn struct {
	raw     net.Conn
	scanner *bufio.Scanner
	mu      sync.Mutex
}

func NewConn(raw net.Conn) *Conn {
	scanner := bufio.NewScanner(raw)
	scanner.Buffer(make([]byte, 64*1024), maxFrameBytes)
	return &Conn{raw: raw, scanner: scanner}
}

func (c *Conn) Read() (Frame, error) {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return Frame{}, err
		}
		return Frame{}, net.ErrClosed
	}
	var frame Frame
	if err := json.Unmarshal(c.scanner.Bytes(), &frame); err != nil {
		return Frame{}, fmt.Errorf("decode frame: %w", err)
	}
	return frame, nil
}

func (c *Conn) Write(frame Frame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.raw.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = c.raw.Write(data)
	return err
}

func (c *Conn) Close() error { return c.raw.Close() }

// SetReadDeadline bounds the next Read.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.raw.SetReadDeadline(t) }

// Encode builds a payload, reporting marshal errors as frame errors.
func Encode(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

// Dial connects to the daemon socket after checking that its directory cannot
// be replaced by other users, so a session never hands its environment to an
// impostor listening at an attacker-controlled path.
func Dial(ctx context.Context, path string) (*Conn, error) {
	if err := checkDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return NewConn(raw), nil
}

func checkDirectory(directory string) error {
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	// Shared directories such as /tmp would let another user plant a socket.
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("refusing daemon socket in %s: the directory is writable by other users", directory)
	}
	return nil
}

// Call performs one request and decodes its response payload into result.
func Call(ctx context.Context, path, operation string, payload, result any) error {
	conn, err := Dial(ctx, path)
	if err != nil {
		return fmt.Errorf("cannot reach the webport daemon at %s: %w (is it running and up to date? try webport doctor)", path, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.Write(Frame{Type: TypeRequest, ID: 1, Operation: operation, Payload: Encode(payload)}); err != nil {
		return err
	}
	response, err := conn.Read()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("daemon closed the connection: %w", err)
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "request failed"
		}
		return errors.New(response.Error)
	}
	if result != nil && len(response.Payload) > 0 {
		return json.Unmarshal(response.Payload, result)
	}
	return nil
}
