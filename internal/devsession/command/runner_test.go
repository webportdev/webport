package command

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "WEBPORT_COMMAND_HELPER"

func TestRunnerPreservesArgvEnvironmentDirectoryAndCapturesStreams(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result")
	sink := &MemorySink{}
	process, err := (Runner{}).Start(context.Background(), Spec{
		Command: []string{os.Args[0], "-test.run=TestCommandHelper"},
		Dir:     dir,
		Env:     append(os.Environ(), helperEnv+"=capture", "WEBPORT_ARG_ONE=hello world", "WEBPORT_ARG_TWO=quote\"value", "WEBPORT_RESULT="+resultPath),
		Sink:    sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "hello world|quote\"value") || !strings.Contains(content, dir) {
		t.Fatalf("helper result = %q", content)
	}
	events := sink.Events()
	if len(events) != 2 || !hasEvent(events, "stdout", "stdout line", true) || !hasEvent(events, "stderr", "stderr line", true) {
		t.Fatalf("events = %+v", events)
	}
}

func TestRunnerHandlesLongAndPartialLinesAndExitStatus(t *testing.T) {
	sink := &MemorySink{}
	process, err := (Runner{}).Start(context.Background(), Spec{
		Command: []string{os.Args[0], "-test.run=TestCommandHelper"},
		Env:     append(os.Environ(), helperEnv+"=long"),
		Sink:    sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = process.Wait()
	if err == nil {
		t.Fatal("Wait() error = nil")
	}
	result := process.Result()
	if result.ExitCode != 7 || result.PID == 0 || result.EndTime.Before(result.StartTime) {
		t.Fatalf("result = %+v", result)
	}
	var sawLong, sawPartial bool
	for _, event := range sink.Events() {
		if event.Stream == "stdout" && len(event.Line) == 100000 {
			sawLong = true
		}
		if event.Stream == "stderr" && !event.Complete && event.Line == "partial" {
			sawPartial = true
		}
	}
	if !sawLong || !sawPartial {
		t.Fatal("long or partial output was lost")
	}
}

func TestRunnerUsesExplicitShellOnlyWhenConfigured(t *testing.T) {
	sink := &MemorySink{}
	process, err := (Runner{}).Start(context.Background(), Spec{Shell: "printf 'shell output\\n'", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	if events := sink.Events(); len(events) != 1 || events[0].Line != "shell output" {
		t.Fatalf("shell events = %+v", events)
	}
}

func TestRunnerTerminatesProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups differ on Windows")
	}
	process, err := (Runner{}).Start(context.Background(), Spec{Command: []string{os.Args[0], "-test.run=TestCommandHelper"}, Env: append(os.Environ(), helperEnv+"=sleep")})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Terminate(syscall.SIGTERM, 100*time.Millisecond); err != nil {
		// An intentional signal commonly produces an ExitError; the process
		// must nevertheless be reaped and marked as cleanup.
		_ = err
	}
	if !process.Result().CleanedUp {
		t.Fatal("process was not marked cleaned up")
	}
}

func TestCommandHelper(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		return
	case "capture":
		_, _ = os.Stdout.WriteString("stdout line\n")
		_, _ = os.Stderr.WriteString("stderr line\n")
		_ = os.WriteFile(os.Getenv("WEBPORT_RESULT"), []byte(os.Getenv("WEBPORT_ARG_ONE")+"|"+os.Getenv("WEBPORT_ARG_TWO")+"|"+mustGetwd(t)), 0o600)
		os.Exit(0)
	case "long":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 100000) + "\n")
		_, _ = os.Stderr.WriteString("partial")
		os.Exit(7)
	case "sleep":
		for {
			time.Sleep(time.Hour)
		}
	default:
		t.Fatalf("unknown helper mode %q", os.Getenv(helperEnv))
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	value, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func hasEvent(events []OutputEvent, stream, line string, complete bool) bool {
	for _, event := range events {
		if event.Stream == stream && event.Line == line && event.Complete == complete {
			return true
		}
	}
	return false
}
