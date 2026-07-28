//go:build linux

package discovery

import (
	"net"
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
