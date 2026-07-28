package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	if err := os.WriteFile(path, []byte("# comment\nTEST_WEBPORT_DNS=value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("TEST_WEBPORT_DNS") })
	if err := loadEnvFile(path, true); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TEST_WEBPORT_DNS"); got != "value" {
		t.Fatalf("environment value = %q", got)
	}
}

func TestUnsupportedProvider(t *testing.T) {
	if _, err := newProvider("custom"); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestCuratedProviders(t *testing.T) {
	t.Setenv("CF_DNS_API_TOKEN", "test-token")
	t.Setenv("DO_AUTH_TOKEN", "test-token")
	for _, provider := range []string{"cloudflare", "digitalocean", "route53"} {
		if _, err := newProvider(provider); err != nil {
			t.Fatalf("newProvider(%q): %v", provider, err)
		}
	}
}
