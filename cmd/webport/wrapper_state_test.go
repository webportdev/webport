package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/tui"
)

func TestWrapperDashboardControlAndEnvironment(t *testing.T) {
	// Keep the Unix socket path short on both supported platforms.
	directory, err := os.MkdirTemp("", "wp-tui-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	t.Setenv("XDG_RUNTIME_DIR", directory)
	store, err := state.NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	defer store.RemoveLive()
	command := &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}, Env: []string{"APP_VALUE=hello", "WEBPORT_CLIENT_TOKEN=private", "WEBPORT_DETACHED_READY_FD=3"}}
	signals := make(chan os.Signal, 1)
	var restart atomic.Bool
	control, err := startWrapperControl(&store, wrapperResolution{Host: "example.test", URL: "https://example.test"}, routeFlags{project: "sample", branch: "main", port: 3000}, command, signals, &restart)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	instances, err := state.ListLive()
	if err != nil || len(instances) != 1 {
		t.Fatalf("instances=%v err=%v", instances, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b := tui.LocalBackend{}
	inspection, err := b.Inspect(ctx, instances[0], false, false)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Environment["command"]["WEBPORT_URL"] != "https://example.test" {
		t.Fatalf("inspection=%+v", inspection)
	}
	inspection, err = b.Inspect(ctx, instances[0], false, true)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Environment["command"]["APP_VALUE"] != "<redacted>" {
		t.Fatal("inherited env must start hidden")
	}
	inspection, err = b.Inspect(ctx, instances[0], true, true)
	if err != nil {
		t.Fatal(err)
	}
	values := inspection.Environment["command"]
	if values["APP_VALUE"] != "hello" || values["WEBPORT_CLIENT_TOKEN"] != "" || values[detachedReadyEnv] != "" {
		t.Fatalf("env=%v", values)
	}
	if err := b.Control(ctx, instances[0], "restart"); err != nil {
		t.Fatal(err)
	}
	if !restart.Load() {
		t.Fatal("restart not requested")
	}
	select {
	case sig := <-signals:
		if sig != syscall.SIGTERM {
			t.Fatalf("signal=%v", sig)
		}
	default:
		t.Fatal("no shutdown signal")
	}
}
func TestTUIRequiresTerminalAndDocumentsFlags(t *testing.T) {
	if err := runTUI(nil, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("accepted nonterminal input")
	}
	if err := runTUI([]string{"--help"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestWrapperStoresAllowConcurrentInstancesInSameWorktree(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	first, err := wrapperStore()
	if err != nil {
		t.Fatal(err)
	}
	second, err := wrapperStore()
	if err != nil {
		t.Fatal(err)
	}
	if first.WorktreeRoot != second.WorktreeRoot || first.LivePath == second.LivePath {
		t.Fatal("wrapper identities do not support concurrency")
	}
	for _, store := range []*state.Store{first, second} {
		if err := store.Acquire(); err != nil {
			t.Fatal(err)
		}
		defer store.Release()
	}
}
