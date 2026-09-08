//go:build !linux && !darwin

package command

import (
	"os"
	"os/exec"
)

func configureProcess(_ *exec.Cmd) {}

func signalProcessGroup(command *exec.Cmd, signal os.Signal) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Signal(signal)
}

func processGroupAlive(command *exec.Cmd) bool {
	return command != nil && command.Process != nil && command.ProcessState == nil
}

func status(state *os.ProcessState) (int, string) {
	if state == nil {
		return -1, ""
	}
	return state.ExitCode(), ""
}
