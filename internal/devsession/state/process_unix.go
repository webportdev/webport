//go:build linux || darwin

package state

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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

func processStartTime(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 22 {
		return "", errors.New("process stat is incomplete")
	}
	return fields[21], nil
}

func currentProcessIdentity(pid int) ProcessIdentity {
	start, _ := processStartTime(pid)
	return ProcessIdentity{PID: pid, StartTime: start}
}

var _ = strconv.Itoa
