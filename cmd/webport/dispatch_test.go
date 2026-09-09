package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunCLIUsesProvidedStreamsForSimpleCommands(t *testing.T) {
	var out bytes.Buffer
	if err := runCLI([]string{"version"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("version output is empty")
	}

	out.Reset()
	if err := runCLI([]string{"help"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "webport dev") {
		t.Fatalf("help output = %q", out.String())
	}
	if !strings.Contains(out.String(), "webport upgrade") {
		t.Fatalf("help output does not document upgrade = %q", out.String())
	}
}

func TestRunCLIReportsUnknownCommandWithoutExiting(t *testing.T) {
	var out bytes.Buffer
	err := runCLI([]string{"unknown"}, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), `unknown command "unknown"`) {
		t.Fatalf("runCLI() error = %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("unknown command output = %q", out.String())
	}
}

func TestWrapperResolutionFormatsDoNotExposeDiscoveryToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"base_domain":         "dev.example.test",
			"default_ttl_seconds": 300,
			"tls_mode":            "local-ca",
		})
	}))
	defer server.Close()

	values := routeFlags{project: "app", branch: "feature/auth", api: server.URL, ttl: 30 * time.Second}
	resolution, err := resolveWrapperValues(values)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Host != "app-feature-auth.dev.example.test" || resolution.URL != "https://app-feature-auth.dev.example.test" {
		t.Fatalf("resolution = %+v", resolution)
	}
	var output bytes.Buffer
	if err := writeWrapperResolution(resolution, "env", &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "CLIENT_TOKEN") || !strings.Contains(output.String(), "WEBPORT_ROUTE='app:feature/auth'") {
		t.Fatalf("env output = %q", output.String())
	}
}

func TestWithEnvironmentReplacesExistingValuesAndPreservesOtherArguments(t *testing.T) {
	got := withEnvironment([]string{"KEEP=one", "WEBPORT_HOST=old", "WEBPORT_CLIENT_TOKEN=old"}, map[string]string{
		"WEBPORT_HOST":         "new",
		"WEBPORT_CLIENT_TOKEN": "secret",
	})
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "WEBPORT_HOST=old") || strings.Contains(joined, "CLIENT_TOKEN=old") || !strings.Contains(joined, "KEEP=one") || !strings.Contains(joined, "WEBPORT_HOST=new") {
		t.Fatalf("environment = %v", got)
	}
}

func TestParseDevOperationArgsAcceptsOptionsAfterOperation(t *testing.T) {
	var configPath, profile, api, format, shell string
	var showSensitive, follow, cleanSecrets bool
	positional, err := parseDevOperationArgs(
		[]string{"--format", "json", "--shell=fish", "--config", "session.yaml", "--profile", "ci", "--api", "http://session", "--show-sensitive", "--follow", "--secrets", "backend"},
		&configPath, &profile, &api, &format, &shell, &showSensitive, &follow, &cleanSecrets,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(positional) != 1 || positional[0] != "backend" || configPath != "session.yaml" || profile != "ci" || api != "http://session" || format != "json" || shell != "fish" || !showSensitive || !follow || !cleanSecrets {
		t.Fatalf("parsed operation arguments = %q, %q, %q, %q, %q, %q, %t, %t, %t", positional, configPath, profile, api, format, shell, showSensitive, follow, cleanSecrets)
	}
}
