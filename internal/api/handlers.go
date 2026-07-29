// Package api provides HTTP REST API handlers for webport.
//
// API Endpoints:
//   - POST   /routes                           Register/update a route
//   - GET    /routes                           List all active routes
//   - DELETE /routes/{project}:{branch}            Remove a route
//   - POST   /routes/{project}:{branch}/heartbeat  Refresh route TTL
//   - GET    /config                           Query server configuration
//   - GET    /health                           Health check
//
// Route IDs use the format "{project}:{branch}". Clients must URL-escape the
// route ID path segment when branch names contain slashes.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/webportdev/webport/internal/config"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/traefik"
)

const maxRequestBodyBytes = 1 << 20

// Handlers manages HTTP endpoints
type Handlers struct {
	store   *route.Store
	control *route.Controller
	writer  traefik.Writer
	cfg     *config.Config
	publish func() error
	started time.Time
}

// NewHandlers creates new handlers
func NewHandlers(store *route.Store, writer traefik.Writer, cfg *config.Config) *Handlers {
	h := &Handlers{
		store:   store,
		writer:  writer,
		cfg:     cfg,
		started: time.Now(),
	}
	h.control = route.NewController(store, h.publishSnapshot)
	return h
}

// WithController uses the daemon's shared route controller.
func (h *Handlers) WithController(control *route.Controller) *Handlers {
	h.control = control
	return h
}

// WithPublisher uses publish for configuration updates. This allows all route
// producers to serialize snapshots through one publisher.
func (h *Handlers) WithPublisher(publish func() error) *Handlers {
	h.publish = publish
	h.control.SetPublisher(publish)
	return h
}

// RegisterRoutes registers all HTTP routes
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/routes", h.handleRoutes)
	mux.HandleFunc("/routes/", h.handleRouteByID)
	mux.HandleFunc("/config", h.handleConfig)
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/ready", h.handleReady)
	mux.HandleFunc("/status", h.handleStatus)
	mux.HandleFunc("/v1/leases", h.handleLeases)
	mux.HandleFunc("/v1/leases/", h.handleLeaseByID)
}

