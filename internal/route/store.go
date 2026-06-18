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

// DeleteExpired removes and returns expired routes
func (s *Store) DeleteExpired(now time.Time) []Route {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expired []Route
	for id, route := range s.routes {
		if now.After(route.ExpiresAt) {
			expired = append(expired, route)
			delete(s.routes, id)
		}
	}
	return expired
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
