//go:build !linux && !darwin

package state

import "syscall"

func processAlive(identity ProcessIdentity) bool {
	return identity.PID > 0 && syscall.Kill(identity.PID, 0) == nil
}

func currentProcessIdentity(pid int) ProcessIdentity { return ProcessIdentity{PID: pid} }
