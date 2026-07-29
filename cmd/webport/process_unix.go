//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureChildProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalChildProcess(command *exec.Cmd, signal os.Signal) error {
	value, ok := signal.(syscall.Signal)
	if !ok {
		return command.Process.Signal(signal)
	}
	return syscall.Kill(-command.Process.Pid, value)
}
