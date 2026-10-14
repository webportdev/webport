package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/tui"
)

func init() { hashIterations = 1000 }

type fakeBackend struct {
	mu        sync.Mutex
	instances []state.LiveState
	controls  []string
	sensitive []bool
}

func (b *fakeBackend) Snapshot(context.Context) tui.Snapshot {
	return tui.Snapshot{
		Instances: b.instances,
		Routes:    []route.Route{{Project: "app", Branch: "main", Port: 3000, Domain: "app-main.example.com"}},
		Daemon:    map[string]any{"ready": true},
	}
}

func (b *fakeBackend) Inspect(_ context.Context, live state.LiveState, sensitive, _ bool) (session.LiveInspection, error) {
	b.mu.Lock()
	b.sensitive = append(b.sensitive, sensitive)
	b.mu.Unlock()
	value := "<redacted>"
	if sensitive {
		value = "hunter2"
	}
	return session.LiveInspection{Project: "app", Environment: map[string]map[string]string{"web": {"SECRET": value, "PORT": "3000"}}}, nil
}

func (b *fakeBackend) Control(_ context.Context, live state.LiveState, operation string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.controls = append(b.controls, live.SessionID+":"+operation)
	return nil
}

func (b *fakeBackend) Logs(_ context.Context, _ state.LiveState, service string) ([]string, error) {
	return []string{"hello from " + service}, nil
}

type harness struct {
	t        *testing.T
	server   *Server
	backend  *fakeBackend
	settings Settings
	now      time.Time
	mu       sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	live := state.LiveState{
		SessionID: "session-1", Worktree: "/src/app", Profile: "default",
		ControlPath: "/run/user/1/webport/x.sock", ControlToken: "super-secret-token",
		Services: map[string]state.ServiceState{"web": {State: "running", Ready: true, PID: 42}},
		Routes:   map[string]state.RouteState{"web": {State: "active", Project: "app", Branch: "main", URL: "https://app-main.example.com"}},
	}
	h := &harness{t: t, backend: &fakeBackend{instances: []state.LiveState{live}}, settings: Settings{Enabled: true, PasswordHash: hash}, now: time.Unix(1_700_000_000, 0)}
	h.server = NewServer(ServerOptions{
		Backend:  h.backend,
		ListLive: func() ([]state.LiveState, error) { return h.backend.instances, nil },
		Settings: func() Settings { h.mu.Lock(); defer h.mu.Unlock(); return h.settings },
		Assets:   fstest.MapFS{"index.html": {Data: []byte("<!doctype html>app")}, "assets/app.js": {Data: []byte("console.log(1)")}},
		Now:      func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now },
	})
	return h
}

func (h *harness) do(method, path, body string, cookie *http.Cookie, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://webport.example.com"+path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:50000"
	if method != http.MethodGet {
		request.Header.Set(requestHeader, "1")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	for _, change := range mutate {
		change(request)
	}
	recorder := httptest.NewRecorder()
	h.server.ServeHTTP(recorder, request)
	return recorder
}

func (h *harness) login() *http.Cookie {
	h.t.Helper()
	response := h.do(http.MethodPost, "/api/login", `{"password":"correct horse"}`, nil)
	if response.Code != http.StatusOK {
		h.t.Fatalf("login = %d %s", response.Code, response.Body.String())
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookie {
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
				h.t.Fatalf("session cookie flags = %#v", cookie)
			}
			return cookie
		}
	}
	h.t.Fatal("login set no session cookie")
	return nil
}

func TestAPIRequiresAuthentication(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/snapshot", "/api/instances/session-1/inspect", "/api/instances/session-1/logs?service=web"} {
		if response := h.do(http.MethodGet, path, "", nil); response.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", path, response.Code)
		}
	}
	if response := h.do(http.MethodPost, "/api/instances/session-1/stop", "", nil); response.Code != http.StatusUnauthorized {
		t.Errorf("stop = %d, want 401", response.Code)
	}
	if len(h.backend.controls) != 0 {
		t.Fatal("unauthenticated control reached the backend")
	}
	response := h.do(http.MethodGet, "/api/auth", "", nil)
	if !strings.Contains(response.Body.String(), `"authenticated":false`) {
		t.Fatalf("auth = %s", response.Body.String())
	}
}

