package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// Load reads and strictly validates configuration from environment variables.
func Load() (*Config, error) {
	dynamicConfigPath := getEnv("WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH", "/etc/traefik/dynamic/webport.yml")
	port, portErr := getEnvInt("WEBPORT_PORT", 8080)
	defaultTTL, ttlErr := getEnvDuration("WEBPORT_DEFAULT_TTL", 30*time.Second)
	checkInterval, checkErr := getEnvDuration("WEBPORT_TTL_CHECK_INTERVAL", 10*time.Second)
	shutdownTimeout, shutdownErr := getEnvDuration("WEBPORT_SHUTDOWN_TIMEOUT", 5*time.Second)
	discoveryEnabled, discoveryErr := getEnvBool("WEBPORT_DISCOVERY_ENABLED", false)
	discoveryInterval, discoveryIntervalErr := getEnvDuration("WEBPORT_DISCOVERY_INTERVAL", 2*time.Second)
	cfg := &Config{
		TraefikDynamicConfigPath: dynamicConfigPath,
		TraefikEntryPoint:        getEnv("WEBPORT_TRAEFIK_ENTRYPOINT", "websecure"),
		TraefikCertResolver:      getEnv("WEBPORT_TRAEFIK_CERT_RESOLVER", "webport"),
		TLSMode:                  getEnv("WEBPORT_TLS_MODE", TLSModeACME),
		LocalCADir:               getEnv("WEBPORT_LOCAL_CA_DIR", filepath.Join(filepath.Dir(dynamicConfigPath), "webport-pki")),
		BaseDomain:               getEnv("WEBPORT_BASE_DOMAIN", ""),
		ListenHost:               getEnv("WEBPORT_LISTEN_HOST", "127.0.0.1"),
		Port:                     port,
		DefaultTTL:               defaultTTL,
		TTLCheckInterval:         checkInterval,
		ShutdownTimeout:          shutdownTimeout,
		DiscoveryEnabled:         discoveryEnabled,
		DiscoveryInterval:        discoveryInterval,
	}

	var errs []error
	errs = appendError(errs, portErr, ttlErr, checkErr, shutdownErr, discoveryErr, discoveryIntervalErr)
	if cfg.BaseDomain == "" {
		errs = append(errs, errors.New("WEBPORT_BASE_DOMAIN is required"))
	} else if err := validateDomain(cfg.BaseDomain); err != nil {
		errs = append(errs, fmt.Errorf("WEBPORT_BASE_DOMAIN: %w", err))
	}
	if cfg.TLSMode != TLSModeACME && cfg.TLSMode != TLSModeLocalCA {
		errs = append(errs, errors.New("WEBPORT_TLS_MODE must be acme or local-ca"))
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		errs = append(errs, errors.New("WEBPORT_PORT must be between 1 and 65535"))
	}
	if cfg.DefaultTTL <= 0 {
		errs = append(errs, errors.New("WEBPORT_DEFAULT_TTL must be positive"))
	}
	if cfg.TTLCheckInterval <= 0 {
		errs = append(errs, errors.New("WEBPORT_TTL_CHECK_INTERVAL must be positive"))
	}
	if cfg.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("WEBPORT_SHUTDOWN_TIMEOUT must be positive"))
	}
	if cfg.DiscoveryInterval <= 0 {
		errs = append(errs, errors.New("WEBPORT_DISCOVERY_INTERVAL must be positive"))
	}
	if cfg.TraefikDynamicConfigPath == "" {
		errs = append(errs, errors.New("WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH must not be empty"))
	}
	if cfg.TraefikEntryPoint == "" {
		errs = append(errs, errors.New("WEBPORT_TRAEFIK_ENTRYPOINT must not be empty"))
	}
	if cfg.TLSMode == TLSModeACME && cfg.TraefikCertResolver == "" {
		errs = append(errs, errors.New("WEBPORT_TRAEFIK_CERT_RESOLVER is required in acme mode"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

func appendError(errs []error, items ...error) []error {
	for _, err := range items {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func getEnvBool(key string, defaultVal bool) (bool, error) {
	val, ok := os.LookupEnv(key)
	if !ok {
		return defaultVal, nil
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) (int, error) {
	if val := os.Getenv(key); val != "" {
		i, err := strconv.Atoi(val)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return i, nil
	}
	return defaultVal, nil
}

func getEnvDuration(key string, defaultVal time.Duration) (time.Duration, error) {
	if val := os.Getenv(key); val != "" {
		d, err := time.ParseDuration(val)
		if err != nil {
			return 0, fmt.Errorf("%s must be a duration such as 30s or 2m", key)
		}
		return d, nil
	}
	return defaultVal, nil
}

// GetListenAddr returns the address string for the HTTP server
func (c *Config) GetListenAddr() string {
	return net.JoinHostPort(c.ListenHost, strconv.Itoa(c.Port))
}

func validateDomain(value string) error {
	value = strings.TrimSuffix(strings.ToLower(value), ".")
	if value == "" || len(value) > 253 {
		return errors.New("must be a valid DNS name")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("must be a valid DNS name")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return errors.New("must contain only DNS letters, digits, dots, and dashes")
			}
		}
	}
	return nil
}
