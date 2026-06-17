package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/caddy"
	"github.com/webportdev/webport/internal/config"
	"github.com/webportdev/webport/internal/route"
)

// Ensure mockWriter implements the caddy.Writer interface
var _ caddy.Writer = (*mockWriter)(nil)

// mockWriter is a mock Writer that doesn't actually execute commands
type mockWriter struct {
	content string
	path    string
}

func (m *mockWriter) Write(content string) error {
	m.content = content
	return nil
}

func (m *mockWriter) GetPath() string {
	return m.path
}

func newMockWriter() *mockWriter {
	return &mockWriter{path: "/tmp/test/Caddyfile"}
}

func newTestHandlers() *Handlers {
	cfg := &config.Config{
		BaseDomain:    "example.com",
		Port:          8080,
		DefaultTTL:    300 * time.Second,
		CaddyfilePath: "/tmp/test/Caddyfile",
	}
	writer := newMockWriter()
	store := route.NewStore()

	return NewHandlers(store, writer, cfg)
}

func TestRegisterRoute(t *testing.T) {
	h := newTestHandlers()

	reqBody := route.RegisterRequest{
		Project: "myapp",
		Branch:  "main",
		Port:    3000,
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.registerRoute(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusCreated)
	}

	var gotRoute route.Route
	if err := json.NewDecoder(resp.Body).Decode(&gotRoute); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if gotRoute.Project != "myapp" {
		t.Errorf("Project = %v, want myapp", gotRoute.Project)
	}
	if gotRoute.Branch != "main" {
		t.Errorf("Branch = %v, want main", gotRoute.Branch)
	}
	if gotRoute.Port != 3000 {
		t.Errorf("Port = %v, want 3000", gotRoute.Port)
	}
	if gotRoute.Domain != "myapp-main.example.com" {
		t.Errorf("Domain = %v, want myapp-main.example.com", gotRoute.Domain)
	}

	// Check that route was added to store
	id := route.RouteID{Project: "myapp", Branch: "main"}
	if _, ok := h.store.Get(id); !ok {
		t.Error("Route was not added to store")
	}
}

