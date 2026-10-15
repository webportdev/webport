package session

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/state"
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
    description: Original server
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
	assertDescription := func(want string) {
		t.Helper()
		status, err := Status(options)
		if err != nil {
			t.Fatal(err)
		}
		if got := status.(state.LiveState).Services["server"].Description; got != want {
			t.Fatalf("live description = %q, want %q", got, want)
		}
		resolved, err := InspectConfig(ctx, Options{ConfigPath: path, PreferLive: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := resolved.Services["server"].Description; got != want {
			t.Fatalf("live config description = %q, want %q", got, want)
		}
	}
	assertDescription("Original server")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(content), "Original server", "Updated server")), 0600); err != nil {
		t.Fatal(err)
	}
	assertDescription("Original server")
	response, err := Control(ctx, options, "exec-info", map[string]any{"service": "server"})
	if err != nil || !response.OK {
		t.Fatalf("control before restart: %v, %+v", err, response)
	}
	response, err = Control(ctx, options, "restart", nil)
	if err != nil || !response.OK {
		t.Fatalf("restart: %v, %+v", err, response)
	}
	waitReady()
	assertDescription("Updated server")
	response, err = Control(ctx, options, "stop", nil)
	if err != nil || !response.OK {
		t.Fatalf("stop: %v, %+v", err, response)
	}
}
