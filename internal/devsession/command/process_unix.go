//go:build linux || darwin

package command

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessGroup(command *exec.Cmd, signal os.Signal) error {
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

func processGroupAlive(command *exec.Cmd) bool {
	if command == nil || command.Process == nil {
		return false
	}
	err := syscall.Kill(-command.Process.Pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func status(state *os.ProcessState) (int, string) {
	if state == nil {
		return -1, ""
	}
	if wait, ok := state.Sys().(syscall.WaitStatus); ok {
		if wait.Signaled() {
			return -1, wait.Signal().String()
		}
	}
	return state.ExitCode(), ""
}
