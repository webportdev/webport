//go:build linux || darwin

package state

import (
	"syscall"
)

func processAlive(identity ProcessIdentity) bool {
	if identity.PID <= 0 || syscall.Kill(identity.PID, 0) != nil {
		return false
	}
	if identity.StartTime == "" {
		return true
	}
	current, err := processStartTime(identity.PID)
	return err == nil && current == identity.StartTime
}

func currentProcessIdentity(pid int) ProcessIdentity {
	start, _ := processStartTime(pid)
	return ProcessIdentity{PID: pid, StartTime: start}
}
