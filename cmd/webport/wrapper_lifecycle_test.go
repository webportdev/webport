package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/tui"
)

func TestWrapperCLIHelper(t *testing.T) {
	api := os.Getenv("WEBPORT_WRAPPER_CLI_API")
	if api == "" {
		return
	}
	args := []string{"--api", api, "--project", "wrapper-test", "--branch", "main", "--port", os.Getenv("WEBPORT_WRAPPER_CLI_PORT"), "--", os.Args[0], "-test.run=^TestWrapperHTTPHelper$"}
	if err := runDevWithIO(args, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}
func TestWrapperHTTPHelper(t *testing.T) {
	port := os.Getenv("WEBPORT_WRAPPER_CLI_PORT")
	if port == "" {
		return
	}
	if err := http.ListenAndServe("127.0.0.1:"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "OK") })); err != nil {
		t.Fatal(err)
	}
}
func TestWrapperLifecycleRestartAndStopViaDashboard(t *testing.T) {
	runtime, err := os.MkdirTemp("", "wp-wrap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtime)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"base_domain": "dev.test", "default_ttl_seconds": 30})
		case r.Method == "POST":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"lease_id": "test-lease", "route": map[string]any{"project": "wrapper-test", "branch": "main", "port": port, "domain": "wrapper-test-main.dev.test"}})
		default:
			_, _ = io.WriteString(w, "OK")
		}
	}))
	defer daemon.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWrapperCLIHelper$")
	child.Env = append(os.Environ(), "WEBPORT_WRAPPER_CLI_API="+daemon.URL, "WEBPORT_WRAPPER_CLI_PORT="+strconv.Itoa(port))
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("wrapper did not terminate")
		}
	}()
	waitLive := func(previousPID int) state.LiveState {
		t.Helper()
		for {
			values, err := state.ListLive()
			if err != nil {
				t.Fatal(err)
			}
			for _, live := range values {
				if live.Services["command"].PID != previousPID {
					return live
				}
			}
			select {
			case err := <-done:
				t.Fatalf("wrapper exited: %v", err)
			case <-ctx.Done():
				t.Fatal("wrapper not ready")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	first := waitLive(0)
	backend := tui.LocalBackend{API: daemon.URL}
	if err := backend.Control(ctx, first, "restart"); err != nil {
		t.Fatal(err)
	}
	second := waitLive(first.Services["command"].PID)
	if second.SessionID == first.SessionID {
		t.Fatal("restart did not replace session identity")
	}
	if err := backend.Control(ctx, second, "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		done <- nil
	case <-ctx.Done():
		t.Fatal("stop did not terminate wrapper")
	}
	live, err := state.ListLive()
	if err != nil || len(live) != 0 {
		t.Fatalf("live after stop=%v err=%v", live, err)
	}
}