func TestRegisterRouteWithBranchSlash(t *testing.T) {
	h := newTestHandlers()

	reqBody := route.RegisterRequest{
		Project: "myapp",
		Branch:  "feature/auth",
		Port:    3000,
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.registerRoute(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	var gotRoute route.Route
	if err := json.NewDecoder(resp.Body).Decode(&gotRoute); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if gotRoute.Domain != "myapp-feature-auth.example.com" {
		t.Errorf("Domain = %v, want myapp-feature-auth.example.com", gotRoute.Domain)
	}
}

func TestRegisterRouteValidation(t *testing.T) {
	h := newTestHandlers()

	tests := []struct {
		name       string
		reqBody    route.RegisterRequest
		wantStatus int
	}{
		{
			name: "missing project",
			reqBody: route.RegisterRequest{
				Branch: "main",
				Port:   3000,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing branch",
			reqBody: route.RegisterRequest{
				Project: "myapp",
				Port:    3000,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid port",
			reqBody: route.RegisterRequest{
				Project: "myapp",
				Branch:  "main",
				Port:    70000,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "zero port",
			reqBody: route.RegisterRequest{
				Project: "myapp",
				Branch:  "main",
				Port:    0,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "project with space",
			reqBody: route.RegisterRequest{
				Project: "my app",
				Branch:  "main",
				Port:    3000,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "project with special char",
			reqBody: route.RegisterRequest{
				Project: "my$app",
				Branch:  "main",
				Port:    3000,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "branch with space",
			reqBody: route.RegisterRequest{
				Project: "myapp",
				Branch:  "my branch",
				Port:    3000,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "branch with invalid special char",
			reqBody: route.RegisterRequest{
				Project: "myapp",
				Branch:  "feature$auth",
				Port:    3000,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "valid project with dash",
			reqBody: route.RegisterRequest{
				Project: "my-app",
				Branch:  "main",
				Port:    3000,
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "valid branch with slash",
			reqBody: route.RegisterRequest{
				Project: "myapp",
				Branch:  "feature/auth",
				Port:    3000,
			},
			wantStatus: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.reqBody)
			req := httptest.NewRequest("POST", "/routes", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.registerRoute(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("Status = %v, want %v", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestRegisterRouteIdempotent(t *testing.T) {
	h := newTestHandlers()

	reqBody := route.RegisterRequest{
		Project: "myapp",
		Branch:  "main",
		Port:    3000,
	}

	// Register first time
	body, _ := json.Marshal(reqBody)
	req1 := httptest.NewRequest("POST", "/routes", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	h.registerRoute(w1, req1)

	// Register second time (should update)
	reqBody.Port = 4000
	body, _ = json.Marshal(reqBody)
	req2 := httptest.NewRequest("POST", "/routes", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.registerRoute(w2, req2)

	// Check that only one route exists
	if h.store.Count() != 1 {
		t.Errorf("Store count = %v, want 1", h.store.Count())
	}

	// Check that the port was updated
	id := route.RouteID{Project: "myapp", Branch: "main"}
	r, ok := h.store.Get(id)
	if !ok {
		t.Fatal("Route not found")
	}
	if r.Port != 4000 {
		t.Errorf("Port = %v, want 4000", r.Port)
	}
}

func TestListRoutes(t *testing.T) {
	h := newTestHandlers()

	// Add some routes
	h.store.Add(route.Route{
		Project:   "app1",
		Branch:    "main",
		Port:      3000,
		Domain:    "app1-main.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	h.store.Add(route.Route{
		Project:   "app2",
		Branch:    "dev",
		Port:      4000,
		Domain:    "app2-dev.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest("GET", "/routes", nil)
	w := httptest.NewRecorder()

	h.listRoutes(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusOK)
	}

	var list route.RoutesList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if list.Total != 2 {
		t.Errorf("Total = %v, want 2", list.Total)
	}
	if len(list.Routes) != 2 {
		t.Errorf("Routes length = %v, want 2", len(list.Routes))
	}
}

func TestDeleteRoute(t *testing.T) {
	h := newTestHandlers()

	// Add a route
	h.store.Add(route.Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      3000,
		Domain:    "myapp-main.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest("DELETE", "/routes/myapp:main", nil)
	w := httptest.NewRecorder()

	h.deleteRoute(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusNoContent)
	}

	// Check that route was deleted
	id := route.RouteID{Project: "myapp", Branch: "main"}
	if _, ok := h.store.Get(id); ok {
		t.Error("Route was not deleted from store")
	}
}

func TestDeleteRouteNotFound(t *testing.T) {
	h := newTestHandlers()

	req := httptest.NewRequest("DELETE", "/routes/myapp:main", nil)
	w := httptest.NewRecorder()

	h.deleteRoute(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHeartbeat(t *testing.T) {
	h := newTestHandlers()

	// Add a route that's about to expire
	h.store.Add(route.Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      3000,
		Domain:    "myapp-main.example.com",
		ExpiresAt: time.Now().Add(1 * time.Second),
	})

	req := httptest.NewRequest("POST", "/routes/myapp:main/heartbeat", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.heartbeat(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusOK)
	}

	// Check that the route was updated
	id := route.RouteID{Project: "myapp", Branch: "main"}
	r, ok := h.store.Get(id)
	if !ok {
		t.Fatal("Route not found")
	}

	// Expiry should be extended (default TTL is 300s)
	if time.Until(r.ExpiresAt) < 200*time.Second {
		t.Errorf("Expiry not extended: %v", r.ExpiresAt)
	}
}

func TestHeartbeatNotFound(t *testing.T) {
	h := newTestHandlers()

	req := httptest.NewRequest("POST", "/routes/myapp:main/heartbeat", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.heartbeat(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHeartbeatWithEmptyBody(t *testing.T) {
	h := newTestHandlers()

	// Add a route
	h.store.Add(route.Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      3000,
		Domain:    "myapp-main.example.com",
		ExpiresAt: time.Now().Add(1 * time.Second),
	})

	req := httptest.NewRequest("POST", "/routes/myapp:main/heartbeat", bytes.NewReader([]byte{}))
	w := httptest.NewRecorder()

	h.heartbeat(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusOK)
	}

	// Check that the route was updated with default TTL
	id := route.RouteID{Project: "myapp", Branch: "main"}
	r, ok := h.store.Get(id)
	if !ok {
		t.Fatal("Route not found")
	}

	// Expiry should be extended (default TTL is 300s)
	if time.Until(r.ExpiresAt) < 200*time.Second {
		t.Errorf("Expiry not extended: %v", r.ExpiresAt)
	}
}

func TestHeartbeatWithInvalidJSON(t *testing.T) {
	h := newTestHandlers()

	// Add a route
	h.store.Add(route.Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      3000,
		Domain:    "myapp-main.example.com",
		ExpiresAt: time.Now().Add(1 * time.Second),
	})

	req := httptest.NewRequest("POST", "/routes/myapp:main/heartbeat", bytes.NewReader([]byte("{invalid json")))
	w := httptest.NewRecorder()

	h.heartbeat(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHeartbeatWithCustomTTL(t *testing.T) {
	h := newTestHandlers()

	// Add a route
	h.store.Add(route.Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      3000,
		Domain:    "myapp-main.example.com",
		ExpiresAt: time.Now().Add(1 * time.Second),
	})

	customTTL := 600
	reqBody := fmt.Sprintf(`{"ttl": %d}`, customTTL)
	req := httptest.NewRequest("POST", "/routes/myapp:main/heartbeat", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.heartbeat(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusOK)
	}

	// Check that the route was updated with custom TTL
	id := route.RouteID{Project: "myapp", Branch: "main"}
	r, ok := h.store.Get(id)
	if !ok {
		t.Fatal("Route not found")
	}

	// Expiry should be extended to ~600 seconds
	if time.Until(r.ExpiresAt) < 500*time.Second {
		t.Errorf("Expiry not extended with custom TTL: %v", r.ExpiresAt)
	}
}

func TestHealth(t *testing.T) {
	h := newTestHandlers()

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	h.handleHealth(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusOK)
	}

	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if buf.String() != "OK" {
		t.Errorf("Body = %v, want OK", buf.String())
	}
}

func TestConfig(t *testing.T) {
	h := newTestHandlers()

	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()

	h.handleConfig(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusOK)
	}

	var got ConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if got.BaseDomain != "example.com" {
		t.Errorf("BaseDomain = %v, want example.com", got.BaseDomain)
	}
	if got.DefaultTTL != 300 {
		t.Errorf("DefaultTTL = %v, want 300", got.DefaultTTL)
	}
}

func TestConfigMethodNotAllowed(t *testing.T) {
	h := newTestHandlers()

	req := httptest.NewRequest("POST", "/config", nil)
	w := httptest.NewRecorder()

	h.handleConfig(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Status = %v, want %v", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestParseRouteID(t *testing.T) {
	h := newTestHandlers()

	tests := []struct {
		name   string
		path   string
		wantID route.RouteID
		wantOK bool
	}{
		{
			name:   "simple",
			path:   "/routes/myapp:main",
			wantID: route.RouteID{Project: "myapp", Branch: "main"},
			wantOK: true,
		},
		{
			name:   "with heartbeat",
			path:   "/routes/myapp:main/heartbeat",
			wantID: route.RouteID{Project: "myapp", Branch: "main"},
			wantOK: true,
		},
		{
			name:   "branch with dashes becomes slashes",
			path:   "/routes/myapp:feature-auth",
			wantID: route.RouteID{Project: "myapp", Branch: "feature/auth"},
			wantOK: true,
		},
		{
			name:   "project with dash",
			path:   "/routes/my-app:main",
			wantID: route.RouteID{Project: "my-app", Branch: "main"},
			wantOK: true,
		},
		{
			name:   "invalid - no id",
			path:   "/routes",
			wantOK: false,
		},
		{
			name:   "invalid - single word",
			path:   "/routes/myapp",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := h.parseRouteID(tt.path)
			if ok != tt.wantOK {
				t.Errorf("parseRouteID() ok = %v, want %v", ok, tt.wantOK)
				return
			}
			if ok && id != tt.wantID {
				t.Errorf("parseRouteID() = %v, want %v", id, tt.wantID)
			}
		})
	}
}

func TestIsHeartbeat(t *testing.T) {
	h := newTestHandlers()

	tests := []struct {
		path     string
		expected bool
	}{
		{"/routes/myapp:main/heartbeat", true},
		{"/routes/myapp:main", false},
		{"/routes", false},
		{"/health", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := h.isHeartbeat(tt.path); got != tt.expected {
				t.Errorf("isHeartbeat() = %v, want %v", got, tt.expected)
			}
		})
	}
}
