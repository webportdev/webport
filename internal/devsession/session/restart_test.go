package session

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestartReplacesServiceAndRetainsSessionControl(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".webport.yaml")
	err := os.WriteFile(path, []byte(`version: 1
project: restart-test
branch: main
worktree_root: .
profiles: {default: {services: [server]}}
services:
  server:
    command: [sh, -c, "while true; do sleep 1; done"]
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 4)
	options := Options{ConfigPath: path, Out: io.Discard, ErrOut: io.Discard, OnReady: func() { ready <- struct{}{} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("session did not stop")
		}
	}()
	waitReady := func() {
		t.Helper()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("session exited: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("not ready")
		}
	}
	waitReady()
	response, err := Control(ctx, options, "exec-info", map[string]any{"service": "server"})
	if err != nil || !response.OK {
		t.Fatalf("control before restart: %v, %+v", err, response)
	}
	response, err = Control(ctx, options, "restart", nil)
	if err != nil || !response.OK {
		t.Fatalf("restart: %v, %+v", err, response)
	}
	waitReady()
	response, err = Control(ctx, options, "stop", nil)
	if err != nil || !response.OK {
		t.Fatalf("stop: %v, %+v", err, response)
	}
}
