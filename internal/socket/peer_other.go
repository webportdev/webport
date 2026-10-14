//go:build !linux && !darwin

package socket

import (
	"errors"
	"net"
)

// PeerUID is unavailable on this platform, so every connection is refused.
func PeerUID(net.Conn) (int, error) {
	return -1, errors.New("peer credentials are not supported on this platform")
}
