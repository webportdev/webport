// Package runtime manages the lifecycle of a webport route.
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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
	cfg      Config
	client   *http.Client
	route    *RouteInfo
	lease    string
	clientID string
	legacy   bool
	mu       sync.Mutex
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

type leaseRequest struct {
	ClientID string `json:"client_id"`
	Project  string `json:"project"`
	Branch   string `json:"branch"`
	Port     int    `json:"port"`
	TTL      int    `json:"ttl"`
}

type leaseResponse struct {
	LeaseID string    `json:"lease_id"`
	Route   RouteInfo `json:"route"`
}

type statusError struct {
	Code int
	Body string
}

func (e *statusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("unexpected status code: %d", e.Code)
	}
	return fmt.Sprintf("unexpected status code: %d: %s", e.Code, e.Body)
}

// NewManager creates a new Manager.
func NewManager(cfg Config) (*Manager, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	clientID, err := randomID()
	if err != nil {
		return nil, err
	}
	return &Manager{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		clientID: clientID,
	}, nil
}

func validateConfig(cfg Config) error {
	// Allow empty project/branch for config querying only
	if cfg.Port != 0 && (cfg.Port < 1 || cfg.Port > 65535) {
		return fmt.Errorf("port must be between 1-65535, got %d", cfg.Port)
	}
	if cfg.Port > 0 && cfg.TTL < time.Second {
		return fmt.Errorf("TTL must be at least 1 second")
	}
	if cfg.Port > 0 && cfg.Interval < time.Second {
		return fmt.Errorf("interval must be at least 1 second")
	}
	if cfg.Port > 0 && cfg.Interval >= cfg.TTL {
		return fmt.Errorf("interval must be shorter than TTL")
	}
	return nil
}

// Start registers the route and begins the heartbeat loop.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.register(); err != nil {
		return fmt.Errorf("failed to register route: %w", err)
	}

	// Start heartbeat in background
	go m.heartbeatLoop(ctx)

	return nil
}

// Stop unregisters the route.
func (m *Manager) Stop() error {
	m.mu.Lock()
	legacy := m.legacy
	lease := m.lease
	m.mu.Unlock()
	if legacy {
		return m.unregisterRoute()
	}
	if lease == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, m.apiURL("/v1/leases/"+url.PathEscape(lease)), http.NoBody)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return responseError(resp)
	}
	return nil
}

func (m *Manager) register() error {
	if err := m.registerLease(); err != nil {
		var status *statusError
		if errors.As(err, &status) && (status.Code == http.StatusNotFound || status.Code == http.StatusMethodNotAllowed) {
			m.mu.Lock()
			m.legacy = true
			m.mu.Unlock()
			return m.registerRoute()
		}
		return err
	}
	return nil
}

func (m *Manager) registerLease() error {
	req := leaseRequest{
		ClientID: m.clientID, Project: m.cfg.Project, Branch: m.cfg.Branch,
		Port: m.cfg.Port, TTL: int(m.cfg.TTL.Seconds()),
	}
	resp, err := m.client.Post(m.apiURL("/v1/leases"), "application/json", jsonReader(req))
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	var result leaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	m.mu.Lock()
	m.lease = result.LeaseID
	m.route = &result.Route
	m.legacy = false
	m.mu.Unlock()
	log.Printf("Route registered: https://%s -> localhost:%d", result.Route.Domain, result.Route.Port)
	return nil
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
		return responseError(resp)
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
				var status *statusError
				if errors.As(err, &status) && status.Code == http.StatusNotFound {
					if registerErr := m.register(); registerErr != nil {
						log.Printf("Warning: route recovery failed: %v", registerErr)
					} else {
						log.Printf("Route recovered after daemon restart")
					}
					continue
				}
				log.Printf("Warning: heartbeat failed: %v", err)
			}
		}
	}
}

// sendHeartbeat sends a heartbeat to refresh the route TTL.
func (m *Manager) sendHeartbeat() error {
	m.mu.Lock()
	legacy := m.legacy
	lease := m.lease
	m.mu.Unlock()

	heartbeat := struct {
		TTL int `json:"ttl"`
	}{TTL: int(m.cfg.TTL.Seconds())}
	if !legacy {
		if lease == "" {
			return &statusError{Code: http.StatusNotFound}
		}
		endpoint := "/v1/leases/" + url.PathEscape(lease) + "/heartbeat"
		resp, err := m.client.Post(m.apiURL(endpoint), "application/json", jsonReader(heartbeat))
		if err != nil {
			return fmt.Errorf("heartbeat request failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return responseError(resp)
		}
		var result leaseResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("failed to decode heartbeat response: %w", err)
		}
		m.mu.Lock()
		m.route = &result.Route
		m.mu.Unlock()
		if m.cfg.Verbose {
			log.Printf("Heartbeat sent, route expires at: %s", result.Route.ExpiresAt.Format(time.RFC3339))
		}
		return nil
	}

	routeID := m.buildEscapedRouteID()
	endpoint := m.apiURL("/routes/" + routeID + "/heartbeat")

	resp, err := m.client.Post(endpoint, "application/json", jsonReader(heartbeat))
	if err != nil {
		return fmt.Errorf("heartbeat request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
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

	routeID := m.buildEscapedRouteID()
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
		return responseError(resp)
	}

	log.Printf("Route unregistered: %s", routeID)
	return nil
}

// buildRouteID builds the route ID from project and branch.
// Format: project:branch.
func (m *Manager) buildRouteID() string {
	return fmt.Sprintf("%s:%s", m.cfg.Project, m.cfg.Branch)
}

func (m *Manager) buildEscapedRouteID() string {
	return url.PathEscape(m.buildRouteID())
}

// apiURL builds the full API URL for a given path.
func (m *Manager) apiURL(path string) string {
	base := m.cfg.APIAddr
	scheme := "http://"
	if strings.HasPrefix(base, "http://") {
		base = strings.TrimPrefix(base, "http://")
	} else if strings.HasPrefix(base, "https://") {
		base = strings.TrimPrefix(base, "https://")
		scheme = "https://"
	}
	if !strings.Contains(base, ":") {
		base = base + ":80" // default port if none specified
	}
	return scheme + base + path
}

// Route returns the latest route information received from the daemon.
func (m *Manager) Route() (RouteInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.route == nil {
		return RouteInfo{}, false
	}
	return *m.route, true
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &statusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}

func randomID() (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate client ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
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
	TLSMode        string `json:"tls_mode"`
	CACertPath     string `json:"ca_cert_path"`
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
