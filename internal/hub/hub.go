// Package hub is the daemon's registry of development sessions connected
// over the authenticated Unix socket. Sessions push non-secret live state;
// environment values and logs are requested from the session on demand and
// never stored here. A session disappears as soon as its connection closes.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/socket"
)

// ErrNotFound reports a session that is not connected.
var ErrNotFound = errors.New("instance is no longer active")

type peer struct {
	conn    *socket.Conn
	live    state.LiveState
	pending map[uint64]chan socket.Frame
	nextID  uint64
}

// Hub tracks connected sessions.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]*peer
}

func New() *Hub { return &Hub{sessions: make(map[string]*peer)} }

// List returns connected sessions' live state, sorted by worktree.
func (h *Hub) List() []state.LiveState {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]state.LiveState, 0, len(h.sessions))
	for _, p := range h.sessions {
		result = append(result, p.live)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Worktree == result[j].Worktree {
			return result[i].SessionID < result[j].SessionID
		}
		return result[i].Worktree < result[j].Worktree
	})
	return result
}

func decodeLive(payload json.RawMessage) (state.LiveState, error) {
	var live state.LiveState
	if err := json.Unmarshal(payload, &live); err != nil {
		return live, err
	}
	if live.SessionID == "" {
		return live, errors.New("session ID is required")
	}
	live.ControlPath, live.ControlToken = "", ""
	return live, nil
}

// Serve owns a session connection until it closes. It is a
// socket.SessionHandler.
func (h *Hub) Serve(conn *socket.Conn, register socket.Frame) {
	live, err := decodeLive(register.Payload)
	if err != nil {
		_ = conn.Write(socket.Frame{Type: socket.TypeResponse, Error: "invalid registration: " + err.Error()})
		return
	}
	p := &peer{conn: conn, live: live, pending: make(map[uint64]chan socket.Frame)}
	h.mu.Lock()
	if previous, ok := h.sessions[live.SessionID]; ok {
		_ = previous.conn.Close()
	}
	h.sessions[live.SessionID] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.sessions[live.SessionID] == p {
			delete(h.sessions, live.SessionID)
		}
		for id, waiter := range p.pending {
			close(waiter)
			delete(p.pending, id)
		}
		h.mu.Unlock()
	}()
	for {
		frame, err := conn.Read()
		if err != nil {
			return
		}
		switch frame.Type {
		case socket.TypeState:
			next, err := decodeLive(frame.Payload)
			if err != nil || next.SessionID != live.SessionID {
				log.Printf("WARN: ignoring invalid session state update: %v", err)
				continue
			}
			h.mu.Lock()
			p.live = next
			h.mu.Unlock()
		case socket.TypeResponse:
			h.mu.Lock()
			waiter, ok := p.pending[frame.ID]
			delete(p.pending, frame.ID)
			h.mu.Unlock()
			if ok {
				waiter <- frame
				close(waiter)
			}
		}
	}
}

// Request sends operation to a connected session and waits for its answer.
func (h *Hub) Request(ctx context.Context, sessionID, operation string, payload any) (state.Response, error) {
	h.mu.Lock()
	p, ok := h.sessions[sessionID]
	if !ok {
		h.mu.Unlock()
		return state.Response{}, ErrNotFound
	}
	p.nextID++
	id := p.nextID
	waiter := make(chan socket.Frame, 1)
	p.pending[id] = waiter
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		delete(p.pending, id)
		h.mu.Unlock()
	}
	if err := p.conn.Write(socket.Frame{Type: socket.TypeRequest, ID: id, Operation: operation, Payload: socket.Encode(payload)}); err != nil {
		cancel()
		return state.Response{}, fmt.Errorf("send to session: %w", err)
	}
	select {
	case <-ctx.Done():
		cancel()
		return state.Response{}, ctx.Err()
	case frame, ok := <-waiter:
		if !ok {
			return state.Response{}, ErrNotFound
		}
		response := state.Response{OK: frame.OK, Error: frame.Error}
		if len(frame.Payload) > 0 && string(frame.Payload) != "null" {
			if err := json.Unmarshal(frame.Payload, &response.Payload); err != nil {
				return state.Response{}, fmt.Errorf("decode session response: %w", err)
			}
		}
		return response, nil
	}
}
