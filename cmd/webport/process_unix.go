//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const childShutdownGracePeriod = 2 * time.Second

type childProcess struct {
	command *exec.Cmd
	done    chan struct{}
	waitErr error
}

func configureChildProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func trackChildProcess(command *exec.Cmd) *childProcess {
	child := &childProcess{
		command: command,
		done:    make(chan struct{}),
	}
	go func() {
		child.waitErr = command.Wait()
		close(child.done)
	}()
	return child
}

func (c *childProcess) wait() error {
	<-c.done
	return c.waitErr
}

func signalChildProcess(command *exec.Cmd, signal os.Signal) error {
	if command == nil || command.Process == nil {
		return nil
	}
	value, ok := signal.(syscall.Signal)
	if !ok {
		return command.Process.Signal(signal)
	}
	err := syscall.Kill(-command.Process.Pid, value)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func childProcessGroupAlive(command *exec.Cmd) bool {
	if command == nil || command.Process == nil {
		return false
	}
	err := syscall.Kill(-command.Process.Pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// terminate signals the complete child process group, allows it to exit
// gracefully, then force-kills any remaining members. It always reaps the
// directly started process before returning.
func (c *childProcess) terminate(signal os.Signal, gracePeriod time.Duration) error {
	if gracePeriod <= 0 {
		gracePeriod = childShutdownGracePeriod
	}
	signalErr := signalChildProcess(c.command, signal)
	deadline := time.NewTimer(gracePeriod)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for childProcessGroupAlive(c.command) {
		select {
		case <-deadline.C:
			_ = signalChildProcess(c.command, syscall.SIGKILL)
			return errors.Join(c.wait(), signalErr)
		case <-ticker.C:
		}
	}
	return errors.Join(c.wait(), signalErr)
}
