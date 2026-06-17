// Package runtime manages the lifecycle of a webport route.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config holds the runtime configuration.
type Config struct {
	Project  string
	Branch   string
	Port     int
	TTL      time.Duration
	APIAddr  string
	Interval time.Duration
	Verbose  bool
}

// Manager handles route registration, heartbeat, and cleanup.
type Manager struct {
	cfg    Config
	client *http.Client
	route  *RouteInfo
	mu     sync.Mutex
}

// RouteInfo represents a registered route.
type RouteInfo struct {
	Project   string    `json:"project"`
	Branch    string    `json:"branch"`
	Port      int       `json:"port"`
	Domain    string    `json:"domain"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RegisterRequest is the payload for route registration.
type RegisterRequest struct {
	Project string `json:"project"`
	Branch  string `json:"branch"`
	Port    int    `json:"port"`
	TTL     int    `json:"ttl"`
}

// NewManager creates a new Manager.
func NewManager(cfg Config) (*Manager, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	return &Manager{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

func validateConfig(cfg Config) error {
	// Allow empty project/branch for config querying only
	if cfg.Port > 0 && (cfg.Port <= 0 || cfg.Port > 65535) {
		return fmt.Errorf("port must be between 1-65535, got %d", cfg.Port)
	}
	if cfg.Port > 0 && cfg.TTL < time.Second {
		return fmt.Errorf("TTL must be at least 1 second")
	}
	if cfg.Port > 0 && cfg.Interval < time.Second {
		return fmt.Errorf("interval must be at least 1 second")
	}
	return nil
}

// Start registers the route and begins the heartbeat loop.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.registerRoute(); err != nil {
		return fmt.Errorf("failed to register route: %w", err)
	}

	// Start heartbeat in background
	go m.heartbeatLoop(ctx)

	return nil
}

// Stop unregisters the route.
func (m *Manager) Stop() error {
	return m.unregisterRoute()
}

// registerRoute registers the route with webport.
func (m *Manager) registerRoute() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ttlSec := int(m.cfg.TTL.Seconds())
	req := RegisterRequest{
		Project: m.cfg.Project,
		Branch:  m.cfg.Branch,
		Port:    m.cfg.Port,
		TTL:     ttlSec,
	}

	url := m.apiURL("/routes")
	resp, err := m.client.Post(url, "application/json", jsonReader(req))
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var route RouteInfo
	if err := json.NewDecoder(resp.Body).Decode(&route); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	m.route = &route
	log.Printf("Route registered: https://%s -> localhost:%d", route.Domain, route.Port)

	return nil
}

// heartbeatLoop sends periodic heartbeats to keep the route alive.
func (m *Manager) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.sendHeartbeat(); err != nil {
				log.Printf("Warning: heartbeat failed: %v", err)
			}
		}
	}
}

// sendHeartbeat sends a heartbeat to refresh the route TTL.
func (m *Manager) sendHeartbeat() error {
	routeID := m.buildRouteID()
	url := m.apiURL("/routes/" + routeID + "/heartbeat")

	resp, err := m.client.Post(url, "application/json", http.NoBody)
	if err != nil {
		return fmt.Errorf("heartbeat request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var route RouteInfo
	if err := json.NewDecoder(resp.Body).Decode(&route); err != nil {
		return fmt.Errorf("failed to decode heartbeat response: %w", err)
	}

	if m.cfg.Verbose {
		log.Printf("Heartbeat sent, route expires at: %s", route.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

// unregisterRoute removes the route from webport.
func (m *Manager) unregisterRoute() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	routeID := m.buildRouteID()
	url := m.apiURL("/routes/" + routeID)

	req, err := http.NewRequest(http.MethodDelete, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("unregister request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	log.Printf("Route unregistered: %s", routeID)
	return nil
}

// buildRouteID builds the route ID from project and branch.
// Format: project:branch with slashes replaced by dashes.
func (m *Manager) buildRouteID() string {
	branchSlug := strings.ReplaceAll(m.cfg.Branch, "/", "-")
	return fmt.Sprintf("%s:%s", m.cfg.Project, branchSlug)
}

// apiURL builds the full API URL for a given path.
func (m *Manager) apiURL(path string) string {
	base := strings.TrimPrefix(m.cfg.APIAddr, "http://")
	base = strings.TrimPrefix(base, "https://")
	if !strings.Contains(base, ":") {
		base = base + ":80" // default port if none specified
	}
	return "http://" + base + path
}

// jsonReader creates a reader for JSON encoding.
func jsonReader(v any) *strings.Reader {
	b, _ := json.Marshal(v)
	return strings.NewReader(string(b))
}

// ConfigResponse represents the server configuration response.
type ConfigResponse struct {
	BaseDomain     string `json:"base_domain"`
	DefaultTTLSecs int    `json:"default_ttl_seconds"`
}

// GetConfig fetches the server configuration.
func (m *Manager) GetConfig() (*ConfigResponse, error) {
	url := m.apiURL("/config")

	resp, err := m.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("config request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var cfg ConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config response: %w", err)
	}

	return &cfg, nil
}
