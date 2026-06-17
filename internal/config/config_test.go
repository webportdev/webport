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

	if cfg.CaddyfilePath != "/etc/caddy/webport.d/Caddyfile" {
		t.Errorf("CaddyfilePath = %v, want /etc/caddy/webport.d/Caddyfile", cfg.CaddyfilePath)
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
	// TLS defaults to empty (HTTP challenge)
	if cfg.TLS.DNSProvider != "" {
		t.Errorf("DNSProvider = %v, want empty", cfg.TLS.DNSProvider)
	}
	if cfg.TLS.DNSProviderModule != "" {
		t.Errorf("DNSProviderModule = %v, want empty", cfg.TLS.DNSProviderModule)
	}
}

func TestLoadWithEnvVars(t *testing.T) {
	unsetEnvVars()

	os.Setenv("WEBPORT_BASE_DOMAIN", "example.com")
	os.Setenv("WEBPORT_PORT", "9000")
	os.Setenv("WEBPORT_LISTEN_HOST", "0.0.0.0")
	os.Setenv("WEBPORT_DEFAULT_TTL", "600s")
	os.Setenv("WEBPORT_TTL_CHECK_INTERVAL", "60s")

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
}

func TestLoadWithTLSConfig(t *testing.T) {
	unsetEnvVars()

	os.Setenv("WEBPORT_BASE_DOMAIN", "example.com")
	os.Setenv("WEBPORT_TLS_DNS_PROVIDER", "cloudflare")
	os.Setenv("WEBPORT_TLS_DNS_PROVIDER_MODULE", "cloudflare")

	cfg := Load()

	if cfg.TLS.DNSProvider != "cloudflare" {
		t.Errorf("DNSProvider = %v, want cloudflare", cfg.TLS.DNSProvider)
	}
	if cfg.TLS.DNSProviderModule != "cloudflare" {
		t.Errorf("DNSProviderModule = %v, want cloudflare", cfg.TLS.DNSProviderModule)
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
	os.Unsetenv("WEBPORT_CADDYFILE_PATH")
	os.Unsetenv("WEBPORT_LISTEN_HOST")
	os.Unsetenv("WEBPORT_PORT")
	os.Unsetenv("WEBPORT_DEFAULT_TTL")
	os.Unsetenv("WEBPORT_CADDY_RELOAD_CMD")
	os.Unsetenv("WEBPORT_TTL_CHECK_INTERVAL")
	os.Unsetenv("WEBPORT_TLS_DNS_PROVIDER")
	os.Unsetenv("WEBPORT_TLS_DNS_PROVIDER_MODULE")
	os.Unsetenv("WEBPORT_TLS_DNS_TOKEN_ENV_VAR")
}
