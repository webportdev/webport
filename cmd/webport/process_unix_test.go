//go:build linux || darwin

package main

import (
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const processHelperEnv = "WEBPORT_PROCESS_HELPER"

func TestTerminateChildProcessGroupReleasesDescendantPort(t *testing.T) {
	child, address := startChildProcessTree(t, false, false)

	_ = child.terminate(syscall.SIGINT, 500*time.Millisecond)

	assertPortReleased(t, address)
}

func TestTerminateChildProcessGroupForceKillsStubbornDescendant(t *testing.T) {
	child, address := startChildProcessTree(t, true, false)

	started := time.Now()
	_ = child.terminate(syscall.SIGTERM, 100*time.Millisecond)
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond {
		t.Fatalf("stubborn process group terminated before grace period elapsed: %v", elapsed)
	}

	assertPortReleased(t, address)
}

func TestTerminateChildProcessGroupAfterLeaderExits(t *testing.T) {
	child, address := startChildProcessTree(t, false, true)
	select {
	case <-child.done:
	case <-time.After(2 * time.Second):
		t.Fatal("process-group leader did not exit")
	}

	_ = child.terminate(syscall.SIGTERM, 500*time.Millisecond)

	assertPortReleased(t, address)
}

func TestStartupWaitsReturnShutdownSignal(t *testing.T) {
	for _, test := range []struct {
		name string
		wait func(*childProcess, <-chan os.Signal) os.Signal
	}{
		{
			name: "inferred port",
			wait: func(child *childProcess, signals <-chan os.Signal) os.Signal {
				_, got, err := waitForListener(
					"token-that-matches-no-process",
					"test-project",
					"main",
					time.Second,
					child,
					signals,
				)
				if err != nil {
					t.Fatalf("waitForListener returned error: %v", err)
				}
				return got
			},
		},
		{
			name: "explicit port",
			wait: func(child *childProcess, signals <-chan os.Signal) os.Signal {
				got, err := waitForPort(unusedPort(t), time.Second, child, signals)
				if err != nil {
					t.Fatalf("waitForPort returned error: %v", err)
				}
				return got
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=TestWebportProcessHelper")
			command.Env = append(os.Environ(), processHelperEnv+"=sleep")
			configureChildProcess(command)
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			child := trackChildProcess(command)
			t.Cleanup(func() {
				_ = child.terminate(syscall.SIGKILL, 100*time.Millisecond)
			})

			signals := make(chan os.Signal, 1)
			signals <- syscall.SIGINT
			if got := test.wait(child, signals); got != syscall.SIGINT {
				t.Fatalf("shutdown signal = %v, want SIGINT", got)
			}
		})
	}
}

func TestWebportProcessHelper(t *testing.T) {
	switch os.Getenv(processHelperEnv) {
	case "":
		return
	case "leader":
		command := exec.Command(os.Args[0], "-test.run=TestWebportProcessHelper")
		command.Env = append(os.Environ(), processHelperEnv+"=listener")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("WEBPORT_PROCESS_HELPER_EXIT_LEADER") == "1" {
			return
		}
		if os.Getenv("WEBPORT_PROCESS_HELPER_STUBBORN") == "1" {
			signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "listener":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if err := os.WriteFile(
			os.Getenv("WEBPORT_PROCESS_HELPER_ADDRESS"),
			[]byte(listener.Addr().String()),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("WEBPORT_PROCESS_HELPER_STUBBORN") == "1" {
			signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "sleep":
		for {
			time.Sleep(time.Hour)
		}
	default:
		t.Fatalf("unknown process helper mode %q", os.Getenv(processHelperEnv))
	}
}

func startChildProcessTree(t *testing.T, stubborn, exitLeader bool) (*childProcess, string) {
	t.Helper()
	addressPath := filepath.Join(t.TempDir(), "address")
	command := exec.Command(os.Args[0], "-test.run=TestWebportProcessHelper")
	command.Env = append(
		os.Environ(),
		processHelperEnv+"=leader",
		"WEBPORT_PROCESS_HELPER_ADDRESS="+addressPath,
	)
	if stubborn {
		command.Env = append(command.Env, "WEBPORT_PROCESS_HELPER_STUBBORN=1")
	}
	if exitLeader {
		command.Env = append(command.Env, "WEBPORT_PROCESS_HELPER_EXIT_LEADER=1")
	}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	configureChildProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	child := trackChildProcess(command)
	t.Cleanup(func() {
		_ = child.terminate(syscall.SIGKILL, 100*time.Millisecond)
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(addressPath)
		if err == nil && strings.TrimSpace(string(raw)) != "" {
			return child, strings.TrimSpace(string(raw))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("listener descendant did not publish its address")
	return nil, ""
}

func assertPortReleased(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		listener, err := net.Listen("tcp", address)
		if err == nil {
			_ = listener.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant still holds %s", address)
}

func unusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
