package discovery

import (
	"bytes"
	"net"
	"strconv"
	"strings"
)

// parseLSOFListeners parses lsof's -F0 output. Process (p), file descriptor
// (f), type (t), and name (n) records are enough to associate each listener
// with its owner without depending on the human-readable column layout.
func parseLSOFListeners(data []byte) map[int][]listener {
	result := make(map[int][]listener)
	var pid int
	var networkType string

	fields := bytes.FieldsFunc(data, func(r rune) bool {
		return r == 0 || r == '\n'
	})
	for _, raw := range fields {
		if len(raw) < 2 {
			continue
		}
		value := string(raw[1:])
		switch raw[0] {
		case 'p':
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 1 {
				pid = 0
				continue
			}
			pid = parsed
		case 'f':
			networkType = ""
		case 't':
			networkType = value
		case 'n':
			if pid == 0 {
				continue
			}
			item, ok := parseLSOFEndpoint(value, networkType)
			if ok {
				result[pid] = append(result[pid], item)
			}
		}
	}
	return result
}

func parseLSOFEndpoint(value, networkType string) (listener, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "TCP "))
	if value == "" || strings.Contains(value, "->") {
		return listener{}, false
	}
	host, rawPort, err := net.SplitHostPort(value)
	if err != nil {
		return listener{}, false
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return listener{}, false
	}
	if host == "*" {
		if networkType == "IPv6" {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	} else {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || (!ip.IsLoopback() && !ip.IsUnspecified()) {
			return listener{}, false
		}
		if ip.IsUnspecified() {
			if ip.To4() == nil {
				host = "::1"
			} else {
				host = "127.0.0.1"
			}
		} else {
			host = ip.String()
		}
	}
	return listener{host: host, port: port}, true
}
