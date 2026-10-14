package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/tui"
)

const (
	sessionCookie    = "webport_session"
	requestHeader    = "X-Webport-Request"
	sessionIdleLimit = 24 * time.Hour
	sessionMaxAge    = 7 * 24 * time.Hour
	maxSessions      = 64
	requestTimeout   = 5 * time.Second

	failureWindow    = 15 * time.Minute
	freeFailures     = 5
	maxLockout       = 15 * time.Minute
	globalFailures   = 50
	globalFailureKey = "*"
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Status describes the dashboard's own publication.
type Status struct {
	URL        string `json:"url,omitempty"`
	Subdomain  string `json:"subdomain"`
	Listen     string `json:"listen,omitempty"`
	RouteError string `json:"route_error,omitempty"`
}

// Server is the authenticated dashboard HTTP handler.
type Server struct {
	backend  tui.Backend
	listLive func() ([]state.LiveState, error)
	settings func() Settings
	status   func() Status
	assets   fs.FS
	now      func() time.Time
	verify   chan struct{}
	mux      *http.ServeMux

	mu       sync.Mutex
	sessions map[string]authSession
	failures map[string]failureRecord
}

type authSession struct {
	created, seen time.Time
	passwordHash  string
}

type failureRecord struct {
	count      int
	last, next time.Time
}

// ServerOptions configures NewServer. Backend and Settings are required.
type ServerOptions struct {
	Backend  tui.Backend
	ListLive func() ([]state.LiveState, error)
	Settings func() Settings
	Status   func() Status
	Assets   fs.FS
	Now      func() time.Time
}

func NewServer(options ServerOptions) *Server {
	s := &Server{
		backend: options.Backend, listLive: options.ListLive, settings: options.Settings,
		status: options.Status, assets: options.Assets, now: options.Now,
		verify: make(chan struct{}, 2), sessions: make(map[string]authSession),
		failures: make(map[string]failureRecord),
	}
	if s.listLive == nil {
		s.listLive = state.ListLive
	}
	if s.status == nil {
		s.status = func() Status { return Status{Subdomain: s.settings().EffectiveSubdomain()} }
	}
	if s.now == nil {
		s.now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth", s.handleAuth)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/snapshot", s.authenticated(s.handleSnapshot))
	mux.HandleFunc("GET /api/instances/{id}/inspect", s.authenticated(s.handleInspect))
	mux.HandleFunc("GET /api/instances/{id}/env", s.authenticated(s.handleEnvValue))
	mux.HandleFunc("GET /api/instances/{id}/logs", s.authenticated(s.handleLogs))
	mux.HandleFunc("POST /api/instances/{id}/{operation}", s.authenticated(s.handleControl))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	mux.HandleFunc("/", s.handleAsset)
	s.mux = mux
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		header.Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-site request rejected")
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

// sameOrigin rejects cross-site writes. The custom header cannot be sent
// cross-origin without a CORS preflight, which this server never approves;
// Origin and Fetch Metadata are checked as defense in depth.
func sameOrigin(r *http.Request) bool {
	if r.Header.Get(requestHeader) != "1" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			return false
		}
	}
	return true
}

func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		next(w, r)
	}
}

func (s *Server) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	current := s.settings().PasswordHash
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.sessions[cookie.Value]
	if !ok {
		return false
	}
	// Changing or clearing the password signs every browser out.
	if current == "" || subtle.ConstantTimeCompare([]byte(entry.passwordHash), []byte(current)) != 1 ||
		now.Sub(entry.seen) > sessionIdleLimit || now.Sub(entry.created) > sessionMaxAge {
		delete(s.sessions, cookie.Value)
		return false
	}
	entry.seen = now
	s.sessions[cookie.Value] = entry
	return true
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": s.validSession(r), "password_set": s.settings().HasPassword()})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key := clientKey(r)
	if wait := s.lockout(key); wait > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds()+0.999)))
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("Too many failed attempts. Try again in %s.", wait.Round(time.Second)))
		return
	}
	select {
	case s.verify <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	hash := s.settings().PasswordHash
	ok := hash != "" && VerifyPassword(hash, request.Password)
	<-s.verify
	if !ok {
		s.recordFailure(key)
		writeError(w, http.StatusUnauthorized, "Incorrect password.")
		return
	}
	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	now := s.now()
	s.mu.Lock()
	delete(s.failures, key)
	s.pruneSessionsLocked(now)
	s.sessions[token] = authSession{created: now, seen: now, passwordHash: hash}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionMaxAge.Seconds()),
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

