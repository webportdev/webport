// Package api provides HTTP REST API handlers for webport.
//
// API Endpoints:
//   - POST   /routes                           Register/update a route
//   - GET    /routes                           List all active routes
//   - DELETE /routes/{project}:{branch}        Remove a route
//   - POST   /routes/{project}:{branch}/heartbeat  Refresh route TTL
//   - GET    /config                           Query server configuration
//   - GET    /health                           Health check
//
// Route IDs in the URL use the format "{project}:{branch}" where colons
// avoid ambiguity with dashes in project or branch names.
package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/webportdev/webport/internal/caddy"
	"github.com/webportdev/webport/internal/config"
	"github.com/webportdev/webport/internal/route"
)

// Handlers manages HTTP endpoints
type Handlers struct {
	store  *route.Store
	writer caddy.Writer
	cfg    *config.Config
}

// NewHandlers creates new handlers
func NewHandlers(store *route.Store, writer caddy.Writer, cfg *config.Config) *Handlers {
	return &Handlers{
		store:  store,
		writer: writer,
		cfg:    cfg,
	}
}

// RegisterRoutes registers all HTTP routes
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/routes", h.handleRoutes)
	mux.HandleFunc("/routes/", h.handleRouteByID)
	mux.HandleFunc("/config", h.handleConfig)
	mux.HandleFunc("/health", h.handleHealth)
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
	if r.Method == http.MethodDelete {
		h.deleteRoute(w, r)
	} else if r.Method == http.MethodPost && h.isHeartbeat(r.URL.Path) {
		h.heartbeat(w, r)
	} else {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// registerRoute handles POST /routes
func (h *Handlers) registerRoute(w http.ResponseWriter, r *http.Request) {
	var req route.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	domain := caddy.BuildDomain(h.cfg.BaseDomain, req.Project, req.Branch)

	newRoute := route.Route{
		Project:   req.Project,
		Branch:    req.Branch,
		Port:      req.Port,
		Domain:    domain,
		ExpiresAt: time.Now().Add(ttl),
	}

	if err := h.store.Add(newRoute); err != nil {
		log.Printf("ERROR: failed to add route: %v", err)
		http.Error(w, "failed to register route", http.StatusInternalServerError)
		return
	}

	if err := h.reloadCaddy(); err != nil {
		log.Printf("WARN: failed to reload caddy: %v", err)
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

// deleteRoute handles DELETE /routes/{project}-{branch}
func (h *Handlers) deleteRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := h.parseRouteID(r.URL.Path)
	if !ok {
		http.Error(w, "invalid route ID", http.StatusBadRequest)
		return
	}

	if !h.store.Delete(id) {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}

	if err := h.reloadCaddy(); err != nil {
		log.Printf("WARN: failed to reload caddy: %v", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// heartbeat handles POST /routes/{project}-{branch}/heartbeat
func (h *Handlers) heartbeat(w http.ResponseWriter, r *http.Request) {
	id, ok := h.parseRouteID(r.URL.Path)
	if !ok {
		http.Error(w, "invalid route ID", http.StatusBadRequest)
		return
	}

	existingRoute, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}

	// Parse optional TTL override
	var req route.HeartbeatRequest

	// Read body to check if it's empty
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()

	if len(body) == 0 {
		// Empty body - use default TTL
	} else {
		// Try to decode as JSON
		if err := json.Unmarshal(body, &req); err != nil {
			// Body has content but it's not valid JSON
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}

	ttl := h.cfg.DefaultTTL
	if req.TTL > 0 {
		ttl = time.Duration(req.TTL) * time.Second
	}

	existingRoute.ExpiresAt = time.Now().Add(ttl)
	if err := h.store.Add(existingRoute); err != nil {
		log.Printf("ERROR: failed to update route: %v", err)
		http.Error(w, "failed to update route", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(existingRoute); err != nil {
		log.Printf("ERROR: failed to encode response: %v", err)
	}
}

// handleHealth handles GET /health
func (h *Handlers) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// ConfigResponse represents the server configuration response
type ConfigResponse struct {
	BaseDomain string `json:"base_domain"`
	DefaultTTL int    `json:"default_ttl_seconds"`
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
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("ERROR: failed to encode config response: %v", err)
	}
}

// reloadCaddy regenerates and reloads the Caddyfile
func (h *Handlers) reloadCaddy() error {
	routes := h.store.List()
	routeInfos := caddy.RoutesToRouteInfos(routes)

	tlsCfg := caddy.TLSConfig{
		DNSProvider:       h.cfg.TLS.DNSProvider,
		DNSProviderModule: h.cfg.TLS.DNSProviderModule,
		DNSTokenEnvVar:    h.cfg.TLS.DNSTokenEnvVar,
	}

	content, err := caddy.GenerateCaddyfile(routeInfos, tlsCfg, h.cfg.BaseDomain)
	if err != nil {
		return err
	}
	return h.writer.Write(content)
}

// isHeartbeat checks if the path is a heartbeat request
func (h *Handlers) isHeartbeat(path string) bool {
	parts := splitPath(path)
	return len(parts) >= 3 && parts[2] == "heartbeat"
}

// parseRouteID extracts RouteID from URL path
// Expected: /routes/{project}-{branch} or /routes/{project}-{branch}/heartbeat
func (h *Handlers) parseRouteID(path string) (route.RouteID, bool) {
	parts := splitPath(path)
	if len(parts) < 2 {
		return route.RouteID{}, false
	}

	idStr := parts[1]
	id, err := route.RouteIDFromString(idStr)
	if err != nil {
		return route.RouteID{}, false
	}
	return id, true
}

// splitPath splits a URL path into components
func splitPath(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}