func TestLoginAndSnapshotOmitControlCredentials(t *testing.T) {
	h := newHarness(t)
	if response := h.do(http.MethodPost, "/api/login", `{"password":"wrong password"}`, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", response.Code)
	}
	cookie := h.login()
	response := h.do(http.MethodGet, "/api/snapshot", "", cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "super-secret-token") || strings.Contains(body, "x.sock") || strings.Contains(body, "control_") {
		t.Fatalf("snapshot leaked control credentials: %s", body)
	}
	var snapshot snapshotResponse
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Instances) != 1 || snapshot.Instances[0].Name != "app:main" || snapshot.Instances[0].URL != "https://app-main.example.com" {
		t.Fatalf("instances = %#v", snapshot.Instances)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("missing security headers: %v", response.Header())
	}
}

func TestPasswordChangeSignsOutAndLogout(t *testing.T) {
	h := newHarness(t)
	cookie := h.login()
	hash, _ := HashPassword("another password")
	h.mu.Lock()
	h.settings.PasswordHash = hash
	h.mu.Unlock()
	if response := h.do(http.MethodGet, "/api/snapshot", "", cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("snapshot after password change = %d, want 401", response.Code)
	}
	response := h.do(http.MethodPost, "/api/login", `{"password":"another password"}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("login with new password = %d", response.Code)
	}
	cookie = response.Result().Cookies()[0]
	h.do(http.MethodPost, "/api/logout", "", cookie)
	if response := h.do(http.MethodGet, "/api/snapshot", "", cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("snapshot after logout = %d, want 401", response.Code)
	}
}

func TestSessionsExpire(t *testing.T) {
	h := newHarness(t)
	cookie := h.login()
	h.mu.Lock()
	h.now = h.now.Add(sessionIdleLimit + time.Minute)
	h.mu.Unlock()
	if response := h.do(http.MethodGet, "/api/snapshot", "", cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("idle session = %d, want 401", response.Code)
	}
}

func TestCrossSiteWritesRejected(t *testing.T) {
	h := newHarness(t)
	cookie := h.login()
	cases := map[string]func(*http.Request){
		"missing header": func(r *http.Request) { r.Header.Del(requestHeader) },
		"foreign origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"cross site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	}
	for name, mutate := range cases {
		if response := h.do(http.MethodPost, "/api/instances/session-1/stop", "", cookie, mutate); response.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", name, response.Code)
		}
		if response := h.do(http.MethodPost, "/api/login", `{"password":"correct horse"}`, nil, mutate); response.Code != http.StatusForbidden {
			t.Errorf("login %s = %d, want 403", name, response.Code)
		}
	}
	if len(h.backend.controls) != 0 {
		t.Fatalf("cross-site control reached the backend: %v", h.backend.controls)
	}
	same := h.do(http.MethodPost, "/api/instances/session-1/stop", "", cookie, func(r *http.Request) {
		r.Header.Set("Origin", "http://webport.example.com")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	})
	if same.Code != http.StatusAccepted {
		t.Fatalf("same-origin stop = %d %s", same.Code, same.Body.String())
	}
}

func TestLoginLockout(t *testing.T) {
	h := newHarness(t)
	client := func(r *http.Request) { r.Header.Set("X-Real-Ip", "203.0.113.9") }
	for i := 0; i < freeFailures; i++ {
		if response := h.do(http.MethodPost, "/api/login", `{"password":"nope nope"}`, nil, client); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, response.Code)
		}
	}
	response := h.do(http.MethodPost, "/api/login", `{"password":"correct horse"}`, nil, client)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("locked out login = %d, want 429 with Retry-After", response.Code)
	}
	// Another client is unaffected.
	if response := h.do(http.MethodPost, "/api/login", `{"password":"correct horse"}`, nil); response.Code != http.StatusOK {
		t.Fatalf("other client = %d", response.Code)
	}
	h.mu.Lock()
	h.now = h.now.Add(2 * time.Second)
	h.mu.Unlock()
	if response := h.do(http.MethodPost, "/api/login", `{"password":"correct horse"}`, nil, client); response.Code != http.StatusOK {
		t.Fatalf("login after lockout = %d", response.Code)
	}
}

func TestInstanceEndpoints(t *testing.T) {
	h := newHarness(t)
	cookie := h.login()

	inspect := h.do(http.MethodGet, "/api/instances/session-1/inspect?sensitive=0", "", cookie)
	if inspect.Code != http.StatusOK || !strings.Contains(inspect.Body.String(), `\u003credacted\u003e`) {
		t.Fatalf("inspect = %d %s", inspect.Code, inspect.Body.String())
	}
	value := h.do(http.MethodGet, "/api/instances/session-1/env?service=web&name=SECRET", "", cookie)
	if value.Code != http.StatusOK || !strings.Contains(value.Body.String(), "hunter2") {
		t.Fatalf("env value = %d %s", value.Code, value.Body.String())
	}
	if missing := h.do(http.MethodGet, "/api/instances/session-1/env?service=web&name=NOPE", "", cookie); missing.Code != http.StatusNotFound {
		t.Fatalf("missing env = %d", missing.Code)
	}
	logs := h.do(http.MethodGet, "/api/instances/session-1/logs?service=web", "", cookie)
	if logs.Code != http.StatusOK || !strings.Contains(logs.Body.String(), "hello from web") {
		t.Fatalf("logs = %d %s", logs.Code, logs.Body.String())
	}
	if other := h.do(http.MethodGet, "/api/instances/session-1/logs?service=../../etc", "", cookie); other.Code != http.StatusNotFound {
		t.Fatalf("unknown service logs = %d", other.Code)
	}
	if gone := h.do(http.MethodPost, "/api/instances/session-2/restart", "", cookie); gone.Code != http.StatusNotFound {
		t.Fatalf("unknown instance = %d", gone.Code)
	}
	if bad := h.do(http.MethodPost, "/api/instances/session-1/kill", "", cookie); bad.Code != http.StatusNotFound {
		t.Fatalf("unknown operation = %d", bad.Code)
	}
	if restart := h.do(http.MethodPost, "/api/instances/session-1/restart", "", cookie); restart.Code != http.StatusAccepted {
		t.Fatalf("restart = %d", restart.Code)
	}
	if strings.Join(h.backend.controls, ",") != "session-1:restart" {
		t.Fatalf("controls = %v", h.backend.controls)
	}
}

func TestAssets(t *testing.T) {
	h := newHarness(t)
	index := h.do(http.MethodGet, "/", "", nil)
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), "app") || index.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("index = %d %v", index.Code, index.Header())
	}
	asset := h.do(http.MethodGet, "/assets/app.js", "", nil)
	if asset.Code != http.StatusOK || !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d %v", asset.Code, asset.Header())
	}
	if fallback := h.do(http.MethodGet, "/instances/session-1", "", nil); fallback.Code != http.StatusOK {
		t.Fatalf("SPA fallback = %d", fallback.Code)
	}
	if missing := h.do(http.MethodGet, "/missing.js", "", nil); missing.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d", missing.Code)
	}
	if api := h.do(http.MethodGet, "/api/nope", "", nil); api.Code != http.StatusNotFound {
		t.Fatalf("unknown API = %d", api.Code)
	}

}

func TestPasswordHashing(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	hash, err := HashPassword("long enough")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := HashPassword("long enough")
	if hash == other {
		t.Fatal("hashes are not salted")
	}
	if !VerifyPassword(hash, "long enough") || VerifyPassword(hash, "long enougH") || VerifyPassword("garbage", "long enough") {
		t.Fatal("password verification is wrong")
	}
}

func TestSettingsRoundTripAndValidation(t *testing.T) {
	path := t.TempDir() + "/webui.json"
	settings, err := LoadSettings(path)
	if err != nil || settings.Enabled || settings.EffectiveSubdomain() != DefaultSubdomain {
		t.Fatalf("defaults = %#v, %v", settings, err)
	}
	if err := SaveSettings(path, Settings{Enabled: true}); err == nil {
		t.Fatal("enabled without a password was saved")
	}
	hash, _ := HashPassword("correct horse")
	saved := Settings{Enabled: true, Subdomain: "dash", PasswordHash: hash}
	if err := SaveSettings(path, saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings(path)
	if err != nil || loaded != saved {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
	if err := SaveSettings(path, Settings{Subdomain: "Bad.Name"}); err == nil {
		t.Fatal("invalid subdomain saved")
	}
}