func (s *Server) pruneSessionsLocked(now time.Time) {
	var oldestID string
	var oldest time.Time
	for id, entry := range s.sessions {
		if now.Sub(entry.seen) > sessionIdleLimit || now.Sub(entry.created) > sessionMaxAge {
			delete(s.sessions, id)
			continue
		}
		if oldestID == "" || entry.seen.Before(oldest) {
			oldestID, oldest = id, entry.seen
		}
	}
	if len(s.sessions) >= maxSessions && oldestID != "" {
		delete(s.sessions, oldestID)
	}
}

// lockout returns how long the client, or every client after many failures,
// must wait before another password attempt.
func (s *Server) lockout(key string) time.Duration {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var wait time.Duration
	for _, name := range []string{key, globalFailureKey} {
		record, ok := s.failures[name]
		if !ok {
			continue
		}
		if now.Sub(record.last) > failureWindow {
			delete(s.failures, name)
			continue
		}
		wait = max(wait, record.next.Sub(now))
	}
	return wait
}

func (s *Server) recordFailure(key string) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.failures) > 4096 {
		for name, record := range s.failures {
			if now.Sub(record.last) > failureWindow {
				delete(s.failures, name)
			}
		}
	}
	for name, limit := range map[string]int{key: freeFailures, globalFailureKey: globalFailures} {
		record := s.failures[name]
		if now.Sub(record.last) > failureWindow {
			record = failureRecord{}
		}
		record.count++
		record.last = now
		if record.count >= limit {
			delay := time.Second << min(record.count-limit, 10)
			record.next = now.Add(min(delay, maxLockout))
		}
		s.failures[name] = record
	}
}

// clientKey trusts Traefik's client address only from a loopback peer.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if forwarded := strings.TrimSpace(r.Header.Get("X-Real-Ip")); forwarded != "" && len(forwarded) <= 64 {
			return forwarded
		}
	}
	return host
}

func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

// Instance is the browser-safe view of live session metadata. It never
// carries the control socket path or credential.
type Instance struct {
	SessionID string                        `json:"session_id"`
	Name      string                        `json:"name"`
	URL       string                        `json:"url,omitempty"`
	Worktree  string                        `json:"worktree"`
	Profile   string                        `json:"profile"`
	StartedAt time.Time                     `json:"started_at"`
	Ports     map[string]int                `json:"ports,omitempty"`
	Services  map[string]state.ServiceState `json:"services"`
	Routes    map[string]state.RouteState   `json:"routes,omitempty"`
	Endpoints map[string]string             `json:"endpoints,omitempty"`
	LogPaths  map[string]string             `json:"log_paths,omitempty"`
	LastError string                        `json:"last_error,omitempty"`
}

type snapshotResponse struct {
	Instances   []Instance     `json:"instances"`
	Routes      []route.Route  `json:"routes"`
	Daemon      map[string]any `json:"daemon,omitempty"`
	Error       string         `json:"error,omitempty"`
	WebUI       Status         `json:"webui"`
	GeneratedAt time.Time      `json:"generated_at"`
}

