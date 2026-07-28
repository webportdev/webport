package discovery

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/webportdev/webport/internal/route"
)

// Manager periodically reconciles process-discovered routes.
type Manager struct {
	scanner  Scanner
	store    *route.Store
	interval time.Duration
	publish  func() error
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
}

func NewManager(scanner Scanner, store *route.Store, interval time.Duration, publish func() error) *Manager {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Manager{
		scanner:  scanner,
		store:    store,
		interval: interval,
		publish:  publish,
		done:     make(chan struct{}),
	}
}

func (m *Manager) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	go m.run(ctx)
}

func (m *Manager) Stop() {
	m.once.Do(func() {
		if m.cancel != nil {
			m.cancel()
			<-m.done
		}
	})
}

func (m *Manager) run(ctx context.Context) {
	defer close(m.done)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	last := make(map[route.RouteID]route.Route)
	misses := make(map[route.RouteID]int)
	reported := make(map[string]struct{})

	reconcile := func() {
		scanned, scanErrors := m.scanner.Scan(ctx)
		currentErrors := make(map[string]struct{}, len(scanErrors))
		for _, err := range scanErrors {
			message := err.Error()
			currentErrors[message] = struct{}{}
			if _, already := reported[message]; !already {
				log.Printf("WARN: process discovery: %v", err)
			}
		}

		present := make(map[route.RouteID]int, len(scanned))
		for _, item := range scanned {
			id := route.RouteID{Project: item.Project, Branch: item.Branch}
			present[id]++
			if present[id] == 1 {
				last[id] = item
				misses[id] = 0
			} else {
				delete(last, id)
				delete(misses, id)
			}
		}

		// Retain a vanished route for one extra scan. This avoids visible route
		// churn from a transient /proc or startup race while still removing a
		// stopped development server promptly.
		routes := append([]route.Route(nil), scanned...)
		for id, previous := range last {
			if present[id] > 0 {
				continue
			}
			misses[id]++
			if misses[id] < 2 {
				routes = append(routes, previous)
				continue
			}
			delete(last, id)
			delete(misses, id)
		}
		changed, conflicts := m.store.ReconcileDiscovered(routes)
		for _, err := range conflicts {
			message := err.Error()
			currentErrors[message] = struct{}{}
			if _, already := reported[message]; !already {
				log.Printf("WARN: process discovery: %v", err)
			}
		}
		reported = currentErrors
		if changed && m.publish != nil {
			if err := m.publish(); err != nil {
				log.Printf("WARN: failed to publish Traefik configuration after process discovery: %v", err)
			}
		}
	}

	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
