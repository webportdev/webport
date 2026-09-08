package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPClientReadsConfigAndRegistersLease(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/config" {
			_ = json.NewEncoder(w).Encode(Config{BaseDomain: "dev.example.test", DefaultTTL: 300, TLSMode: "local-ca"})
			return
		}
		if r.URL.Path == "/v1/leases" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(leaseResponse{LeaseID: "lease-1", Route: Route{Project: "app", Branch: "main", Port: 3000, Domain: "app-main.dev.example.test"}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, server.Client(), bytesReader(1))
	if err != nil {
		t.Fatal(err)
	}
	gotConfig, err := client.Config(context.Background())
	if err != nil || gotConfig.BaseDomain != "dev.example.test" {
		t.Fatalf("Config() = %+v, %v", gotConfig, err)
	}
	lease, err := client.Register(context.Background(), RouteRequest{Project: "app", Branch: "main", Port: 3000, TTL: time.Minute})
	if err != nil || lease.ID != "lease-1" {
		t.Fatalf("Register() = %+v, %v", lease, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requests.Load())
	}
}

func TestHTTPClientFallsBackToLegacyRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/leases" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/routes" {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Route{Project: "app", Branch: "main", Port: 3000, Domain: "app-main.dev.example.test"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client, err := NewHTTPClient(server.URL, server.Client(), bytesReader(2))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := client.Register(context.Background(), RouteRequest{Project: "app", Branch: "main", Port: 3000, TTL: time.Minute})
	if err != nil || lease.ID != "" || lease.Route.Port != 3000 {
		t.Fatalf("legacy Register() = %+v, %v", lease, err)
	}
}

type fakeClient struct {
	registers  int
	heartbeats int
	releases   int
}

func (f *fakeClient) Config(context.Context) (Config, error) { return Config{BaseDomain: "test"}, nil }
func (f *fakeClient) Register(context.Context, RouteRequest) (Lease, error) {
	f.registers++
	return Lease{ID: "lease", Route: Route{Port: 3000}}, nil
}
func (f *fakeClient) Heartbeat(context.Context, string, time.Duration) (Lease, error) {
	f.heartbeats++
	if f.heartbeats == 1 {
		return Lease{}, ErrNotFound
	}
	return Lease{ID: "lease-recovered", Route: Route{Port: 3000}}, nil
}
func (f *fakeClient) Release(context.Context, string) error { f.releases++; return nil }

func TestLeaseManagerRecoversAfterHeartbeatNotFound(t *testing.T) {
	client := &fakeClient{}
	manager, err := NewLeaseManager(client, RouteRequest{Project: "app", Branch: "main", Port: 3000, TTL: time.Second}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.registers != 2 || client.heartbeats != 1 {
		t.Fatalf("recovery calls = registers %d, heartbeats %d", client.registers, client.heartbeats)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.releases != 1 {
		t.Fatalf("release calls = %d, want 1", client.releases)
	}
}

func TestLeaseManagerRequiresHeartbeatIntervalShorterThanTTL(t *testing.T) {
	_, err := NewLeaseManager(&fakeClient{}, RouteRequest{Project: "app", Branch: "main", Port: 3000, TTL: time.Second}, time.Second)
	if err == nil {
		t.Fatal("NewLeaseManager() error = nil")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("unexpected unavailable error: %v", err)
	}
}

type byteReader byte

func (r byteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func bytesReader(value byte) byteReader { return byteReader(value) }