func publicInstance(live state.LiveState) Instance {
	result := Instance{
		SessionID: live.SessionID, Name: tui.InstanceName(live), Worktree: live.Worktree,
		Profile: live.Profile, StartedAt: live.StartedAt, Ports: live.Ports,
		Services: live.Services, Routes: map[string]state.RouteState{}, Endpoints: map[string]string{},
		LogPaths: live.LogPaths, LastError: live.LastError,
	}
	if result.Services == nil {
		result.Services = map[string]state.ServiceState{}
	}
	if value := tui.InstanceURL(live, ""); tui.ValidHTTPURL(value) {
		result.URL = value
	}
	for name, item := range live.Routes {
		if item.URL != "" && !tui.ValidHTTPURL(item.URL) {
			item.URL = ""
		}
		result.Routes[name] = item
	}
	for name, value := range live.Endpoints {
		result.Endpoints[name] = value
	}
	return result
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	snapshot := s.backend.Snapshot(ctx)
	response := snapshotResponse{
		Instances: make([]Instance, 0, len(snapshot.Instances)), Routes: snapshot.Routes,
		Daemon: snapshot.Daemon, Error: snapshot.Error, WebUI: s.status(), GeneratedAt: s.now(),
	}
	if response.Routes == nil {
		response.Routes = []route.Route{}
	}
	for _, live := range snapshot.Instances {
		response.Instances = append(response.Instances, publicInstance(live))
	}
	writeJSON(w, http.StatusOK, response)
}

// instance resolves the session server-side; browsers never supply control
// credentials or file paths.
func (s *Server) instance(w http.ResponseWriter, r *http.Request) (state.LiveState, bool) {
	id := r.PathValue("id")
	if !sessionIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid instance ID")
		return state.LiveState{}, false
	}
	instances, err := s.listLive()
	for _, live := range instances {
		if live.SessionID == id {
			return live, true
		}
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Sessions: "+err.Error())
	} else {
		writeError(w, http.StatusNotFound, "Instance is no longer active.")
	}
	return state.LiveState{}, false
}

func queryBool(r *http.Request, name string) bool {
	value := r.URL.Query().Get(name)
	return value == "1" || value == "true"
}

func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	live, ok := s.instance(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	inspection, err := s.backend.Inspect(ctx, live, queryBool(r, "sensitive"), queryBool(r, "inherited"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "Inspection failed: "+err.Error())
		return
	}
	if inspection.Environment == nil {
		inspection.Environment = map[string]map[string]string{}
	}
	if inspection.Routes == nil {
		inspection.Routes = []session.InspectionRoute{}
	}
	writeJSON(w, http.StatusOK, inspection)
}

// handleEnvValue returns one cleartext value for a clipboard operation, so
// copying works while the page keeps sensitive values hidden.
func (s *Server) handleEnvValue(w http.ResponseWriter, r *http.Request) {
	live, ok := s.instance(w, r)
	if !ok {
		return
	}
	service, name := r.URL.Query().Get("service"), r.URL.Query().Get("name")
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	inspection, err := s.backend.Inspect(ctx, live, true, queryBool(r, "inherited"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "Inspection failed: "+err.Error())
		return
	}
	value, ok := inspection.Environment[service][name]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("Environment entry %q is no longer available.", name))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": value})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	live, ok := s.instance(w, r)
	if !ok {
		return
	}
	service := r.URL.Query().Get("service")
	if _, ok := live.Services[service]; !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("Service %q is not part of this instance.", service))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	lines, err := s.backend.Logs(ctx, live, service)
	response := map[string]any{"lines": lines}
	if lines == nil {
		response["lines"] = []string{}
	}
	if err != nil {
		response["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	operation := r.PathValue("operation")
	if operation != "stop" && operation != "restart" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	live, ok := s.instance(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if err := s.backend.Control(ctx, live, operation); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": operation + " requested; waiting for session state"})
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if s.assets == nil {
		http.NotFound(w, r)
		return
	}
	if info, err := fs.Stat(s.assets, name); err == nil && !info.IsDir() {
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, s.assets, name)
		return
	}
	// Client-side routes fall back to the application shell; missing files
	// are not found.
	if strings.Contains(path.Base(name), ".") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.assets, "index.html")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		log.Printf("WARN: web UI response encoding failed: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
