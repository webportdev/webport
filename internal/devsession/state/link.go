package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/webportdev/webport/internal/socket"
)

// linkOperations are the only requests a session accepts from the daemon.
// They match what the terminal dashboard can do through the control socket.
var linkOperations = map[string]bool{"inspect": true, "stop": true, "restart": true, "status": true, "logs": true}

const linkRetry = 2 * time.Second

// Link keeps a session registered with the daemon over its authenticated
// Unix socket, so the daemon's web UI can show and manage it. The session
// dials out; the daemon never reads the session's private files. A missing
// or restarting daemon is retried quietly and never affects the session.
type Link struct {
	path    string
	handler Handler
	cancel  context.CancelFunc
	done    chan struct{}

	closeOnce sync.Once

	// state is the latest live state, serialized at Publish time so later
	// changes to the session's maps cannot race with sending it.
	mu        sync.Mutex
	state     []byte
	sessionID string
	conn      *socket.Conn
	wake      chan struct{}
}

// StartLink connects handler's session to the daemon socket at path, or the
// default path when it is empty. Call Publish to register and update state.
func StartLink(path string, handler Handler) *Link {
	if path == "" {
		path = socket.DefaultPath()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &Link{path: path, handler: handler, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	go l.run(ctx)
	return l
}

// Publish records the session's current state, without its control
// credential, and sends it asynchronously so a slow or absent daemon never
// blocks the session.
func (l *Link) Publish(live LiveState) {
	if l == nil {
		return
	}
	live.ControlPath, live.ControlToken = "", ""
	data, err := json.Marshal(live)
	if err != nil {
		return
	}
	l.mu.Lock()
	l.state, l.sessionID = data, live.SessionID
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *Link) latest() ([]byte, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state, l.sessionID
}

// Close disconnects; the daemon drops the session immediately.
func (l *Link) Close() {
	if l == nil {
		return
	}
	l.closeOnce.Do(l.shutdown)
}

func (l *Link) shutdown() {
	l.cancel()
	l.mu.Lock()
	if l.conn != nil {
		_ = l.conn.Close()
	}
	l.mu.Unlock()
	<-l.done
}

func (l *Link) run(ctx context.Context) {
	defer close(l.done)
	for ctx.Err() == nil {
		if data, sessionID := l.latest(); data != nil {
			l.session(ctx, data, sessionID)
		}
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-time.After(linkRetry):
		}
	}
}

func (l *Link) session(ctx context.Context, registration []byte, sessionID string) {
	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	conn, err := socket.Dial(dialCtx, l.path)
	cancel()
	if err != nil {
		return
	}
	l.mu.Lock()
	l.conn = conn
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.conn = nil
		l.mu.Unlock()
		_ = conn.Close()
	}()
	if err := conn.Write(socket.Frame{Type: socket.TypeRegister, Payload: registration}); err != nil {
		return
	}
	sent := registration
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			frame, err := conn.Read()
			if err != nil {
				return
			}
			if frame.Type == socket.TypeResponse && frame.Error != "" {
				// The daemon refused the registration, e.g. a UID mismatch.
				return
			}
			if frame.Type == socket.TypeRequest {
				go l.answer(ctx, conn, frame)
			}
		}
	}()
	for {
		if data, id := l.latest(); id == sessionID && !bytes.Equal(data, sent) {
			if err := conn.Write(socket.Frame{Type: socket.TypeState, Payload: data}); err != nil {
				return
			}
			sent = data
		}
		select {
		case <-ctx.Done():
			return
		case <-closed:
			return
		case <-l.wake:
		}
	}
}

func (l *Link) answer(ctx context.Context, conn *socket.Conn, frame socket.Frame) {
	reply := socket.Frame{Type: socket.TypeResponse, ID: frame.ID}
	if !linkOperations[frame.Operation] {
		reply.Error = "operation not permitted over the daemon channel"
		_ = conn.Write(reply)
		return
	}
	var payload map[string]any
	if len(frame.Payload) > 0 {
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			reply.Error = "invalid payload"
			_ = conn.Write(reply)
			return
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if frame.Operation == "logs" {
		var live LiveState
		if data, _ := l.latest(); data != nil {
			_ = json.Unmarshal(data, &live)
		}
		service, _ := payload["service"].(string)
		lines, err := ServiceLogs(requestCtx, live, service)
		result := map[string]any{"lines": lines}
		if err != nil && !errors.Is(err, context.Canceled) {
			result["error"] = err.Error()
		}
		reply.OK, reply.Payload = true, socket.Encode(result)
		_ = conn.Write(reply)
		return
	}
	response := l.handler(requestCtx, Request{Operation: frame.Operation, Payload: payload})
	reply.OK, reply.Error, reply.Payload = response.OK, response.Error, socket.Encode(response.Payload)
	if !response.OK && reply.Error == "" {
		reply.Error = "request failed"
	}
	_ = conn.Write(reply)
}
