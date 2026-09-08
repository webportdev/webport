package plan

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/identity"
)

type fakeDaemon struct {
	config daemon.Config
	err    error
}

func (f fakeDaemon) Config(context.Context) (daemon.Config, error) { return f.config, f.err }
func (fakeDaemon) Register(context.Context, daemon.RouteRequest) (daemon.Lease, error) {
	return daemon.Lease{}, nil
}
func (fakeDaemon) Heartbeat(context.Context, string, time.Duration) (daemon.Lease, error) {
	return daemon.Lease{}, nil
}
func (fakeDaemon) Release(context.Context, string) error { return nil }

func TestResolveRoutesUsesRawIdentityAndDaemonHostname(t *testing.T) {
	cfg := config.Config{
		Project: "cortex", Branch: "feature/auth",
		Services: map[string]config.Service{
			"frontend": {Route: &config.Route{Port: "frontend", Export: map[string]string{"host": "WEBPORT_DOMAIN", "url": "WEBPORT_PUBLIC_URL"}}},
			"mailpit":  {DependsOn: []string{"frontend"}, Route: &config.Route{Port: "mailpit", Optional: true, Export: map[string]string{"host": "MAILPIT_HOST"}}},
		},
	}
	id := identity.Identity{Project: "cortex", Branch: "feature/auth"}
	got, err := ResolveRoutes(context.Background(), cfg, id, fakeDaemon{config: daemon.Config{BaseDomain: "dev.example.test", DefaultTTL: 300, TLSMode: "local-ca", CACertPath: "/tmp/ca.crt"}}, "frontend", map[string]int{"frontend": 5173, "mailpit": 8025})
	if err != nil {
		t.Fatal(err)
	}
	if got.Daemon.CACertPath != "/tmp/ca.crt" || len(got.Routes) != 2 {
		t.Fatalf("routes = %+v", got)
	}
	frontend := got.Routes[0]
	if frontend.RawID != "cortex:feature/auth" || frontend.Host != "cortex-feature-auth.dev.example.test" || frontend.URL != "https://cortex-feature-auth.dev.example.test" {
		t.Fatalf("frontend route = %+v", frontend)
	}
	if len(frontend.ExportReceivers) != 2 || frontend.ExportReceivers[0] != "frontend" || frontend.ExportReceivers[1] != "mailpit" {
		t.Fatalf("frontend export receivers = %v", frontend.ExportReceivers)
	}
	mailpit := got.Routes[1]
	if mailpit.Project != "cortex-mailpit" || len(mailpit.ExportReceivers) != 1 || mailpit.ExportReceivers[0] != "mailpit" {
		t.Fatalf("mailpit route = %+v", mailpit)
	}
	if !strings.Contains(got.Env(), "WEBPORT_ROUTE_FRONTEND_HOST='cortex-feature-auth.dev.example.test'") {
		t.Fatalf("env = %s", got.Env())
	}
	jsonValue, err := got.JSON()
	if err != nil || !bytes.Contains(jsonValue, []byte(`"schema_version": 1`)) {
		t.Fatalf("JSON = %s, %v", jsonValue, err)
	}
}

func TestResolveRoutesOptionalDaemonFailureDoesNotInventPublicValues(t *testing.T) {
	cfg := config.Config{Services: map[string]config.Service{
		"frontend": {Route: &config.Route{Port: "frontend", Optional: true}},
	}}
	got, err := ResolveRoutes(context.Background(), cfg, identity.Identity{Project: "app", Branch: "main"}, fakeDaemon{err: errors.New("offline")}, "frontend", map[string]int{"frontend": 3000})
	if err != nil {
		t.Fatal(err)
	}
	if got.Daemon.Available || got.Routes[0].Host != "" || got.Routes[0].URL != "" || got.Routes[0].Available {
		t.Fatalf("optional failure result = %+v", got)
	}
}

func TestResolveRoutesRequiredDaemonFailureAndConflicts(t *testing.T) {
	cfg := config.Config{Services: map[string]config.Service{
		"api": {Route: &config.Route{Port: "api"}},
	}}
	_, err := ResolveRoutes(context.Background(), cfg, identity.Identity{Project: "app", Branch: "main"}, fakeDaemon{err: errors.New("offline")}, "api", map[string]int{"api": 3000})
	if err == nil || !strings.Contains(err.Error(), "required route") {
		t.Fatalf("required failure = %v", err)
	}

	cfg = config.Config{Env: map[string]config.Value{"PUBLIC": {}}, Services: map[string]config.Service{
		"api": {Route: &config.Route{Port: "api", Export: map[string]string{"host": "PUBLIC"}}},
	}}
	_, err = ResolveRoutes(context.Background(), cfg, identity.Identity{Project: "app", Branch: "main"}, fakeDaemon{config: daemon.Config{BaseDomain: "test"}}, "api", map[string]int{"api": 3000})
	if err == nil || !strings.Contains(err.Error(), "conflicts with configured environment") {
		t.Fatalf("export conflict = %v", err)
	}
}
