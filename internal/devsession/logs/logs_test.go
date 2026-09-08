package logs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webportdev/webport/internal/devsession/command"
	"github.com/webportdev/webport/internal/devsession/config"
)

func TestManagerPrefixesMirrorsTailsAndSeparatesStreams(t *testing.T) {
	root := t.TempDir()
	var terminal bytes.Buffer
	manager, err := New(root, &terminal, true)
	if err != nil {
		t.Fatal(err)
	}
	sinkValue, err := manager.Sink("api", &config.Logs{Destination: "file", Path: "logs/api.log", Streams: "separate", Mode: "truncate"})
	if err != nil {
		t.Fatal(err)
	}
	sink := sinkValue.(command.OutputSink)
	sink.WriteOutput(command.OutputEvent{Stream: "stdout", Raw: []byte("hello\n"), Line: "hello", Complete: true})
	sink.WriteOutput(command.OutputEvent{Stream: "stderr", Raw: []byte("oops"), Line: "oops", Complete: false})
	if !strings.Contains(terminal.String(), "api") || !strings.Contains(terminal.String(), "\x1b[36m") {
		t.Fatalf("terminal = %q", terminal.String())
	}
	if got := manager.Tail("api"); len(got) != 2 || got[0] != "hello" || got[1] != "oops" {
		t.Fatalf("tail = %v", got)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, _ := os.ReadFile(filepath.Join(root, "logs", "api.stdout.log"))
	stderr, _ := os.ReadFile(filepath.Join(root, "logs", "api.stderr.log"))
	if string(stdout) != "hello\n" || string(stderr) != "oops" {
		t.Fatalf("logs stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestManagerRotatesAndRejectsOutsidePaths(t *testing.T) {
	root := t.TempDir()
	manager, err := New(root, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := manager.Sink("api", &config.Logs{Destination: "file", Path: "api.log", MaxBytes: 5, Backups: 1})
	if err != nil {
		t.Fatal(err)
	}
	sink.WriteOutput(command.OutputEvent{Stream: "stdout", Raw: []byte("12345\n"), Line: "12345", Complete: true})
	sink.WriteOutput(command.OutputEvent{Stream: "stdout", Raw: []byte("67890\n"), Line: "67890", Complete: true})
	_ = manager.Close()
	if _, err := os.Stat(filepath.Join(root, "api.log.1")); err != nil {
		t.Fatalf("rotated log missing: %v", err)
	}
	if _, err := New(root, nil, false); err != nil {
		t.Fatal(err)
	}
	manager, _ = New(root, nil, false)
	if _, err := manager.Sink("api", &config.Logs{Destination: "file", Path: "../outside.log"}); err == nil {
		t.Fatal("outside log path accepted")
	}
}
