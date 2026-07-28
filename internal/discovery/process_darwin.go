//go:build darwin

package discovery

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const lsofPath = "/usr/sbin/lsof"

func scanProcesses(ctx context.Context) ([]process, []error) {
	command := exec.CommandContext(ctx, lsofPath,
		"-nP", "-a", "-iTCP", "-sTCP:LISTEN", "-F0pftn")
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, []error{ctx.Err()}
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || len(exitErr.Stderr) > 0 {
			return nil, []error{fmt.Errorf("run %s: %w", lsofPath, err)}
		}
		// lsof exits 1 when its selection has no matches.
		return nil, nil
	}

	owned := parseLSOFListeners(output)
	found := make([]process, 0, len(owned))
	var scanErrors []error
	for pid, listeners := range owned {
		env, err := readDarwinEnvironment(pid)
		if err != nil {
			// Processes can disappear or exec between the lsof and sysctl
			// snapshots. Treat those races as absent.
			continue
		}
		if strings.TrimSpace(env[routeEnv]) == "" {
			continue
		}
		found = append(found, process{
			pid:       pid,
			startTime: readDarwinStartTime(pid),
			env:       env,
			listeners: uniqueListeners(listeners),
		})
	}
	return found, scanErrors
}

func readDarwinEnvironment(pid int) (map[string]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseDarwinProcArgs(raw)
}

func readDarwinStartTime(pid int) string {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "unknown"
	}
	start := info.Proc.P_starttime
	return strconv.FormatInt(start.Sec, 10) + "." + strconv.FormatInt(int64(start.Usec), 10)
}
