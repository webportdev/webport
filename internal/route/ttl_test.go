package route

import (
	"sync"
	"testing"
	"time"
)

func TestTTLCheckerExpiration(t *testing.T) {
	store := NewStore()
	expiredCount := 0
	var expiredRoutes []Route

	// Create a checker with a short interval
	checker := NewTTLChecker(store, 100*time.Millisecond, func(routes []Route) {
		expiredCount = len(routes)
		expiredRoutes = routes
	})

	// Add a route that expires immediately
	store.Add(Route{
		Project:   "test",
		Branch:    "main",
		Port:      3000,
		Domain:    "test-main.example.com",
		ExpiresAt: time.Now().Add(-time.Hour), // Already expired
	})

	// Add a route that expires in the future
	store.Add(Route{
		Project:   "active",
		Branch:    "main",
		Port:      4000,
		Domain:    "active-main.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	checker.Start()

	// Wait for at least one tick
	time.Sleep(200 * time.Millisecond)

	checker.Stop()

	if expiredCount != 1 {
		t.Errorf("Expected 1 expired route, got %d", expiredCount)
	}

	if len(expiredRoutes) > 0 && expiredRoutes[0].Project != "test" {
		t.Errorf("Expected expired route to be 'test', got %s", expiredRoutes[0].Project)
	}

	// Verify the active route is still there
	id := RouteID{Project: "active", Branch: "main"}
	if _, ok := store.Get(id); !ok {
		t.Error("Active route was removed")
	}
}

func TestTTLCheckerNoExpiration(t *testing.T) {
	store := NewStore()
	callbackCalled := false

	checker := NewTTLChecker(store, 100*time.Millisecond, func(routes []Route) {
		callbackCalled = true
	})

	// Add routes that don't expire
	store.Add(Route{
		Project:   "test",
		Branch:    "main",
		Port:      3000,
		Domain:    "test-main.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	checker.Start()
	time.Sleep(200 * time.Millisecond)
	checker.Stop()

	if callbackCalled {
		t.Error("Callback was called when no routes expired")
	}
}

func TestTTLCheckerStop(t *testing.T) {
	store := NewStore()
	checker := NewTTLChecker(store, 100*time.Millisecond, func(routes []Route) {})

	checker.Start()

	// Stop should wait for the goroutine to finish
	doneCh := make(chan struct{})
	go func() {
		checker.Stop()
		close(doneCh)
	}()

	select {
	case <-doneCh:
		// Success
	case <-time.After(5 * time.Second):
		t.Error("Stop() took too long")
	}
}

func TestTTLCheckerConcurrentAccess(t *testing.T) {
	store := NewStore()
	var mu sync.Mutex
	var expiredRoutes [][]Route

	checker := NewTTLChecker(store, 50*time.Millisecond, func(routes []Route) {
		mu.Lock()
		expiredRoutes = append(expiredRoutes, routes)
		mu.Unlock()
	})

	// Add routes that will expire at different times (some already expired)
	for i := 0; i < 10; i++ {
		// First 5 routes are already expired, next 5 will expire soon
		expiresAt := time.Now().Add(time.Duration(i*10) * time.Millisecond)
		if i < 5 {
			expiresAt = time.Now().Add(-time.Hour) // Already expired
		}

		store.Add(Route{
			Project:   "test",
			Branch:    "branch",
			Port:      3000 + i,
			Domain:    "test-branch.example.com",
			ExpiresAt: expiresAt,
		})
	}

	checker.Start()
	time.Sleep(300 * time.Millisecond)
	checker.Stop()

	// Should not panic
	mu.Lock()
	count := len(expiredRoutes)
	mu.Unlock()

	if count == 0 {
		t.Error("Expected at least one expiration callback")
	}
}

func TestTTLCheckerWithNilCallback(t *testing.T) {
	store := NewStore()

	// Should not panic with nil callback
	checker := NewTTLChecker(store, 100*time.Millisecond, nil)

	store.Add(Route{
		Project:   "test",
		Branch:    "main",
		Port:      3000,
		Domain:    "test-main.example.com",
		ExpiresAt: time.Now().Add(-time.Hour),
	})

	checker.Start()
	time.Sleep(200 * time.Millisecond)
	checker.Stop()

	// Should not panic
}
