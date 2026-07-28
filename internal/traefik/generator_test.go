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
}
