package discovery

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func TestSelectListenerFindsHTTPAmongMultiplePorts(t *testing.T) {
	httpListener := listener{host: "127.0.0.1", port: 5173}
	rawListener := listener{host: "127.0.0.1", port: 9229}
	proc := process{
		pid:       42,
		env:       map[string]string{routeEnv: "app:main"},
		listeners: []listener{rawListener, httpListener},
	}

	got, err := selectListenerWithProbe(proc, func(candidate listener) bool {
		return candidate == httpListener
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != httpListener {
		t.Fatalf("selected listener = %#v, want %#v", got, httpListener)
	}
}

func TestSelectListenerRequiresOverrideForMultipleHTTPPorts(t *testing.T) {
	proc := process{
		pid: 42,
		env: map[string]string{routeEnv: "app:main"},
		listeners: []listener{
			{host: "127.0.0.1", port: 3000},
			{host: "127.0.0.1", port: 5173},
		},
	}

	if _, err := selectListenerWithProbe(proc, func(listener) bool { return true }); err == nil {
		t.Fatal("expected an ambiguity error")
	}
}

func TestSelectListenerValidatesExplicitPortOwnership(t *testing.T) {
	target := listener{host: "127.0.0.1", port: 5173}
	proc := process{
		pid:       42,
		env:       map[string]string{routeEnv: "app:main", portEnv: fmt.Sprint(target.port)},
		listeners: []listener{target},
	}

	got, err := selectListenerWithProbe(proc, func(listener) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("selected listener = %#v, want %#v", got, target)
	}

	proc.env[portEnv] = "65534"
	if _, err := selectListenerWithProbe(proc, func(listener) bool { return false }); err == nil {
		t.Fatal("expected ownership validation error")
	}
}

func TestParseLSOFListeners(t *testing.T) {
	raw := []byte("p42\x00\nf12\x00tIPv4\x00n*:5173\x00\n" +
		"f13\x00tIPv6\x00n[::1]:4173\x00\n" +
		"p99\x00\nf8\x00tIPv4\x00n192.0.2.10:8080\x00\n")
	got := parseLSOFListeners(raw)
	if len(got[42]) != 2 {
		t.Fatalf("pid 42 listeners = %#v", got[42])
	}
	if got[42][0] != (listener{host: "127.0.0.1", port: 5173}) {
		t.Errorf("IPv4 wildcard = %#v", got[42][0])
	}
	if got[42][1] != (listener{host: "::1", port: 4173}) {
		t.Errorf("IPv6 loopback = %#v", got[42][1])
	}
	if len(got[99]) != 0 {
		t.Errorf("non-loopback listener was accepted: %#v", got[99])
	}
}

func TestParseDarwinProcArgs(t *testing.T) {
	raw := make([]byte, 4)
	binary.NativeEndian.PutUint32(raw, 2)
	raw = append(raw, []byte("/usr/local/bin/node\x00\x00node\x00server.js\x00")...)
	raw = append(raw, []byte("PATH=/usr/bin\x00WEBPORT_ROUTE=app:main\x00WEBPORT_APP_PORT=5173\x00\x00")...)

	got, err := parseDarwinProcArgs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got[routeEnv] != "app:main" || got[portEnv] != "5173" {
		t.Fatalf("environment = %#v", got)
	}
}

func TestParseDarwinProcArgsRejectsTruncation(t *testing.T) {
	if _, err := parseDarwinProcArgs([]byte{1, 0, 0}); err == nil {
		t.Fatal("expected truncated response error")
	}
}
