//go:build linux

package discovery

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func scanProcesses(_ context.Context) ([]process, []error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, []error{fmt.Errorf("read /proc: %w", err)}
	}

	var found []process
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		base := filepath.Join("/proc", entry.Name())
		env, err := readEnvironment(filepath.Join(base, "environ"))
		if err != nil || strings.TrimSpace(env[routeEnv]) == "" {
			continue
		}
		inodes := readSocketInodes(filepath.Join(base, "fd"))
		if len(inodes) == 0 {
			continue
		}
		listeners := append(
			readTCPListeners(filepath.Join(base, "net", "tcp"), inodes, false),
			readTCPListeners(filepath.Join(base, "net", "tcp6"), inodes, true)...,
		)
		found = append(found, process{
			pid:       pid,
			startTime: readStartTime(filepath.Join(base, "stat")),
			env:       env,
			listeners: listeners,
		})
	}
	return found, nil
}

func readEnvironment(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string)
	for _, entry := range strings.Split(string(data), "\x00") {
		key, value, ok := strings.Cut(entry, "=")
		if ok && isDiscoveryEnvKey(key) {
			env[key] = value
		}
	}
	return env, nil
}

func readSocketInodes(dir string) map[string]struct{} {
	inodes := make(map[string]struct{})
	entries, err := os.ReadDir(dir)
	if err != nil {
		return inodes
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = struct{}{}
		}
	}
	return inodes
}

func readTCPListeners(path string, owned map[string]struct{}, ipv6 bool) []listener {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var result []listener
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		if _, ok := owned[fields[9]]; !ok {
			continue
		}
		address, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		port64, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || port64 == 0 {
			continue
		}
		ip := decodeProcIP(address, ipv6)
		if ip == nil || (!ip.IsLoopback() && !ip.IsUnspecified()) {
			continue
		}
		host := ip.String()
		if ip.IsUnspecified() {
			if ipv6 {
				host = "::1"
			} else {
				host = "127.0.0.1"
			}
		}
		result = append(result, listener{host: host, port: int(port64)})
	}
	return result
}

func decodeProcIP(value string, ipv6 bool) net.IP {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil
	}
	if !ipv6 {
		if len(raw) != net.IPv4len {
			return nil
		}
		reverse(raw)
		return net.IP(raw)
	}
	if len(raw) != net.IPv6len {
		return nil
	}
	for start := 0; start < len(raw); start += 4 {
		reverse(raw[start : start+4])
	}
	return net.IP(raw)
}

func reverse(value []byte) {
	for left, right := 0, len(value)-1; left < right; left, right = left+1, right-1 {
		value[left], value[right] = value[right], value[left]
	}
}

func readStartTime(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 {
		return "unknown"
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) <= 19 {
		return "unknown"
	}
	return fields[19]
}
