package primitives

import (
	"context"
	"net"
	"testing"
	"time"
)

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func TestClockContractCanBeImplementedByDeterministicTestClock(t *testing.T) {
	var clock Clock = systemClock{}
	if clock.Now().IsZero() {
		t.Fatal("clock returned zero time")
	}
}

type tcpProber struct{}

func (tcpProber) Probe(ctx context.Context, host string, port int) error {
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "0"))
	if err == nil {
		_ = conn.Close()
	}
	return err
}

func TestPortProberIsAnInjectableBoundary(t *testing.T) {
	var _ PortProber = tcpProber{}
}
