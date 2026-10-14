package webui

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/route"
)

func newTestManager(t *testing.T) (*Manager, *route.Store, *route.Controller) {
	t.Helper()
	store := route.NewStore()
	controller := route.NewController(store, func() error { return nil })
	manager, err := NewManager(ManagerOptions{
		SettingsPath: filepath.Join(t.TempDir(), "webui.json"), Listen: "127.0.0.1:0",
		BaseDomain: "example.test", Routes: controller, Backend: &fakeBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.Start()
	t.Cleanup(manager.Stop)
	return manager, store, controller
}

func TestManagerLifecycle(t *testing.T) {
	manager, store, _ := newTestManager(t)
	if _, err := manager.Apply(Update{Enabled: ptr(true)}); err == nil {
		t.Fatal("enabled without a password")
	}
	hash, _ := HashPassword("correct horse")
	report, err := manager.Apply(Update{Enabled: ptr(true), PasswordHash: &hash})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Running || report.URL != "https://webport.example.test" || report.Listen == "" {
		t.Fatalf("report = %#v", report)
	}
	published, ok := store.Get(route.RouteID{Project: RouteProject, Branch: RouteBranch})
	if !ok || published.Domain != "webport.example.test" || !published.IsSystem() || !published.ExpiresAt.IsZero() {
		t.Fatalf("system route = %#v", published)
	}
	response, err := http.Get("http://" + report.Listen + "/api/auth")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("dashboard request = %v, %v", response, err)
	}
	response.Body.Close()

	report, err = manager.Apply(Update{Subdomain: ptr("dash")})
	if err != nil || report.URL != "https://dash.example.test" {
		t.Fatalf("subdomain report = %#v, %v", report, err)
	}
	if store.Count() != 1 {
		t.Fatalf("routes after subdomain change = %d, want 1", store.Count())
	}

	// Settings survive a daemon restart.
	reloaded, err := NewManager(ManagerOptions{SettingsPath: manager.options.SettingsPath})
	if err != nil || reloaded.settings.Subdomain != "dash" || !reloaded.settings.Enabled {
		t.Fatalf("reloaded = %#v, %v", reloaded.settings, err)
	}

	report, err = manager.Apply(Update{Enabled: ptr(false)})
	if err != nil || report.Running || report.URL != "" || store.Count() != 0 {
		t.Fatalf("disabled report = %#v, routes %d, %v", report, store.Count(), err)
	}
}

func TestManagerHostnameConflictFailsClosed(t *testing.T) {
	manager, store, controller := newTestManager(t)
	now := time.Now()
	// A developer route already produces dash-ui.example.test.
	if _, _, _, err := controller.CreateLease("dev", route.RegisterRequest{Project: "dash", Branch: "ui", Port: 3000}, "example.test", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashPassword("correct horse")
	report, err := manager.Apply(Update{Enabled: ptr(true), PasswordHash: &hash, Subdomain: ptr("dash-ui")})
	if err != nil {
		t.Fatal(err)
	}
	if report.URL != "" || !strings.Contains(report.RouteError, "conflict") {
		t.Fatalf("conflict report = %#v", report)
	}
	if got, _ := store.Get(route.RouteID{Project: "dash", Branch: "ui"}); got.Port != 3000 {
		t.Fatalf("developer route was replaced: %#v", got)
	}
	report, err = manager.Apply(Update{Subdomain: ptr("")})
	if err != nil || report.URL != "https://webport.example.test" || report.RouteError != "" {
		t.Fatalf("recovered report = %#v, %v", report, err)
	}
}

func ptr[T any](value T) *T { return &value }
