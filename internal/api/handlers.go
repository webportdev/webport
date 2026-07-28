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
	"strings"
	"time"

	"github.com/webportdev/webport/internal/config"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/traefik"
)

const maxRequestBodyBytes = 1 << 20

// Handlers manages HTTP endpoints
type Handlers struct {
	store  *route.Store
	writer traefik.Writer
	cfg    *config.Config
}

// NewHandlers creates new handlers
func NewHandlers(store *route.Store, writer traefik.Writer, cfg *config.Config) *Handlers {
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

	domain := route.BuildDomain(h.cfg.BaseDomain, req.Project, req.Branch)

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

	if err := h.publishTraefikConfig(); err != nil {
		log.Printf("WARN: failed to publish Traefik configuration: %v", err)
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

	if !h.store.Delete(id) {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}

	if err := h.publishTraefikConfig(); err != nil {
		log.Printf("WARN: failed to publish Traefik configuration: %v", err)
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

	existingRoute, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "route not found", http.StatusNotFound)
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

// publishTraefikConfig regenerates the file-provider configuration.
func (h *Handlers) publishTraefikConfig() error {
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

// isHeartbeat checks if the path is a heartbeat request
func (h *Handlers) isHeartbeat(path string) bool {
	parts := splitPath(path)
	return len(parts) >= 3 && parts[2] == "heartbeat"
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
	return json.NewDecoder(r.Body).Decode(dst)
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()
	err := json.NewDecoder(r.Body).Decode(dst)
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	return err
}

// splitPath splits a URL path into components
func splitPath(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}
