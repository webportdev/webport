package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDiscoversNearestConfigAndMergesLocalFile(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "packages", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(root, ".webport.yaml")
	local := filepath.Join(root, ".webport.local.yaml")
	writeConfig(t, primary, `version: 1
env:
  BASE: one
  REPLACE: primary
services:
  app:
    command: [go, run, ./app]
    env: {A: one}
profiles:
  default: {services: [app]}
`)
	writeConfig(t, local, `env:
  LOCAL: two
  REPLACE: local
services:
  app:
    env: {B: two}
`)

	got, err := Load(Options{CurrentDir: nested, WorktreeRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source.PrimaryPath != primary || got.Source.LocalPath != local {
		t.Fatalf("source = %+v", got.Source)
	}
	if got.Env["BASE"].Literal == nil || *got.Env["BASE"].Literal != "one" || *got.Env["LOCAL"].Literal != "two" || *got.Env["REPLACE"].Literal != "local" {
		t.Fatalf("merged env = %+v", got.Env)
	}
	if got.Services["app"].Env["A"].Literal == nil || got.Services["app"].Env["B"].Literal == nil {
		t.Fatalf("recursive service merge = %+v", got.Services["app"].Env)
	}
	if got.Session.RetainLastSummary == nil || !*got.Session.RetainLastSummary {
		t.Fatal("default retain_last_summary = false")
	}
}

func TestLoadSearchStopsAtWorktreeRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, filepath.Join(filepath.Dir(root), ".webport.yaml"), `version: 1`)
	_, err := Load(Options{CurrentDir: nested, WorktreeRoot: root})
	if err == nil || !strings.Contains(err.Error(), "file not found before worktree root") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadExplicitConfigUsesSiblingLocalFile(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "project.yaml")
	writeConfig(t, primary, `version: 1
profiles: {default: {services: [api]}}
services: {api: {command: [go]}}
`)
	writeConfig(t, filepath.Join(dir, ".webport.local.yaml"), `project: local-project
`)
	got, err := LoadFrom(primary)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "local-project" {
		t.Fatalf("project = %q", got.Project)
	}
}

func TestLoadRejectsUnknownAndDuplicateKeysWithLocation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{name: "unknown", body: "version: 1\nunknown: true\n", want: []string{".webport.yaml", "unknown", "unknown field"}},
		{name: "duplicate", body: "version: 1\nversion: 1\n", want: []string{".webport.yaml", "version", "duplicate"}},
		{name: "nested unknown", body: "version: 1\nservices:\n  api:\n    command: [go]\n    mystery: true\n", want: []string{".webport.yaml", "services.api.mystery", "unknown field"}},
		{name: "bad duration", body: "version: 1\nservices:\n  api:\n    command: [go]\n    ready:\n      http:\n        url: http://localhost\n        timeout: nope\n", want: []string{".webport.yaml", "services.api.ready.http.timeout", "invalid duration"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".webport.yaml")
			writeConfig(t, path, test.body)
			_, err := LoadFrom(path)
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadRejectsWrongTypeAndUnsupportedVersion(t *testing.T) {
	for _, body := range []string{
		"version: '1'\n",
		"version: 2\n",
		"version: 1\nservices: []\n",
	} {
		path := filepath.Join(t.TempDir(), ".webport.yaml")
		writeConfig(t, path, body)
		_, err := LoadFrom(path)
		if err == nil {
			t.Errorf("Load(%q) error = nil", body)
		}
	}
}

func TestLoadDoesNotMutateEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".webport.yaml")
	writeConfig(t, path, "version: 1\nenv: {VALUE: \"${env.OTHER}\"}\n")
	_ = os.Unsetenv("VALUE")
	_, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("VALUE"); ok {
		t.Fatal("Load mutated process environment")
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
