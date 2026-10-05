package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/webportdev/webport/internal/devsession/state"
	"syscall"
	"time"
)

const detachedReadyEnv = "WEBPORT_DETACHED_READY_FD"

func notifyDetachedReady() {
	if os.Getenv(detachedReadyEnv) != "3" {
		return
	}
	_ = os.Unsetenv(detachedReadyEnv)
	file := os.NewFile(3, "detached-ready")
	if file != nil {
		_, _ = fmt.Fprintln(file, "ready")
		_ = file.Close()
	}
}

func detachedArgs(args []string) ([]string, error) {
	result := []string{"dev"}
	command := false
	for _, arg := range args {
		if arg == "--" {
			command = true
		}
		if !command && (arg == "-d" || arg == "--detach" || strings.HasPrefix(arg, "-d=") || strings.HasPrefix(arg, "--detach=")) {
			continue
		}
		result = append(result, arg)
	}
	for _, arg := range result[1:] {
		if arg == "--" {
			break
		}
		if isDevInspectionOperation(arg) || arg == "exec" {
			return nil, errors.New("detached mode requires a development session or command")
		}
	}
	return result, nil
}

func startDetachedDev(args []string, timeout time.Duration, out io.Writer) error {
	childArgs, err := detachedArgs(args)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.CreateTemp("", "webport-dev-*.log")
	if err != nil {
		return err
	}
	defer log.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	child := exec.Command(executable, childArgs...)
	child.Stdout, child.Stderr = log, log
	child.Env = withEnvironment(os.Environ(), map[string]string{detachedReadyEnv: "3"})
	child.ExtraFiles = []*os.File{writer}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	_ = writer.Close()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	ready := make(chan bool, 1)
	go func() { line, _ := bufio.NewReader(reader).ReadString('\n'); ready <- line == "ready\n" }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ok := <-ready:
		if ok {
			instances, stateErr := state.ListLive()
			if err := writeDetachedSummary(out, child.Process.Pid, log.Name(), instances); err != nil {
				return err
			}
			if stateErr != nil && len(detachedServiceURLs(instances, child.Process.Pid)) == 0 {
				_, err = fmt.Fprintf(out, "service URLs unavailable: %v\n", stateErr)
			}
			return err
		}
	case <-done:
	case <-timer.C:
		_ = child.Process.Signal(syscall.SIGTERM)
		return fmt.Errorf("detached startup timed out; log: %s", log.Name())
	}
	return fmt.Errorf("detached development session failed to start; log: %s", log.Name())
}

// Select by supervisor PID rather than checkout: multiple wrappers can run in
// the same worktree, and detached configured sessions may use --config PATH.
func detachedServiceURLs(instances []state.LiveState, pid int) map[string]string {
	urls := make(map[string]string)
	for _, live := range instances {
		if len(live.PIDs) == 0 || live.PIDs[0].PID != pid {
			continue
		}
		for service, route := range live.Routes {
			if route.State == "active" && route.URL != "" {
				urls[service] = route.URL
			}
		}
		for endpoint, url := range live.Endpoints {
			if url != "" {
				urls[endpoint] = url
			}
		}
	}
	return urls
}

func writeDetachedSummary(out io.Writer, pid int, logPath string, instances []state.LiveState) error {
	if _, err := fmt.Fprintf(out, "development session detached (PID %d); log: %s\n", pid, logPath); err != nil {
		return err
	}
	urls := detachedServiceURLs(instances, pid)
	names := make([]string, 0, len(urls))
	for name := range urls {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintf(out, "%s: %s\n", name, urls[name]); err != nil {
			return err
		}
	}
	return nil
}
