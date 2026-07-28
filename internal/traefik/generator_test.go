package traefik

import (
	"strings"
	"testing"
)

func TestGenerateDynamicConfig(t *testing.T) {
	config, err := GenerateDynamicConfig([]RouteInfo{
		{Domain: "z.example.com", Port: 4000},
		{Domain: "a.example.com", Port: 3000},
	}, Config{EntryPoint: "https", CertResolver: "resolver"}, "example.com")
	if err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		`"a.example.com":`,
		`rule: "Host(` + "`a.example.com`" + `)"`,
		`entryPoints: ["https"]`,
		`url: "http://127.0.0.1:3000"`,
		`resolver: "resolver"`,
		`main: "*.example.com"`,
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("generated configuration does not contain %q:\n%s", expected, config)
		}
	}
	if strings.Index(config, `"a.example.com":`) > strings.Index(config, `"z.example.com":`) {
		t.Error("routes are not sorted")
	}
}

func TestGenerateDynamicConfigEmpty(t *testing.T) {
	config, err := GenerateDynamicConfig(nil, Config{EntryPoint: "websecure", CertResolver: "webport"}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "http:") || strings.Contains(config, "routers:") || strings.Contains(config, "services:") {
		t.Fatalf("empty configuration should omit the HTTP section:\n%s", config)
	}
	if !strings.Contains(config, `main: "*.example.com"`) {
		t.Fatalf("empty configuration must retain wildcard TLS configuration:\n%s", config)
	}
}

func TestGenerateDynamicConfigIPv6Backend(t *testing.T) {
	config, err := GenerateDynamicConfig([]RouteInfo{{
		Domain: "app.example.com", Host: "::1", Port: 5173,
	}}, Config{EntryPoint: "https", CertResolver: "resolver"}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, `url: "http://[::1]:5173"`) {
		t.Fatalf("generated configuration does not contain an IPv6 backend:\n%s", config)
	}
}

func TestGenerateDynamicConfigLocalCertificate(t *testing.T) {
	config, err := GenerateDynamicConfig(nil, Config{
		EntryPoint: "websecure",
		CertFile:   "/etc/traefik/dynamic/webport-pki/wildcard.crt",
		KeyFile:    "/etc/traefik/dynamic/webport-pki/wildcard.key",
	}, "webport.localhost")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`certFile: "/etc/traefik/dynamic/webport-pki/wildcard.crt"`,
		`keyFile: "/etc/traefik/dynamic/webport-pki/wildcard.key"`,
		"defaultCertificate:",
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("generated local-CA configuration does not contain %q:\n%s", expected, config)
		}
	}
	if strings.Contains(config, "resolver:") || strings.Contains(config, "defaultGeneratedCert:") {
		t.Fatalf("local-CA configuration references ACME:\n%s", config)
	}
}

func TestGenerateDynamicConfigValidation(t *testing.T) {
	tests := []struct {
		name       string
		baseDomain string
		config     Config
	}{
		{"base domain", "", Config{EntryPoint: "websecure", CertResolver: "webport"}},
		{"entrypoint", "example.com", Config{CertResolver: "webport"}},
		{"resolver", "example.com", Config{EntryPoint: "websecure"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := GenerateDynamicConfig(nil, test.config, test.baseDomain); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := GenerateDynamicConfig(nil, Config{
		EntryPoint: "websecure",
		CertFile:   "/tmp/cert.pem",
	}, "example.com"); err == nil {
		t.Fatal("expected incomplete local certificate validation error")
	}
}
