// Package route provides route storage and management for webport.
//
// It includes:
//   - Store: Thread-safe storage for routes using sync.RWMutex
//   - RouteID: Composite key for routes (project + branch)
//   - TTLChecker: Background goroutine that expires inactive routes
//
// Route IDs are formatted as "{project}:{branch}". Clients must URL-escape
// branch slashes when route IDs are used as HTTP path segments.
package route

import (
	"fmt"
	"sync"
	"time"
)

// Store provides concurrent-safe route storage
type Store struct {
	mu     sync.RWMutex
	routes map[RouteID]Route
}

// NewStore creates a new route store
func NewStore() *Store {
	return &Store{
		routes: make(map[RouteID]Route),
	}
}

// Add or update a route (idempotent)
func (s *Store) Add(route Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := RouteID{Project: route.Project, Branch: route.Branch}
	// If updating existing route, preserve CreatedAt
	if existing, exists := s.routes[id]; exists {
		if route.CreatedAt.IsZero() {
			route.CreatedAt = existing.CreatedAt
		}
	} else if route.CreatedAt.IsZero() {
		route.CreatedAt = time.Now()
	}
	s.routes[id] = route
	return nil
}

// Get retrieves a route by ID
func (s *Store) Get(id RouteID) (Route, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	route, ok := s.routes[id]
	return route, ok
}

// Delete removes a route
func (s *Store) Delete(id RouteID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.routes[id]; ok {
		delete(s.routes, id)
		return true
	}
	return false
}

// List returns all routes (snapshot)
func (s *Store) List() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()

	routes := make([]Route, 0, len(s.routes))
	for _, route := range s.routes {
		routes = append(routes, route)
	}
	return routes
}

// SetManual atomically replaces all manually registered routes while
// preserving process-discovered routes. It returns the previous manual set.
func (s *Store) SetManual(desired []Route) []Route {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous := make([]Route, 0)
	next := make(map[RouteID]Route, len(s.routes)+len(desired))
	for id, existing := range s.routes {
		if existing.IsDiscovered() {
			next[id] = existing
		} else {
			previous = append(previous, existing)
		}
	}
	for _, candidate := range desired {
		id := RouteID{Project: candidate.Project, Branch: candidate.Branch}
		if existing, ok := s.routes[id]; ok && candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = existing.CreatedAt
		}
		if candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = time.Now()
		}
		next[id] = candidate
	}
	s.routes = next
	return previous
}

// DeleteExpired removes and returns expired routes
func (s *Store) DeleteExpired(now time.Time) []Route {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expired []Route
	for id, route := range s.routes {
		if route.IsDiscovered() {
			continue
		}
		if now.After(route.ExpiresAt) {
			expired = append(expired, route)
			delete(s.routes, id)
		}
	}
	return expired
}

// ReconcileDiscovered atomically replaces the process-discovered portion of
// the route set. Manual API routes always take precedence.
func (s *Store) ReconcileDiscovered(desired []Route) (bool, []error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make(map[RouteID]Route, len(desired))
	blocked := make(map[RouteID]struct{})
	var conflicts []error

	manualDomains := make(map[string]RouteID, len(s.routes))
	for id, existing := range s.routes {
		if !existing.IsDiscovered() {
			manualDomains[existing.Domain] = id
		}
	}
	discoveredDomains := make(map[string]RouteID, len(desired))

	for _, candidate := range desired {
		id := RouteID{Project: candidate.Project, Branch: candidate.Branch}
		if _, ok := blocked[id]; ok {
			continue
		}
		candidate.Source = SourceProcess
		candidate.ExpiresAt = time.Time{}

		if existing, ok := s.routes[id]; ok && !existing.IsDiscovered() {
			conflicts = append(conflicts, fmt.Errorf(
				"discovered route %s conflicts with a manually registered route", id.String(),
			))
			continue
		}
		if owner, ok := manualDomains[candidate.Domain]; ok && owner != id {
			conflicts = append(conflicts, fmt.Errorf(
				"discovered domain %s conflicts with route %s", candidate.Domain, owner.String(),
			))
			continue
		}
		if owner, ok := discoveredDomains[candidate.Domain]; ok && owner != id {
			delete(next, owner)
			blocked[owner] = struct{}{}
			blocked[id] = struct{}{}
			conflicts = append(conflicts, fmt.Errorf(
				"multiple process routes produce discovered domain %s", candidate.Domain,
			))
			continue
		}
		if existing, ok := next[id]; ok {
			if existing.Port != candidate.Port || existing.Owner != candidate.Owner {
				delete(next, id)
				blocked[id] = struct{}{}
				conflicts = append(conflicts, fmt.Errorf(
					"multiple processes claim discovered route %s", id.String(),
				))
			}
			continue
		}
		next[id] = candidate
		discoveredDomains[candidate.Domain] = id
	}

	changed := false
	for id, existing := range s.routes {
		if !existing.IsDiscovered() {
			continue
		}
		candidate, ok := next[id]
		if !ok {
			delete(s.routes, id)
			changed = true
			continue
		}
		if existing.Port != candidate.Port || existing.Host != candidate.Host ||
			existing.Domain != candidate.Domain ||
			existing.Owner != candidate.Owner {
			candidate.CreatedAt = existing.CreatedAt
			s.routes[id] = candidate
			changed = true
		}
		delete(next, id)
	}

	for id, candidate := range next {
		if candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = time.Now()
		}
		s.routes[id] = candidate
		changed = true
	}

	return changed, conflicts
}

// Clear removes all routes and returns them (for shutdown)
func (s *Store) Clear() []Route {
	s.mu.Lock()
	defer s.mu.Unlock()

	routes := make([]Route, 0, len(s.routes))
	for _, route := range s.routes {
		routes = append(routes, route)
	}
	s.routes = make(map[RouteID]Route)
	return routes
}

// Count returns the number of routes
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.routes)
}
