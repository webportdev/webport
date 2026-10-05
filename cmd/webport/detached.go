package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
			_, err = fmt.Fprintf(out, "development session detached (PID %d); log: %s\n", child.Process.Pid, log.Name())
			return err
		}
	case <-done:
	case <-timer.C:
		_ = child.Process.Signal(syscall.SIGTERM)
		return fmt.Errorf("detached startup timed out; log: %s", log.Name())
	}
	return fmt.Errorf("detached development session failed to start; log: %s", log.Name())
}
