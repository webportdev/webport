//go:build darwin

package state

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func processStartTime(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}
