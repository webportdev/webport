package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/socket"
	"github.com/webportdev/webport/internal/tui"
)

const routeRetryInterval = 10 * time.Second

// Routes publishes the dashboard's daemon-owned hostname.
type Routes interface {
	SetSystemRoute(req route.RegisterRequest, baseDomain string, now time.Time) (route.Route, error)
	DeleteSystemRoute(id route.RouteID, baseDomain string, now time.Time) error
}

// Report is the web UI state returned to "webport webui status".
type Report struct {
	Enabled      bool   `json:"enabled"`
	PasswordSet  bool   `json:"password_set"`
	Subdomain    string `json:"subdomain"`
	Running      bool   `json:"running"`
	URL          string `json:"url,omitempty"`
	Listen       string `json:"listen,omitempty"`
	RouteError   string `json:"route_error,omitempty"`
	SettingsPath string `json:"settings_path"`
}

// Update changes settings; nil fields are left unchanged and an empty
// subdomain restores the default.
type Update struct {
	Enabled      *bool   `json:"enabled,omitempty"`
	Subdomain    *string `json:"subdomain,omitempty"`
	PasswordHash *string `json:"password_hash,omitempty"`
}

// ManagerOptions configures NewManager.
type ManagerOptions struct {
	SettingsPath string
	// Listen is the loopback address Traefik forwards dashboard requests to.
	Listen     string
	BaseDomain string
	Routes     Routes
	Backend    tui.Backend
	ListLive   func() ([]state.LiveState, error)
	Assets     fs.FS
}

// Manager runs the dashboard according to its persisted settings.
type Manager struct {
	options ManagerOptions
	stop    chan struct{}
	done    chan struct{}

	mu         sync.Mutex
	settings   Settings
	server     *http.Server
	listen     string
	port       int
	url        string
	routeError string
	stopped    bool
}

// NewManager loads settings. Call Start to apply them.
func NewManager(options ManagerOptions) (*Manager, error) {
	settings, err := LoadSettings(options.SettingsPath)
	if err != nil {
		return nil, err
	}
	return &Manager{options: options, settings: settings, stop: make(chan struct{}), done: make(chan struct{})}, nil
}

// Start applies the settings and retries a conflicting hostname periodically.
func (m *Manager) Start() {
	m.mu.Lock()
	m.applyLocked()
	m.mu.Unlock()
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(routeRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.mu.Lock()
				if m.routeError != "" {
					m.applyLocked()
				}
				m.mu.Unlock()
			}
		}
	}()
}

// Stop shuts the dashboard down. Daemon shutdown clears routes separately.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	close(m.stop)
	server := m.server
	m.server = nil
	m.mu.Unlock()
	<-m.done
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

func (m *Manager) applyLocked() {
	if m.stopped {
		return
	}
	id := route.RouteID{Project: RouteProject, Branch: RouteBranch}
	if !m.settings.Enabled || !m.settings.HasPassword() {
		if err := m.options.Routes.DeleteSystemRoute(id, m.options.BaseDomain, time.Now()); err != nil {
			log.Printf("WARN: remove web UI route: %v", err)
		}
		if m.server != nil {
			server := m.server
			m.server = nil
			go func() { _ = server.Close() }()
		}
		m.listen, m.port, m.url, m.routeError = "", 0, "", ""
		return
	}
	if m.server == nil {
		listener, err := net.Listen("tcp", m.options.Listen)
		if err != nil {
			m.routeError = fmt.Sprintf("listen on %s: %v", m.options.Listen, err)
			return
		}
		handler := NewServer(ServerOptions{
			Backend: m.options.Backend, ListLive: m.options.ListLive, Assets: m.options.Assets,
			Settings: m.currentSettings, Status: m.dashboardStatus,
		})
		m.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
		m.listen, m.port = listener.Addr().String(), listener.Addr().(*net.TCPAddr).Port
		server := m.server
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("WARN: web UI server stopped: %v", err)
			}
		}()
		log.Printf("Web UI listening on %s", m.listen)
	}
	published, err := m.options.Routes.SetSystemRoute(route.RegisterRequest{
		Project: RouteProject, Branch: RouteBranch, Port: m.port, Subdomain: m.settings.EffectiveSubdomain(),
	}, m.options.BaseDomain, time.Now())
	if err != nil {
		if m.routeError != err.Error() {
			log.Printf("WARN: publish web UI route: %v", err)
		}
		m.url, m.routeError = "", err.Error()
		return
	}
	if m.url != "https://"+published.Domain {
		log.Printf("Web UI published at https://%s", published.Domain)
	}
	m.url, m.routeError = "https://"+published.Domain, ""
}

func (m *Manager) currentSettings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

func (m *Manager) dashboardStatus() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{URL: m.url, Subdomain: m.settings.EffectiveSubdomain(), Listen: m.listen, RouteError: m.routeError}
}

// Report returns the current state.
func (m *Manager) Report() Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reportLocked()
}

func (m *Manager) reportLocked() Report {
	return Report{
		Enabled: m.settings.Enabled, PasswordSet: m.settings.HasPassword(),
		Subdomain: m.settings.EffectiveSubdomain(), Running: m.server != nil,
		URL: m.url, Listen: m.listen, RouteError: m.routeError, SettingsPath: m.options.SettingsPath,
	}
}

// Apply persists an update and applies it immediately.
func (m *Manager) Apply(update Update) (Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.settings
	if update.PasswordHash != nil {
		if _, err := parseHash(*update.PasswordHash); err != nil {
			return Report{}, err
		}
		next.PasswordHash = *update.PasswordHash
	}
	if update.Subdomain != nil {
		value := *update.Subdomain
		if value == DefaultSubdomain {
			value = ""
		}
		if value != "" {
			if err := ValidateSubdomain(value); err != nil {
				return Report{}, err
			}
		}
		next.Subdomain = value
	}
	if update.Enabled != nil {
		next.Enabled = *update.Enabled
	}
	if err := SaveSettings(m.options.SettingsPath, next); err != nil {
		return Report{}, err
	}
	m.settings = next
	m.applyLocked()
	return m.reportLocked(), nil
}

// SocketHandlers are the daemon socket operations behind "webport webui".
func (m *Manager) SocketHandlers() map[string]socket.Handler {
	return map[string]socket.Handler{
		"webui.status": func(context.Context, json.RawMessage) (any, error) { return m.Report(), nil },
		"webui.update": func(_ context.Context, payload json.RawMessage) (any, error) {
			var update Update
			if err := json.Unmarshal(payload, &update); err != nil {
				return nil, fmt.Errorf("invalid web UI update: %w", err)
			}
			return m.Apply(update)
		},
	}
}
