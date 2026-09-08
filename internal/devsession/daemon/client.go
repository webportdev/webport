// Package daemon provides the session-facing Webport daemon client.
package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound         = errors.New("daemon resource not found")
	ErrMethodNotAllowed = errors.New("daemon method not allowed")
	ErrUnavailable      = errors.New("daemon unavailable")
)

type Config struct {
	BaseDomain string `json:"base_domain"`
	DefaultTTL int    `json:"default_ttl_seconds"`
	TLSMode    string `json:"tls_mode"`
	CACertPath string `json:"ca_cert_path,omitempty"`
}

type RouteRequest struct {
	Project  string
	Branch   string
	Port     int
	TTL      time.Duration
	ClientID string
}

type Route struct {
	Project   string    `json:"project"`
	Branch    string    `json:"branch"`
	Port      int       `json:"port"`
	Domain    string    `json:"domain"`
	Host      string    `json:"host,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Lease struct {
	ID    string
	Route Route
}

type Client interface {
	Config(context.Context) (Config, error)
	Register(context.Context, RouteRequest) (Lease, error)
	Heartbeat(context.Context, string, time.Duration) (Lease, error)
	Release(context.Context, string) error
}

type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
	clientID   string
	random     io.Reader
}

func NewHTTPClient(baseURL string, httpClient *http.Client, randomSource io.Reader) (*HTTPClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("daemon API URL is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if randomSource == nil {
		randomSource = rand.Reader
	}
	var idBytes [18]byte
	if _, err := io.ReadFull(randomSource, idBytes[:]); err != nil {
		return nil, fmt.Errorf("generate daemon client ID: %w", err)
	}
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient,
		clientID: base64.RawURLEncoding.EncodeToString(idBytes[:]), random: randomSource,
	}, nil
}

func (c *HTTPClient) Config(ctx context.Context) (Config, error) {
	var result Config
	if err := c.doJSON(ctx, http.MethodGet, "/config", nil, &result, http.StatusOK); err != nil {
		return Config{}, fmt.Errorf("query daemon config: %w", err)
	}
	if result.BaseDomain == "" {
		return Config{}, errors.New("query daemon config: response has no base domain")
	}
	return result, nil
}

func (c *HTTPClient) Register(ctx context.Context, route RouteRequest) (Lease, error) {
	clientID := route.ClientID
	if clientID == "" {
		clientID = c.clientID
	}
	request := struct {
		ClientID string `json:"client_id"`
		Project  string `json:"project"`
		Branch   string `json:"branch"`
		Port     int    `json:"port"`
		TTL      int    `json:"ttl"`
	}{clientID, route.Project, route.Branch, route.Port, int(route.TTL.Seconds())}
	var response leaseResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/leases", request, &response, http.StatusOK, http.StatusCreated)
	if err == nil {
		return Lease{ID: response.LeaseID, Route: response.Route}, nil
	}
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrMethodNotAllowed) {
		return Lease{}, fmt.Errorf("register lease: %w", err)
	}

	legacy := struct {
		Project string `json:"project"`
		Branch  string `json:"branch"`
		Port    int    `json:"port"`
		TTL     int    `json:"ttl"`
	}{route.Project, route.Branch, route.Port, int(route.TTL.Seconds())}
	var legacyRoute Route
	if err := c.doJSON(ctx, http.MethodPost, "/routes", legacy, &legacyRoute, http.StatusCreated); err != nil {
		return Lease{}, fmt.Errorf("register route: %w", err)
	}
	return Lease{Route: legacyRoute}, nil
}

func (c *HTTPClient) Heartbeat(ctx context.Context, leaseID string, ttl time.Duration) (Lease, error) {
	body := struct {
		TTL int `json:"ttl"`
	}{int(ttl.Seconds())}
	if leaseID == "" {
		return Lease{}, errors.New("heartbeat lease ID is required")
	}
	var response leaseResponse
	endpoint := "/v1/leases/" + url.PathEscape(leaseID) + "/heartbeat"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, body, &response, http.StatusOK); err != nil {
		return Lease{}, fmt.Errorf("heartbeat lease: %w", err)
	}
	return Lease{ID: response.LeaseID, Route: response.Route}, nil
}

func (c *HTTPClient) Release(ctx context.Context, leaseID string) error {
	if leaseID == "" {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint("/v1/leases/"+url.PathEscape(leaseID)), http.NoBody)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotFound {
		return c.statusError(response)
	}
	return nil
}

type LeaseManager struct {
	client   Client
	request  RouteRequest
	interval time.Duration
	mu       struct {
		// The manager's lease is replaced atomically after recovery.
		sync.Mutex
		lease Lease
		ok    bool
	}
}

func NewLeaseManager(client Client, request RouteRequest, interval time.Duration) (*LeaseManager, error) {
	if client == nil {
		return nil, errors.New("lease client is required")
	}
	if request.Project == "" || request.Branch == "" || request.Port < 1 || request.Port > 65535 {
		return nil, errors.New("invalid route request")
	}
	if request.TTL <= 0 {
		return nil, errors.New("route TTL must be positive")
	}
	if interval <= 0 || interval >= request.TTL {
		return nil, errors.New("heartbeat interval must be positive and shorter than TTL")
	}
	if request.ClientID == "" {
		clientID, err := randomID()
		if err != nil {
			return nil, err
		}
		request.ClientID = clientID
	}
	return &LeaseManager{client: client, request: request, interval: interval}, nil
}

func (m *LeaseManager) Start(ctx context.Context) error {
	lease, err := m.client.Register(ctx, m.request)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.mu.lease, m.mu.ok = lease, true
	m.mu.Unlock()
	return nil
}

func (m *LeaseManager) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := m.heartbeat(ctx); err != nil {
				return err
			}
		}
	}
}

func (m *LeaseManager) heartbeat(ctx context.Context) error {
	m.mu.Lock()
	if !m.mu.ok {
		m.mu.Unlock()
		return errors.New("lease has not been started")
	}
	leaseID := m.mu.lease.ID
	m.mu.Unlock()
	lease, err := m.client.Heartbeat(ctx, leaseID, m.request.TTL)
	if err != nil {
		if errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), ErrNotFound.Error()) {
			lease, err = m.client.Register(ctx, m.request)
		}
		if err != nil {
			return err
		}
	}
	m.mu.Lock()
	if !m.mu.ok {
		m.mu.Unlock()
		_ = m.client.Release(context.Background(), lease.ID)
		return context.Canceled
	}
	m.mu.lease = lease
	m.mu.Unlock()
	return nil
}

func (m *LeaseManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.mu.ok {
		m.mu.Unlock()
		return nil
	}
	leaseID := m.mu.lease.ID
	m.mu.ok = false
	m.mu.Unlock()
	return m.client.Release(ctx, leaseID)
}

func (m *LeaseManager) Route() (Route, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.mu.ok {
		return Route{}, false
	}
	return m.mu.lease.Route, true
}

type leaseResponse struct {
	LeaseID string `json:"lease_id"`
	Route   Route  `json:"route"`
}

func (c *HTTPClient) doJSON(ctx context.Context, method, path string, value, output any, statuses ...int) error {
	var body io.Reader = http.NoBody
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return err
	}
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	for _, status := range statuses {
		if response.StatusCode == status {
			if output == nil {
				return nil
			}
			if err := json.NewDecoder(response.Body).Decode(output); err != nil {
				return fmt.Errorf("decode daemon response: %w", err)
			}
			return nil
		}
	}
	return c.statusError(response)
}

func (c *HTTPClient) statusError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	message := strings.TrimSpace(string(body))
	var sentinel error
	switch response.StatusCode {
	case http.StatusNotFound:
		sentinel = ErrNotFound
	case http.StatusMethodNotAllowed:
		sentinel = ErrMethodNotAllowed
	default:
		sentinel = fmt.Errorf("unexpected daemon status %d", response.StatusCode)
	}
	if message == "" {
		return sentinel
	}
	return fmt.Errorf("%w: %s", sentinel, message)
}

func (c *HTTPClient) endpoint(path string) string { return c.baseURL + path }

func randomID() (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate lease client ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}
