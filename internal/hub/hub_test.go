package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/socket"
)

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startHub(t *testing.T, path string) (*Hub, *socket.Server) {
	t.Helper()
	h := New()
	uid := os.Getuid()
	server, err := socket.Listen(path, func(peer int) bool { return peer == uid }, nil, h.Serve)
	if err != nil {
		t.Fatal(err)
	}
	return h, server
}

func TestSessionLinkRegistersAnswersAndDisconnects(t *testing.T) {
	directory, err := os.MkdirTemp("", "wph")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "d.sock")
	h, server := startHub(t, path)

	logPath := filepath.Join(directory, "web.log")
	if err := os.WriteFile(logPath, []byte("first\n\x1b[31msecond\x1b[0m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var operations []string
	link := state.StartLink(path, func(_ context.Context, request state.Request) state.Response {
		mu.Lock()
		operations = append(operations, request.Operation)
		mu.Unlock()
		if request.Operation == "inspect" {
			sensitive, _ := request.Payload["show_sensitive"].(bool)
			value := "<redacted>"
			if sensitive {
				value = "hunter2"
			}
			return state.Response{OK: true, Payload: map[string]any{"inspection": map[string]any{"environment": map[string]any{"web": map[string]any{"SECRET": value}}}}}
		}
		return state.Response{OK: true}
	})
	defer link.Close()
	live := state.LiveState{
		SessionID: "s1", Worktree: "/src/app", ControlPath: "/run/x.sock", ControlToken: "secret-token",
		Services: map[string]state.ServiceState{"web": {State: "starting"}},
		LogPaths: map[string]string{"web": logPath},
	}
	link.Publish(live)
	waitFor(t, "registration", func() bool { return len(h.List()) == 1 })
	if got := h.List()[0]; got.ControlToken != "" || got.ControlPath != "" {
		t.Fatalf("hub received control credentials: %#v", got)
	}

	live.Services["web"] = state.ServiceState{State: "ready", Ready: true}
	link.Publish(live)
	waitFor(t, "state update", func() bool { return h.List()[0].Services["web"].Ready })

	ctx := context.Background()
	response, err := h.Request(ctx, "s1", "inspect", map[string]any{"show_sensitive": true})
	if err != nil || !response.OK || !strings.Contains(fmtAny(response.Payload), "hunter2") {
		t.Fatalf("inspect = %#v, %v", response, err)
	}
	logs, err := h.Request(ctx, "s1", "logs", map[string]any{"service": "web"})
	if err != nil || fmtAny(logs.Payload["lines"]) != "[first second]" {
		t.Fatalf("logs = %#v, %v", logs, err)
	}
	if response, err := h.Request(ctx, "s1", "exec-info", nil); err != nil || response.OK || !strings.Contains(response.Error, "not permitted") {
		t.Fatalf("disallowed operation = %#v, %v", response, err)
	}
	if response, err := h.Request(ctx, "s1", "stop", nil); err != nil || !response.OK {
		t.Fatalf("stop = %#v, %v", response, err)
	}
	mu.Lock()
	if strings.Join(operations, ",") != "inspect,stop" {
		t.Fatalf("handler operations = %v", operations)
	}
	mu.Unlock()
	if _, err := h.Request(ctx, "missing", "stop", nil); err != ErrNotFound {
		t.Fatalf("missing session error = %v", err)
	}

	// The link reconnects after a daemon restart.
	_ = server.Close()
	waitFor(t, "disconnect", func() bool { return len(h.List()) == 0 })
	restarted, server2 := startHub(t, path)
	defer server2.Close()
	waitFor(t, "re-registration", func() bool { return len(restarted.List()) == 1 })

	link.Close()
	waitFor(t, "removal after close", func() bool { return len(restarted.List()) == 0 })
}

func fmtAny(value any) string {
	switch v := value.(type) {
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = fmtAny(item)
		}
		return "[" + strings.Join(parts, " ") + "]"
	case map[string]any:
		var parts []string
		for key, item := range v {
			parts = append(parts, key+":"+fmtAny(item))
		}
		return "{" + strings.Join(parts, " ") + "}"
	case string:
		return v
	default:
		return ""
	}
}
