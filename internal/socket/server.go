package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Handler answers one CLI request.
type Handler func(ctx context.Context, payload json.RawMessage) (any, error)

// SessionHandler takes over a connection whose first frame was "register".
// It returns when the session disconnects.
type SessionHandler func(conn *Conn, register Frame)

// Server accepts authenticated connections on a Unix socket.
type Server struct {
	path     string
	allowed  func(uid int) bool
	handlers map[string]Handler
	session  SessionHandler
	listener net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	closed   bool
}

// Listen creates the socket. The file is world-connectable; every
// connection is then admitted only if allowed accepts its peer UID.
func Listen(path string, allowed func(uid int) bool, handlers map[string]Handler, session SessionHandler) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		_ = listener.Close()
		return nil, err
	}
	s := &Server{path: path, allowed: allowed, handlers: handlers, session: session, listener: listener, conns: make(map[net.Conn]struct{})}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

func (s *Server) Path() string { return s.path }

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		raw, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = raw.Close()
			return
		}
		s.conns[raw] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, raw)
				s.mu.Unlock()
				_ = raw.Close()
			}()
			s.handle(raw)
		}()
	}
}

func (s *Server) handle(raw net.Conn) {
	conn := NewConn(raw)
	uid, err := PeerUID(raw)
	if err != nil || !s.allowed(uid) {
		message := "unauthorized: this daemon only accepts its configured owner (WEBPORT_OWNER_UID)"
		if err != nil {
			message = "unauthorized: " + err.Error()
		} else {
			log.Printf("WARN: rejected daemon socket connection from uid %d", uid)
		}
		_ = conn.Write(Frame{Type: TypeResponse, ID: 1, Error: message})
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	first, err := conn.Read()
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch first.Type {
	case TypeRegister:
		if s.session != nil {
			s.session(conn, first)
		}
	case TypeRequest:
		handler, ok := s.handlers[first.Operation]
		if !ok {
			_ = conn.Write(Frame{Type: TypeResponse, ID: first.ID, Error: fmt.Sprintf("unknown operation %q; the daemon may need an upgrade", first.Operation)})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := handler(ctx, first.Payload)
		if err != nil {
			_ = conn.Write(Frame{Type: TypeResponse, ID: first.ID, Error: err.Error()})
			return
		}
		_ = conn.Write(Frame{Type: TypeResponse, ID: first.ID, OK: true, Payload: Encode(result)})
	default:
		_ = conn.Write(Frame{Type: TypeResponse, ID: first.ID, Error: "unexpected frame " + first.Type})
	}
}

// Close stops accepting, closes every connection, and removes the socket.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for raw := range s.conns {
		_ = raw.Close()
	}
	s.mu.Unlock()
	err := s.listener.Close()
	s.wg.Wait()
	if removeErr := os.Remove(s.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		err = errors.Join(err, removeErr)
	}
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return err
}
