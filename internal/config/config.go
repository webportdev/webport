package config

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	TLSModeACME    = "acme"
	TLSModeLocalCA = "local-ca"
)

// Config holds all service configuration
type Config struct {
	// Traefik dynamic configuration output path
	TraefikDynamicConfigPath string
	// Traefik HTTPS entrypoint referenced by generated routers
	TraefikEntryPoint string
	// Traefik ACME certificate resolver used for the wildcard certificate
	TraefikCertResolver string
	// TLSMode selects public ACME or a private local certificate authority.
	TLSMode string
	// LocalCADir stores the private CA and wildcard certificate.
	LocalCADir string
	// Base domain for routes (e.g., "mond.boo")
	BaseDomain string
	// HTTP API listen host
	ListenHost string
	// HTTP API port
	Port int
	// Default TTL for new routes
	DefaultTTL time.Duration
	// TTL check interval
	TTLCheckInterval time.Duration
	// Shutdown timeout for graceful shutdown
	ShutdownTimeout time.Duration
	// Enable daemon-side discovery of processes carrying WEBPORT_ROUTE.
	DiscoveryEnabled bool
	// Process discovery reconciliation interval.
	DiscoveryInterval time.Duration
}

// Load reads configuration from environment variables
func Load() *Config {
	dynamicConfigPath := getEnv("WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH", "/etc/traefik/dynamic/webport.yml")
	cfg := &Config{
		TraefikDynamicConfigPath: dynamicConfigPath,
		TraefikEntryPoint:        getEnv("WEBPORT_TRAEFIK_ENTRYPOINT", "websecure"),
		TraefikCertResolver:      getEnv("WEBPORT_TRAEFIK_CERT_RESOLVER", "webport"),
		TLSMode:                  getEnv("WEBPORT_TLS_MODE", TLSModeACME),
		LocalCADir:               getEnv("WEBPORT_LOCAL_CA_DIR", filepath.Join(filepath.Dir(dynamicConfigPath), "webport-pki")),
		BaseDomain:               getEnv("WEBPORT_BASE_DOMAIN", ""),
		ListenHost:               getEnv("WEBPORT_LISTEN_HOST", "127.0.0.1"),
		Port:                     getEnvInt("WEBPORT_PORT", 8080),
		DefaultTTL:               getEnvDuration("WEBPORT_DEFAULT_TTL", 300*time.Second),
		TTLCheckInterval:         getEnvDuration("WEBPORT_TTL_CHECK_INTERVAL", 30*time.Second),
		ShutdownTimeout:          getEnvDuration("WEBPORT_SHUTDOWN_TIMEOUT", 5*time.Second),
		DiscoveryEnabled:         getEnvBool("WEBPORT_DISCOVERY_ENABLED", true),
		DiscoveryInterval:        getEnvDuration("WEBPORT_DISCOVERY_INTERVAL", 2*time.Second),
	}

	if cfg.BaseDomain == "" {
		panic("WEBPORT_BASE_DOMAIN is required")
	}
	if cfg.TLSMode != TLSModeACME && cfg.TLSMode != TLSModeLocalCA {
		panic("WEBPORT_TLS_MODE must be acme or local-ca")
	}

	return cfg
}

func getEnvBool(key string, defaultVal bool) bool {
	val, ok := os.LookupEnv(key)
	if !ok {
		return defaultVal
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return defaultVal
	}
	return parsed
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
