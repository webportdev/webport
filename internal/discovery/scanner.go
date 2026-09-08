// Package discovery finds development servers that opt in through their
// process environment.
package discovery

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/webportdev/webport/internal/route"
)

const (
	routeEnv          = "WEBPORT_ROUTE"
	portEnv           = "WEBPORT_APP_PORT"
	clientTokenEnv    = "WEBPORT_CLIENT_TOKEN"
	sessionManagedEnv = "WEBPORT_SESSION_MANAGED"
)

func isDiscoveryEnvKey(key string) bool {
	return key == routeEnv || key == portEnv || key == clientTokenEnv || key == sessionManagedEnv
}

type listener struct {
	host string
	port int
}

type process struct {
	pid       int
	startTime string
	env       map[string]string
	listeners []listener
}

// Scanner discovers opted-in HTTP listeners.
type Scanner struct {
	BaseDomain   string
	ProbeTimeout time.Duration
	// ClientToken restricts scans to a user-run webport dev process tree.
	ClientToken string
}

// Scan returns the complete currently discoverable process route set.
func (s Scanner) Scan(ctx context.Context) ([]route.Route, []error) {
	processes, scanErrors := scanProcesses(ctx)
	discovered := make([]route.Route, 0, len(processes))
	errs := append([]error(nil), scanErrors...)

	timeout := s.ProbeTimeout
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}

	for _, proc := range processes {
		if !s.acceptsProcess(proc) {
			continue
		}
		spec := strings.TrimSpace(proc.env[routeEnv])
		id, err := route.RouteIDFromString(spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("pid %d has invalid %s=%q: expected project:branch", proc.pid, routeEnv, spec))
			continue
		}

		selected, err := selectListener(ctx, proc, timeout)
		if err != nil {
			errs = append(errs, fmt.Errorf("pid %d (%s): %w", proc.pid, id.String(), err))
			continue
		}

		discovered = append(discovered, route.Route{
			Project: id.Project,
			Branch:  id.Branch,
			Host:    selected.host,
			Port:    selected.port,
			Domain:  route.BuildDomain(s.BaseDomain, id.Project, id.Branch),
			Source:  route.SourceProcess,
			Owner:   fmt.Sprintf("pid:%d:%s", proc.pid, proc.startTime),
		})
	}

	return discovered, errs
}

func (s Scanner) acceptsProcess(proc process) bool {
	if s.ClientToken != "" {
		return proc.env[clientTokenEnv] == s.ClientToken
	}
	// Configured foreground sessions publish their own readiness-gated
	// leases. Their generic route environment is context, not an opt-in.
	return proc.env[sessionManagedEnv] != "1"
}

func selectListener(ctx context.Context, proc process, timeout time.Duration) (listener, error) {
	return selectListenerWithProbe(proc, func(candidate listener) bool {
		return respondsHTTP(ctx, candidate, timeout)
	})
}

func selectListenerWithProbe(proc process, probe func(listener) bool) (listener, error) {
	listeners := uniqueListeners(proc.listeners)
	if raw := strings.TrimSpace(proc.env[portEnv]); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return listener{}, fmt.Errorf("invalid %s=%q", portEnv, raw)
		}
		for _, candidate := range listeners {
			if candidate.port == port {
				return candidate, nil
			}
		}
		return listener{}, fmt.Errorf("%s=%d is not a TCP listener owned by the process", portEnv, port)
	}

	var httpListeners []listener
	for _, candidate := range listeners {
		if probe(candidate) {
			httpListeners = append(httpListeners, candidate)
		}
	}
	switch len(httpListeners) {
	case 1:
		return httpListeners[0], nil
	case 0:
		if len(listeners) == 0 {
			return listener{}, fmt.Errorf("no loopback-reachable TCP listener found")
		}
		return listener{}, fmt.Errorf("no HTTP listener identified; set %s explicitly", portEnv)
	default:
		ports := make([]string, 0, len(httpListeners))
		for _, item := range httpListeners {
			ports = append(ports, strconv.Itoa(item.port))
		}
		return listener{}, fmt.Errorf("multiple HTTP listeners identified (%s); set %s explicitly", strings.Join(ports, ", "), portEnv)
	}
}

func uniqueListeners(items []listener) []listener {
	seen := make(map[string]struct{}, len(items))
	result := make([]listener, 0, len(items))
	for _, item := range items {
		key := net.JoinHostPort(item.host, strconv.Itoa(item.port))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func respondsHTTP(parent context.Context, target listener, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(target.host, strconv.Itoa(target.port)))
	if err != nil {
		return false
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	if _, err := io.WriteString(conn, "HEAD / HTTP/1.0\r\nHost: localhost\r\n\r\n"); err != nil {
		return false
	}

	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return false
	}
	return strings.HasPrefix(string(buf[:n]), "HTTP/")
}
