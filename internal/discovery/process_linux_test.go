//go:build linux

package discovery

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeProcIP(t *testing.T) {
	if got := decodeProcIP("0100007F", false); !got.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("IPv4 decoded as %v", got)
	}
	if got := decodeProcIP("00000000000000000000000001000000", true); !got.Equal(net.ParseIP("::1")) {
		t.Fatalf("IPv6 decoded as %v", got)
	}
}

func TestReadEnvironmentIncludesClientToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environ")
	data := []byte("PATH=/usr/bin\x00WEBPORT_ROUTE=app:main\x00WEBPORT_APP_PORT=5173\x00WEBPORT_CLIENT_TOKEN=token-123\x00")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	if got[routeEnv] != "app:main" || got[portEnv] != "5173" || got[clientTokenEnv] != "token-123" {
		t.Fatalf("environment = %#v", got)
	}
	if _, ok := got["PATH"]; ok {
		t.Fatalf("unrelated environment variable retained: %#v", got)
	}
}
