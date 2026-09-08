// Package routes manages independent route leases for configured services.
package routes

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/plan"
)

type State string

const (
	Pending     State = "pending"
	Active      State = "active"
	Unavailable State = "unavailable"
	Recovering  State = "recovering"
	Failed      State = "failed"
	Released    State = "released"
)

type Failure struct {
	Service  string
	Required bool
	Error    error
}

type Entry struct {
	Route  plan.Route `json:"route"`
	State  State      `json:"state"`
	Error  string     `json:"error,omitempty"`
	lease  *daemon.LeaseManager
	cancel context.CancelFunc
}

type Manager struct {
	client   daemon.Client
	ttl      time.Duration
	interval time.Duration
	failures chan Failure
	mu       sync.Mutex
	entries  map[string]*Entry
}

func NewManager(client daemon.Client, ttl, interval time.Duration) (*Manager, error) {
	if client == nil {
		return nil, errors.New("route client is required")
	}
	if ttl <= 0 || interval <= 0 || interval >= ttl {
		return nil, errors.New("route TTL and heartbeat interval are invalid")
	}
	return &Manager{client: client, ttl: ttl, interval: interval, failures: make(chan Failure, 16), entries: make(map[string]*Entry)}, nil
}

func (m *Manager) Activate(ctx context.Context, route plan.Route) error {
	entry := &Entry{Route: route, State: Pending}
	m.mu.Lock()
	if _, exists := m.entries[route.Service]; exists {
		m.mu.Unlock()
		return fmt.Errorf("route for service %q is already active", route.Service)
	}
	m.entries[route.Service] = entry
	m.mu.Unlock()
	lease, err := daemon.NewLeaseManager(m.client, daemon.RouteRequest{
		Project: route.Project, Branch: route.Branch, Port: route.Port, TTL: m.ttl,
	}, m.interval)
	if err == nil {
		err = lease.Start(ctx)
	}
	if err != nil {
		m.mu.Lock()
		if route.Optional {
			entry.State = Unavailable
			entry.Error = err.Error()
			m.mu.Unlock()
			return nil
		}
		entry.State = Failed
		entry.Error = err.Error()
		m.mu.Unlock()
		return err
	}
	heartbeatCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	entry.lease, entry.cancel = lease, cancel
	entry.State = Active
	m.mu.Unlock()
	go func() {
		err := lease.Run(heartbeatCtx)
		if err == nil || errors.Is(err, context.Canceled) || heartbeatCtx.Err() != nil {
			return
		}
		m.mu.Lock()
		entry.State = Failed
		entry.Error = err.Error()
		m.mu.Unlock()
		m.failures <- Failure{Service: route.Service, Required: !route.Optional, Error: err}
	}()
	return nil
}

func (m *Manager) ReleaseAll(ctx context.Context) error {
	m.mu.Lock()
	services := make([]string, 0, len(m.entries))
	for service := range m.entries {
		services = append(services, service)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(services)))
	entries := make([]*Entry, 0, len(services))
	for _, service := range services {
		entries = append(entries, m.entries[service])
	}
	m.mu.Unlock()
	var combined error
	for _, entry := range entries {
		if entry.cancel != nil {
			entry.cancel()
		}
		if entry.lease != nil {
			if err := entry.lease.Stop(ctx); err != nil {
				combined = errors.Join(combined, fmt.Errorf("release route %s: %w", entry.Route.Service, err))
			}
		}
		m.mu.Lock()
		entry.State = Released
		m.mu.Unlock()
	}
	return combined
}

func (m *Manager) Failures() <-chan Failure { return m.failures }

func (m *Manager) Snapshot() map[string]Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]Entry, len(m.entries))
	for name, entry := range m.entries {
		copy := *entry
		copy.lease, copy.cancel = nil, nil
		result[name] = copy
	}
	return result
}

func (m *Manager) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.entries {
		if entry.State == Active || entry.State == Recovering {
			return true
		}
	}
	return false
}
