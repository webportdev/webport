package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/socket"
	webuiserver "github.com/webportdev/webport/internal/webui"
)

type fakeSystemRoutes struct{ subdomain string }

func (f *fakeSystemRoutes) SetSystemRoute(req route.RegisterRequest, baseDomain string, _ time.Time) (route.Route, error) {
	f.subdomain = req.Subdomain
	return route.Route{Project: req.Project, Branch: req.Branch, Port: req.Port, Domain: req.Subdomain + "." + baseDomain}, nil
}

func (f *fakeSystemRoutes) DeleteSystemRoute(route.RouteID, string, time.Time) error {
	f.subdomain = ""
	return nil
}

// startWebUIDaemon serves the real web UI manager on a real socket.
func startWebUIDaemon(t *testing.T) (string, *fakeSystemRoutes) {
	t.Helper()
	// Unix socket paths are limited to about 100 bytes.
	directory, err := os.MkdirTemp("", "wpui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	settingsPath := filepath.Join(directory, "webui.json")
	routes := &fakeSystemRoutes{}
	manager, err := webuiserver.NewManager(webuiserver.ManagerOptions{
		SettingsPath: settingsPath, Listen: "127.0.0.1:0", BaseDomain: "example.test", Routes: routes,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.Start()
	t.Cleanup(manager.Stop)
	socketPath := filepath.Join(directory, "d.sock")
	uid := os.Getuid()
	server, err := socket.Listen(socketPath, func(peer int) bool { return peer == uid }, manager.SocketHandlers(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv(socket.EnvPath, socketPath)
	return settingsPath, routes
}

func runWebUITest(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runCLI(append([]string{"webui"}, args...), strings.NewReader(input), &out, &errOut)
	return out.String() + errOut.String(), err
}

func TestWebUIPasswordFromStdin(t *testing.T) {
	settingsPath, _ := startWebUIDaemon(t)
	if _, err := runWebUITest(t, "short\n", "password", "--stdin"); err == nil {
		t.Fatal("short password accepted")
	}
	if _, err := runWebUITest(t, "", "password", "hunter2hunter2"); err == nil {
		t.Fatal("password accepted as an argument")
	}
	if _, err := runWebUITest(t, "", "password"); err == nil || !strings.Contains(err.Error(), "--stdin") {
		t.Fatalf("non-terminal prompt error = %v", err)
	}
	output, err := runWebUITest(t, "correct horse battery\n", "password", "--stdin")
	if err != nil || !strings.Contains(output, "password updated") {
		t.Fatalf("output = %q, %v", output, err)
	}
	settings, err := webuiserver.LoadSettings(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !webuiserver.VerifyPassword(settings.PasswordHash, "correct horse battery") {
		t.Fatal("stored hash does not verify")
	}
	if info, err := os.Stat(settingsPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v, %v", info.Mode(), err)
	}
}

func TestWebUIEnableSubdomainDisable(t *testing.T) {
	settingsPath, routes := startWebUIDaemon(t)
	if _, err := runWebUITest(t, "", "enable"); err == nil || !strings.Contains(err.Error(), "--stdin") {
		t.Fatalf("enable without password error = %v", err)
	}
	if _, err := runWebUITest(t, "correct horse battery\n", "password", "--stdin"); err != nil {
		t.Fatal(err)
	}
	output, err := runWebUITest(t, "", "enable", "--subdomain", "Dash")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "url: https://dash.example.test") || routes.subdomain != "dash" {
		t.Fatalf("enable output = %q, route subdomain %q", output, routes.subdomain)
	}

	status, err := runWebUITest(t, "", "status", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var report webuiserver.Report
	if err := json.Unmarshal([]byte(status), &report); err != nil {
		t.Fatalf("status = %q: %v", status, err)
	}
	if !report.Enabled || !report.PasswordSet || !report.Running || report.Subdomain != "dash" || report.SettingsPath != settingsPath {
		t.Fatalf("report = %#v", report)
	}
	if strings.Contains(status, "pbkdf2") {
		t.Fatal("status exposed the password hash")
	}

	if _, err := runWebUITest(t, "", "subdomain", "not.valid"); err == nil {
		t.Fatal("invalid subdomain accepted")
	}
	if output, err := runWebUITest(t, "", "subdomain", "--reset"); err != nil || !strings.Contains(output, "https://webport.example.test") {
		t.Fatalf("reset output = %q, %v", output, err)
	}
	if output, _ := runWebUITest(t, "", "subdomain"); strings.TrimSpace(output) != "webport" {
		t.Fatalf("subdomain = %q", output)
	}

	if _, err := runWebUITest(t, "", "disable"); err != nil {
		t.Fatal(err)
	}
	settings, _ := webuiserver.LoadSettings(settingsPath)
	if settings.Enabled || !settings.HasPassword() || routes.subdomain != "" {
		t.Fatalf("after disable: settings %#v, route %q", settings, routes.subdomain)
	}
}

func TestWebUIReportsUnreachableDaemon(t *testing.T) {
	t.Setenv(socket.EnvPath, filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := runWebUITest(t, "", "status"); err == nil || !strings.Contains(err.Error(), "cannot reach the webport daemon") {
		t.Fatalf("error = %v", err)
	}
}
