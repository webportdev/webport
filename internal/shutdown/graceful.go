// Package shutdown provides graceful shutdown handling for webport.
//
// It ensures that on termination:
//  1. The TTL checker is stopped
//  2. All routes are cleared from the store
//  3. An empty Caddyfile is written (removing all proxies)
//  4. Caddy is reloaded to apply changes
//  5. The HTTP server is shut down gracefully
//  6. Registered exit callbacks are invoked
//
// This prevents orphaned routes and ensures clean service restarts.
package shutdown

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/webportdev/webport/internal/caddy"
	"github.com/webportdev/webport/internal/route"
)

// Manager handles graceful shutdown
type Manager struct {
	store           *route.Store
	writer          caddy.Writer
	server          *http.Server
	ttl             *route.TTLChecker
	shutdownTimeout time.Duration
	onExit          func() // Optional callback
	mu              sync.Mutex
	tlsCfg          caddy.TLSConfig
	baseDomain      string
}

// NewManager creates a shutdown manager
func NewManager(store *route.Store, writer caddy.Writer, server *http.Server, ttl *route.TTLChecker, shutdownTimeout time.Duration, tlsCfg caddy.TLSConfig, baseDomain string) *Manager {
	if shutdownTimeout == 0 {
		shutdownTimeout = 5 * time.Second // default fallback
	}
	return &Manager{
		store:           store,
		writer:          writer,
		server:          server,
		ttl:             ttl,
		shutdownTimeout: shutdownTimeout,
		tlsCfg:          tlsCfg,
		baseDomain:      baseDomain,
	}
}

// OnExit registers a callback for exit
func (m *Manager) OnExit(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onExit = fn
}

// Wait blocks until termination signal received
func (m *Manager) Wait() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	log.Println("Shutdown signal received")
	m.Shutdown()
}

// Shutdown performs graceful shutdown
func (m *Manager) Shutdown() {
	log.Println("Starting graceful shutdown...")

	// Stop TTL checker
	if m.ttl != nil {
		m.ttl.Stop()
	}

	// Clear all routes (delete from Caddy)
	log.Println("Clearing all routes...")
	cleared := m.store.Clear()
	if len(cleared) > 0 {
		// Write empty Caddyfile
		content, err := caddy.GenerateCaddyfile([]caddy.RouteInfo{}, m.tlsCfg, m.baseDomain)
		if err == nil {
			if err := m.writer.Write(content); err != nil {
				log.Printf("WARN: failed to write empty Caddyfile: %v", err)
			} else {
				log.Printf("Cleared %d route(s) from Caddy", len(cleared))
			}
		}
	}

	// Shutdown HTTP server
	if m.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), m.shutdownTimeout)
		defer cancel()
		if err := m.server.Shutdown(ctx); err != nil {
			log.Printf("ERROR: server shutdown error: %v", err)
		}
	}

	// Run exit callback
	m.mu.Lock()
	if m.onExit != nil {
		m.onExit()
	}
	m.mu.Unlock()

	log.Println("Shutdown complete")
}

// SetupSignals sets up signal handling and returns a manager
func SetupSignals(store *route.Store, writer caddy.Writer, server *http.Server, ttl *route.TTLChecker, shutdownTimeout time.Duration, tlsCfg caddy.TLSConfig, baseDomain string) *Manager {
	return NewManager(store, writer, server, ttl, shutdownTimeout, tlsCfg, baseDomain)
}