// handleRoutes handles POST /routes and GET /routes
func (h *Handlers) handleRoutes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.registerRoute(w, r)
	case http.MethodGet:
		h.listRoutes(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRouteByID handles DELETE /routes/{id} and POST /routes/{id}/heartbeat
func (h *Handlers) handleRouteByID(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.EscapedPath())
	if r.Method == http.MethodDelete && len(parts) == 2 {
		h.deleteRoute(w, r)
	} else if r.Method == http.MethodPost && h.isHeartbeat(r.URL.EscapedPath()) {
		h.heartbeat(w, r)
	} else {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// registerRoute handles POST /routes
func (h *Handlers) registerRoute(w http.ResponseWriter, r *http.Request) {
	var req route.RegisterRequest
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ttl := h.cfg.DefaultTTL
	if req.TTL > 0 {
		ttl = time.Duration(req.TTL) * time.Second
	}

	newRoute, err := h.control.RegisterLegacy(req, h.cfg.BaseDomain, time.Now(), ttl)
	if err != nil {
		h.writeControlError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(newRoute); err != nil {
		log.Printf("ERROR: failed to encode response: %v", err)
	}
}

// listRoutes handles GET /routes
func (h *Handlers) listRoutes(w http.ResponseWriter, r *http.Request) {
	routes := h.store.List()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(route.RoutesList{
		Routes: routes,
		Total:  len(routes),
	}); err != nil {
		log.Printf("ERROR: failed to encode response: %v", err)
	}
}

// deleteRoute handles DELETE /routes/{project}:{branch}
func (h *Handlers) deleteRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := h.parseRouteID(r.URL.EscapedPath())
	if !ok {
		http.Error(w, "invalid route ID", http.StatusBadRequest)
		return
	}

	if err := h.control.DeleteLegacy(id, time.Now(), h.cfg.BaseDomain); err != nil {
		h.writeControlError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// heartbeat handles POST /routes/{project}:{branch}/heartbeat
func (h *Handlers) heartbeat(w http.ResponseWriter, r *http.Request) {
	id, ok := h.parseRouteID(r.URL.EscapedPath())
	if !ok {
		http.Error(w, "invalid route ID", http.StatusBadRequest)
		return
	}

	// Parse optional TTL override
	var req route.HeartbeatRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		if !errors.Is(err, io.EOF) {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var ttl time.Duration
	if req.TTL > 0 {
		ttl = time.Duration(req.TTL) * time.Second
	}
	existingRoute, err := h.control.HeartbeatLegacy(id, ttl, h.cfg.DefaultTTL, time.Now(), h.cfg.BaseDomain)
	if err != nil {
		h.writeControlError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(existingRoute); err != nil {
		log.Printf("ERROR: failed to encode response: %v", err)
	}
}

// handleHealth handles GET /health
func (h *Handlers) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (h *Handlers) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.control.Ready() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

type StatusResponse struct {
	Version         string    `json:"version"`
	UptimeSeconds   int64     `json:"uptime_seconds"`
	TLSMode         string    `json:"tls_mode"`
	RouteCount      int       `json:"route_count"`
	LeaseCount      int       `json:"lease_count"`
	Revision        uint64    `json:"revision"`
	AppliedRevision uint64    `json:"applied_revision"`
	LastPublished   time.Time `json:"last_published,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	Ready           bool      `json:"ready"`
}

func (h *Handlers) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status := h.control.Status()
	resp := StatusResponse{
		Version: buildVersion(), UptimeSeconds: int64(time.Since(h.started).Seconds()),
		TLSMode: h.cfg.TLSMode, RouteCount: h.store.Count(), LeaseCount: status.LeaseCount,
		Revision: status.Revision, AppliedRevision: status.AppliedRevision,
		LastPublished: status.LastPublished, LastError: status.LastError,
		Ready: h.control.Ready(),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("ERROR: failed to encode status response: %v", err)
	}
}

type leaseRequest struct {
	ClientID string `json:"client_id"`
	Project  string `json:"project"`
	Branch   string `json:"branch"`
	Port     int    `json:"port"`
	TTL      int    `json:"ttl"`
}

type leaseResponse struct {
	LeaseID string      `json:"lease_id"`
	Route   route.Route `json:"route"`
}

func (h *Handlers) handleLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req leaseRequest
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ClientID == "" || len(req.ClientID) > 128 {
		http.Error(w, "client_id must contain between 1 and 128 characters", http.StatusBadRequest)
		return
	}
	register := route.RegisterRequest{Project: req.Project, Branch: req.Branch, Port: req.Port, TTL: req.TTL}
	if err := register.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ttl := h.cfg.DefaultTTL
	if req.TTL > 0 {
		ttl = time.Duration(req.TTL) * time.Second
	}
	claim, registered, created, err := h.control.CreateLease(req.ClientID, register, h.cfg.BaseDomain, time.Now(), ttl)
	if err != nil {
		h.writeControlError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	if err := json.NewEncoder(w).Encode(leaseResponse{LeaseID: claim.LeaseID, Route: registered}); err != nil {
		log.Printf("ERROR: failed to encode lease response: %v", err)
	}
}

func (h *Handlers) handleLeaseByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/v1/leases/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "invalid lease ID", http.StatusBadRequest)
		return
	}
	leaseID, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid lease ID", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "heartbeat":
		var req route.HeartbeatRequest
		if err := decodeOptionalJSON(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := req.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var ttl time.Duration
		if req.TTL > 0 {
			ttl = time.Duration(req.TTL) * time.Second
		}
		claim, registered, err := h.control.HeartbeatLease(leaseID, ttl, time.Now(), h.cfg.BaseDomain)
		if err != nil {
			h.writeControlError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(leaseResponse{LeaseID: claim.LeaseID, Route: registered})
	case r.Method == http.MethodDelete && len(parts) == 1:
		if err := h.control.DeleteLease(leaseID, time.Now(), h.cfg.BaseDomain); err != nil {
			h.writeControlError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handlers) writeControlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, route.ErrNotFound):
		http.Error(w, "route not found", http.StatusNotFound)
	case errors.Is(err, route.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		log.Printf("ERROR: route update failed: %v", err)
		http.Error(w, "failed to apply route configuration", http.StatusServiceUnavailable)
	}
}

// ConfigResponse represents the server configuration response
type ConfigResponse struct {
	BaseDomain string `json:"base_domain"`
	DefaultTTL int    `json:"default_ttl_seconds"`
	TLSMode    string `json:"tls_mode"`
	CACertPath string `json:"ca_cert_path,omitempty"`
}

// handleConfig handles GET /config
func (h *Handlers) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := ConfigResponse{
		BaseDomain: h.cfg.BaseDomain,
		DefaultTTL: int(h.cfg.DefaultTTL.Seconds()),
		TLSMode:    h.cfg.TLSMode,
	}
	if h.cfg.TLSMode == config.TLSModeLocalCA {
		resp.CACertPath = filepath.Join(h.cfg.LocalCADir, "ca.crt")
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("ERROR: failed to encode config response: %v", err)
	}
}

// publishTraefikConfig regenerates the file-provider configuration.
func (h *Handlers) publishSnapshot() error {
	if h.publish != nil {
		return h.publish()
	}
	routes := h.store.List()
	routeInfos := traefik.RoutesToRouteInfos(routes)
	content, err := traefik.GenerateDynamicConfig(routeInfos, traefik.Config{
		EntryPoint:   h.cfg.TraefikEntryPoint,
		CertResolver: h.cfg.TraefikCertResolver,
	}, h.cfg.BaseDomain)
	if err != nil {
		return err
	}
	return h.writer.Write(content)
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// isHeartbeat checks if the path is a heartbeat request
func (h *Handlers) isHeartbeat(path string) bool {
	parts := splitPath(path)
	return len(parts) == 3 && parts[2] == "heartbeat"
}

// parseRouteID extracts RouteID from URL path
// Expected: /routes/{project}:{branch} or /routes/{project}:{branch}/heartbeat.
// The route ID path segment must be URL-escaped if the branch contains slashes.
func (h *Handlers) parseRouteID(path string) (route.RouteID, bool) {
	parts := splitPath(path)
	if len(parts) < 2 {
		return route.RouteID{}, false
	}

	idStr, err := url.PathUnescape(parts[1])
	if err != nil {
		return route.RouteID{}, false
	}
	id, err := route.RouteIDFromString(idStr)
	if err != nil {
		return route.RouteID{}, false
	}
	return id, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(dst)
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	if err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

// splitPath splits a URL path into components
func splitPath(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}
