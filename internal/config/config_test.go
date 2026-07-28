package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Set required env var
	unsetEnvVars()
	os.Setenv("WEBPORT_BASE_DOMAIN", "test.example.com")

	cfg := Load()

	if cfg.TraefikDynamicConfigPath != "/etc/traefik/dynamic/webport.yml" {
		t.Errorf("TraefikDynamicConfigPath = %v", cfg.TraefikDynamicConfigPath)
	}
	if cfg.TraefikEntryPoint != "websecure" {
		t.Errorf("TraefikEntryPoint = %v, want websecure", cfg.TraefikEntryPoint)
	}
	if cfg.TraefikCertResolver != "webport" {
		t.Errorf("TraefikCertResolver = %v, want webport", cfg.TraefikCertResolver)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %v, want 8080", cfg.Port)
	}
	if cfg.ListenHost != "127.0.0.1" {
		t.Errorf("ListenHost = %v, want 127.0.0.1", cfg.ListenHost)
	}
	if cfg.DefaultTTL != 300*time.Second {
		t.Errorf("DefaultTTL = %v, want 300s", cfg.DefaultTTL)
	}
	if cfg.TTLCheckInterval != 30*time.Second {
		t.Errorf("TTLCheckInterval = %v, want 30s", cfg.TTLCheckInterval)
	}
}

func TestLoadWithEnvVars(t *testing.T) {
	unsetEnvVars()

	os.Setenv("WEBPORT_BASE_DOMAIN", "example.com")
	os.Setenv("WEBPORT_PORT", "9000")
	os.Setenv("WEBPORT_LISTEN_HOST", "0.0.0.0")
	os.Setenv("WEBPORT_DEFAULT_TTL", "600s")
	os.Setenv("WEBPORT_TTL_CHECK_INTERVAL", "60s")
	os.Setenv("WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH", "/tmp/dynamic.yml")
	os.Setenv("WEBPORT_TRAEFIK_ENTRYPOINT", "https")
	os.Setenv("WEBPORT_TRAEFIK_CERT_RESOLVER", "acme")

	cfg := Load()

	if cfg.BaseDomain != "example.com" {
		t.Errorf("BaseDomain = %v, want example.com", cfg.BaseDomain)
	}
	if cfg.Port != 9000 {
		t.Errorf("Port = %v, want 9000", cfg.Port)
	}
	if cfg.ListenHost != "0.0.0.0" {
		t.Errorf("ListenHost = %v, want 0.0.0.0", cfg.ListenHost)
	}
	if cfg.DefaultTTL != 600*time.Second {
		t.Errorf("DefaultTTL = %v, want 600s", cfg.DefaultTTL)
	}
	if cfg.TTLCheckInterval != 60*time.Second {
		t.Errorf("TTLCheckInterval = %v, want 60s", cfg.TTLCheckInterval)
	}
	if cfg.TraefikDynamicConfigPath != "/tmp/dynamic.yml" {
		t.Errorf("TraefikDynamicConfigPath = %v", cfg.TraefikDynamicConfigPath)
	}
	if cfg.TraefikEntryPoint != "https" || cfg.TraefikCertResolver != "acme" {
		t.Errorf("Traefik config = %q/%q", cfg.TraefikEntryPoint, cfg.TraefikCertResolver)
	}
}

func TestLoadRequiredBaseDomain(t *testing.T) {
	unsetEnvVars()
	// Don't set WEBPORT_BASE_DOMAIN

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when BASE_DOMAIN is not set")
		}
	}()

	Load()
}

func TestGetListenAddr(t *testing.T) {
	unsetEnvVars()
	os.Setenv("WEBPORT_BASE_DOMAIN", "example.com")
	os.Setenv("WEBPORT_PORT", "9999")

	cfg := Load()
	addr := cfg.GetListenAddr()

	if addr != "127.0.0.1:9999" {
		t.Errorf("GetListenAddr() = %v, want 127.0.0.1:9999", addr)
	}
}

func TestGetListenAddrIPv6(t *testing.T) {
	cfg := &Config{ListenHost: "::1", Port: 8080}
	if addr := cfg.GetListenAddr(); addr != "[::1]:8080" {
		t.Errorf("GetListenAddr() = %v, want [::1]:8080", addr)
	}
}

func unsetEnvVars() {
	os.Unsetenv("WEBPORT_BASE_DOMAIN")
	os.Unsetenv("WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH")
	os.Unsetenv("WEBPORT_TRAEFIK_ENTRYPOINT")
	os.Unsetenv("WEBPORT_TRAEFIK_CERT_RESOLVER")
	os.Unsetenv("WEBPORT_LISTEN_HOST")
	os.Unsetenv("WEBPORT_PORT")
	os.Unsetenv("WEBPORT_DEFAULT_TTL")
	os.Unsetenv("WEBPORT_TTL_CHECK_INTERVAL")
}
