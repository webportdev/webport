package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildRouteID(t *testing.T) {
	manager := &Manager{cfg: Config{Project: "myapp", Branch: "feature/auth-oauth"}}

	if got := manager.buildRouteID(); got != "myapp:feature/auth-oauth" {
		t.Errorf("buildRouteID() = %v, want myapp:feature/auth-oauth", got)
	}
}

func TestManagerRecoversLeaseAfterHeartbeatNotFound(t *testing.T) {
	var registrations atomic.Int32
	var heartbeatTTL atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			registrations.Add(1)
			data, _ := json.Marshal(leaseResponse{
				LeaseID: "lease",
				Route: RouteInfo{
					Project: "app", Branch: "main", Port: 3000,
					Domain: "app-main.example.com", ExpiresAt: time.Now().Add(3 * time.Second),
				},
			})
			return testResponse(http.StatusCreated, string(data)), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/heartbeat"):
			data, _ := io.ReadAll(r.Body)
			var request struct {
				TTL int `json:"ttl"`
			}
			_ = json.Unmarshal(data, &request)
			heartbeatTTL.Store(int32(request.TTL))
			return testResponse(http.StatusNotFound, "missing"), nil
		default:
			return testResponse(http.StatusNotFound, "missing"), nil
		}
	})

	manager, err := NewManager(Config{
		Project: "app", Branch: "main", Port: 3000,
		TTL: 3 * time.Second, Interval: time.Second, APIAddr: "http://webport.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.client = &http.Client{Transport: transport}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for registrations.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if registrations.Load() < 2 {
		t.Fatal("manager did not re-register after heartbeat 404")
	}
	if heartbeatTTL.Load() != 3 {
		t.Fatalf("heartbeat TTL = %d, want 3", heartbeatTTL.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestBuildEscapedRouteID(t *testing.T) {
	manager := &Manager{cfg: Config{Project: "myapp", Branch: "feature/auth-oauth"}}

	if got := manager.buildEscapedRouteID(); got != "myapp:feature%2Fauth-oauth" {
		t.Errorf("buildEscapedRouteID() = %v, want myapp:feature%%2Fauth-oauth", got)
	}
}
