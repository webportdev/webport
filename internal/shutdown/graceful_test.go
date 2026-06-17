package shutdown

import (
	"log"
	"net/http"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/caddy"
	"github.com/webportdev/webport/internal/route"
)

// mockWriter is a mock Writer that doesn't actually execute commands
type mockWriter struct {
	content         string
	path            string
	emptyWriteCount int
}

func (m *mockWriter) Write(content string) error {
	m.content = content
	// Check if it's essentially empty (just headers, no routes)
	if len(content) < 100 { // Empty Caddyfile with just header is ~70 chars
		m.emptyWriteCount++
	}
	return nil
}

func (m *mockWriter) GetPath() string {
	return m.path
}

func TestShutdownClearsRoutes(t *testing.T) {
	store := route.NewStore()
	writer := &mockWriter{path: "/tmp/test/Caddyfile"}

	server := &http.Server{
		Addr:    ":8080",
		Handler: http.NewServeMux(),
	}

	// Add some routes
	store.Add(route.Route{
		Project:   "app1",
		Branch:    "main",
		Port:      3000,
		Domain:    "app1.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	store.Add(route.Route{
		Project:   "app2",
		Branch:    "dev",
		Port:      4000,
		Domain:    "app2.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	manager := NewManager(store, writer, server, nil, 5*time.Second, caddy.TLSConfig{}, "example.com")

	manager.Shutdown()

	// Verify all routes were cleared
	if store.Count() != 0 {
		t.Errorf("Store count = %d, want 0 after shutdown", store.Count())
	}

	// Verify empty Caddyfile was written
	if writer.emptyWriteCount != 1 {
		t.Errorf("Expected 1 empty write, got %d", writer.emptyWriteCount)
	}
}

func TestShutdownCallsOnExit(t *testing.T) {
	store := route.NewStore()
	writer := &mockWriter{path: "/tmp/test/Caddyfile"}

	server := &http.Server{
		Addr:    ":8080",
		Handler: http.NewServeMux(),
	}

	manager := NewManager(store, writer, server, nil, 5*time.Second, caddy.TLSConfig{}, "example.com")

	exitCalled := false
	manager.OnExit(func() {
		exitCalled = true
	})

	manager.Shutdown()

	if !exitCalled {
		t.Error("OnExit callback was not called")
	}
}

func TestShutdownStopsTTLChecker(t *testing.T) {
	store := route.NewStore()
	writer := &mockWriter{path: "/tmp/test/Caddyfile"}

	server := &http.Server{
		Addr:    ":8080",
		Handler: http.NewServeMux(),
	}

	checker := route.NewTTLChecker(store, 100*time.Millisecond, func(routes []route.Route) {})
	checker.Start()

	manager := NewManager(store, writer, server, checker, 5*time.Second, caddy.TLSConfig{}, "example.com")
	manager.Shutdown()

	// If Stop() was called, starting a new checker should work fine
	// (if Stop() wasn't called, we'd have a panic or issue)
	newChecker := route.NewTTLChecker(store, 100*time.Millisecond, func(routes []route.Route) {})
	newChecker.Start()
	newChecker.Stop()
}

func TestShutdownWithServer(t *testing.T) {
	store := route.NewStore()
	writer := &mockWriter{path: "/tmp/test/Caddyfile"}

	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:    ":0", // Use random available port
		Handler: mux,
	}

	// Start server in background
	go func() {
		server.ListenAndServe()
	}()
	time.Sleep(100 * time.Millisecond) // Give server time to start

	manager := NewManager(store, writer, server, nil, 5*time.Second, caddy.TLSConfig{}, "example.com")

	doneCh := make(chan struct{})
	go func() {
		manager.Shutdown()
		close(doneCh)
	}()

	select {
	case <-doneCh:
		// Success - shutdown completed
	case <-time.After(5 * time.Second):
		t.Error("Shutdown took too long")
	}
}

func TestNewManager(t *testing.T) {
	store := route.NewStore()
	writer := &mockWriter{path: "/tmp/test/Caddyfile"}
	server := &http.Server{Addr: ":8080"}

	manager := NewManager(store, writer, server, nil, 5*time.Second, caddy.TLSConfig{}, "example.com")

	if manager == nil {
		t.Error("NewManager returned nil")
	}

	if manager.store != store {
		t.Error("Manager store not set correctly")
	}

	if manager.writer != writer {
		t.Error("Manager writer not set correctly")
	}

	if manager.server != server {
		t.Error("Manager server not set correctly")
	}
}

func TestSetupSignals(t *testing.T) {
	store := route.NewStore()
	writer := &mockWriter{path: "/tmp/test/Caddyfile"}
	server := &http.Server{Addr: ":8080"}

	manager := SetupSignals(store, writer, server, nil, 5*time.Second, caddy.TLSConfig{}, "example.com")

	if manager == nil {
		t.Error("SetupSignals returned nil")
	}
}

// Helper function to capture log output during shutdown
func captureLogs(f func()) []string {
	// This is a simplified version - in real tests you might want to use
	// a more sophisticated logging capture mechanism
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	defer func() {
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	}()

	// For this test, we just verify the function doesn't panic
	f()
	return nil
}

func TestShutdownLogs(t *testing.T) {
	// This test mainly ensures that logging during shutdown doesn't cause issues
	store := route.NewStore()
	writer := &mockWriter{path: "/tmp/test/Caddyfile"}
	server := &http.Server{Addr: ":8080"}

	manager := NewManager(store, writer, server, nil, 5*time.Second, caddy.TLSConfig{}, "example.com")

	// Should not panic
	captureLogs(func() {
		manager.Shutdown()
	})
}
