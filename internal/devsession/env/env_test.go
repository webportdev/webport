package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/plan"
)

func TestResolveAppliesPrecedenceAndForwardReferences(t *testing.T) {
	dotenv := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(dotenv, []byte("VALUE=dotenv\nDOT_ONLY=from-dotenv\nQUOTED='a # b' # comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	literal := func(value string) config.Value { return config.Value{Literal: stringPtr(value)} }
	got, err := Resolve(Input{
		Inherited:   map[string]string{"VALUE": "inherited", "INHERITED": "yes"},
		DotenvFiles: []string{dotenv},
		Top: map[string]config.Value{
			"VALUE":   literal("top"),
			"FORWARD": literal("${env.SERVICE}-${env.PROFILE}"),
		},
		Profile: map[string]config.Value{"PROFILE": literal("profile")},
		Service: map[string]config.Value{"SERVICE": literal("service")},
		Project: "app", Branch: "main", Scope: "app-worktree-abc",
		Ports: map[string]int{"api": 8080},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"VALUE": "top", "DOT_ONLY": "from-dotenv", "INHERITED": "yes", "FORWARD": "service-profile", "QUOTED": "a # b"} {
		entry, ok := got.Get(name)
		if !ok || entry.Value != want {
			t.Errorf("%s = %+v, want %q", name, entry, want)
		}
	}
}

func TestManagedOnlyExcludesInheritedValuesAndKeepsOverrides(t *testing.T) {
	literal := func(value string) config.Value { return config.Value{Literal: stringPtr(value)} }
	got, err := Resolve(Input{
		Inherited: map[string]string{
			"INHERITED_ONLY": "from-process",
			"OVERRIDDEN":     "from-process",
		},
		Top: map[string]config.Value{
			"OVERRIDDEN": literal("from-webport"),
			"MANAGED":    literal("configured"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	managed := got.ManagedOnly().Map(true)
	if _, ok := managed["INHERITED_ONLY"]; ok {
		t.Fatalf("managed values retained inherited-only variable: %v", managed)
	}
	if managed["OVERRIDDEN"] != "from-webport" || managed["MANAGED"] != "configured" {
		t.Fatalf("managed values = %v", managed)
	}
}

func TestResolveMarksInheritedValuesSensitiveWhenRequested(t *testing.T) {
	values, err := Resolve(Input{
		Inherited:          map[string]string{"TOKEN": "secret"},
		InheritedSensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := values.Get("TOKEN")
	if !ok || !entry.Sensitive {
		t.Fatalf("TOKEN entry = %+v, want sensitive", entry)
	}
	if got := values.Map(false)["TOKEN"]; got != "<redacted>" {
		t.Fatalf("redacted TOKEN = %q", got)
	}
	if got := values.Map(true)["TOKEN"]; got != "secret" {
		t.Fatalf("sensitive TOKEN = %q", got)
	}
}

func TestResolveSupportsAllowedReferencesAndSensitiveTaint(t *testing.T) {
	literal := func(value string, sensitive bool) config.Value {
		return config.Value{Literal: stringPtr(value), Sensitive: sensitive}
	}
	routes := plan.Routes{Routes: []plan.Route{{Service: "frontend", Host: "app-main.test", URL: "https://app-main.test", Available: true, Export: map[string]string{"host": "PUBLIC_HOST"}, ExportReceivers: []string{"frontend"}}}}
	got, err := Resolve(Input{
		Top: map[string]config.Value{
			"SECRET":  literal("shh", true),
			"DERIVED": literal("prefix-${env.SECRET}", false),
			"URL":     literal("${routes.frontend.url}:${ports.api}", false),
		},
		Project: "app", Branch: "main", Scope: "scope", Ports: map[string]int{"api": 3000}, Routes: routes, ServiceName: "frontend",
	})
	if err != nil {
		t.Fatal(err)
	}
	derived, _ := got.Get("DERIVED")
	if !derived.Sensitive || derived.Value != "prefix-shh" {
		t.Fatalf("derived = %+v", derived)
	}
	urlValue, _ := got.Get("URL")
	if urlValue.Value != "https://app-main.test:3000" || urlValue.Sensitive {
		t.Fatalf("url = %+v", urlValue)
	}
	if _, err := got.ExpandArgument("--secret=${SECRET}"); err == nil {
		t.Fatal("ExpandArgument() allowed sensitive interpolation")
	}
	if rendered, err := got.Render("bash", false); err != nil || !strings.Contains(rendered, "SECRET='<redacted>'") {
		t.Fatalf("redacted bash = %q, %v", rendered, err)
	}
	if rendered, err := got.Render("fish", true); err != nil || !strings.Contains(rendered, "set -gx URL") {
		t.Fatalf("fish = %q, %v", rendered, err)
	}
}

func TestResolveRejectsCyclesUnknownReferencesAndShellExpansion(t *testing.T) {
	literal := func(value string) config.Value { return config.Value{Literal: stringPtr(value)} }
	for _, values := range []map[string]config.Value{
		{"A": literal("${env.B}"), "B": literal("${env.A}")},
		{"A": literal("${env.MISSING}")},
		{"A": literal("${HOME}")},
	} {
		_, err := Resolve(Input{Top: values})
		if err == nil {
			t.Errorf("Resolve(%v) error = nil", values)
		}
	}
}

func TestResolveDefersRuntimePortUntilServiceStart(t *testing.T) {
	literal := func(value string) config.Value { return config.Value{Literal: stringPtr(value)} }
	values, err := Resolve(Input{
		Top:           map[string]config.Value{"API_URL": literal("http://127.0.0.1:${ports.api}")},
		DeferredPorts: map[string]struct{}{"api": {}},
		DeferRuntime:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := values.Get("API_URL")
	if !ok || entry.Value != "http://127.0.0.1:${ports.api}" {
		t.Fatalf("deferred value = %+v", entry)
	}
	resolved, err := values.ExpandRuntimeValues(Runtime{Ports: map[string]int{"api": 4312}})
	if err != nil {
		t.Fatal(err)
	}
	entry, _ = resolved.Get("API_URL")
	if entry.Value != "http://127.0.0.1:4312" {
		t.Fatalf("resolved value = %+v", entry)
	}
}

func TestDotenvSyntaxIsNotSourcedThroughShell(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("A=$(touch /tmp/should-not-exist)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(Input{DotenvFiles: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/tmp/should-not-exist"); err == nil {
		t.Fatal("dotenv parser executed shell syntax")
	}
}

func stringPtr(value string) *string { return &value }
