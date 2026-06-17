package config

import (
	"net"
	"os"
	"strconv"
	"time"
)

// Config holds all service configuration
type Config struct {
	// Caddyfile output path
	CaddyfilePath string
	// Base domain for routes (e.g., "mond.boo")
	BaseDomain string
	// HTTP API listen host
	ListenHost string
	// HTTP API port
	Port int
	// Default TTL for new routes
	DefaultTTL time.Duration
	// Caddy reload command
	CaddyReloadCmd string
	// TTL check interval
	TTLCheckInterval time.Duration
	// Shutdown timeout for graceful shutdown
	ShutdownTimeout time.Duration
	// TLS configuration
	TLS TLSConfig
}

// TLSConfig holds TLS/DNS challenge configuration
type TLSConfig struct {
	// DNS provider for challenges (e.g., "cloudflare", "digitalocean", "route53")
	// Leave empty to use HTTP challenge (default)
	DNSProvider string
	// DNS provider config module (e.g., "cloudflare" for Caddy module)
	// See https://caddyserver.com/docs/caddyfile/directives/tls#dns-providers
	DNSProviderModule string
	// Environment variable name that holds the DNS provider API token
	// Route53 does not use a token argument; other providers have sensible defaults.
	DNSTokenEnvVar string
}

// Load reads configuration from environment variables
func Load() *Config {
	cfg := &Config{
		CaddyfilePath:    getEnv("WEBPORT_CADDYFILE_PATH", "/etc/caddy/webport.d/Caddyfile"),
		BaseDomain:       getEnv("WEBPORT_BASE_DOMAIN", ""),
		ListenHost:       getEnv("WEBPORT_LISTEN_HOST", "127.0.0.1"),
		Port:             getEnvInt("WEBPORT_PORT", 8080),
		DefaultTTL:       getEnvDuration("WEBPORT_DEFAULT_TTL", 300*time.Second),
		CaddyReloadCmd:   getEnv("WEBPORT_CADDY_RELOAD_CMD", "caddy reload --config /etc/caddy/Caddyfile"),
		TTLCheckInterval: getEnvDuration("WEBPORT_TTL_CHECK_INTERVAL", 30*time.Second),
		ShutdownTimeout:  getEnvDuration("WEBPORT_SHUTDOWN_TIMEOUT", 5*time.Second),
		TLS: TLSConfig{
			DNSProvider:       getEnv("WEBPORT_TLS_DNS_PROVIDER", ""),
			DNSProviderModule: getEnv("WEBPORT_TLS_DNS_PROVIDER_MODULE", ""),
			DNSTokenEnvVar:    getEnv("WEBPORT_TLS_DNS_TOKEN_ENV_VAR", ""),
		},
	}

	if cfg.BaseDomain == "" {
		panic("WEBPORT_BASE_DOMAIN is required")
	}

	return cfg
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}

// GetListenAddr returns the address string for the HTTP server
func (c *Config) GetListenAddr() string {
	return net.JoinHostPort(c.ListenHost, strconv.Itoa(c.Port))
}
