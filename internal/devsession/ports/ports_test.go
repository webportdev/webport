package ports

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/webportdev/webport/internal/devsession/config"
)

type fakeProber struct {
	blocked map[int]bool
	calls   []int
}

func (p *fakeProber) Probe(_ context.Context, _ string, port int) error {
	p.calls = append(p.calls, port)
	if p.blocked[port] {
		return errBlocked{}
	}
	return nil
}

type errBlocked struct{}

func (errBlocked) Error() string { return "occupied" }

func TestResolveFixedFirstFreeRandomAndOwnership(t *testing.T) {
	fixed := 4000
	first := 4000
	random := []int{4010, 4012}
	prober := &fakeProber{blocked: map[int]bool{4001: true}}
	got, err := Resolve(context.Background(), map[string]config.Port{
		"api":      {Fixed: &fixed},
		"frontend": {FirstFree: &first},
		"worker":   {Random: random},
	}, map[string]string{"api": "api", "frontend": "frontend", "worker": "worker"}, Options{PortProber: prober, RandomSource: bytes.NewReader([]byte{0, 0, 0, 0})})
	if err != nil {
		t.Fatal(err)
	}
	if got["api"].Port != 4000 || got["frontend"].Port != 4002 || got["worker"].Port != 4010 {
		t.Fatalf("allocations = %+v", got)
	}
	if got["frontend"].Owner != "frontend" {
		t.Fatalf("owner = %q", got["frontend"].Owner)
	}
}

func TestResolveRejectsConflictsAndInvalidModes(t *testing.T) {
	a, b := 3000, 3000
	_, err := Resolve(context.Background(), map[string]config.Port{"a": {Fixed: &a}, "b": {Fixed: &b}}, nil, Options{PortProber: &fakeProber{}})
	if err == nil || !strings.Contains(err.Error(), "both request fixed") {
		t.Fatalf("fixed conflict = %v", err)
	}
	_, err = Resolve(context.Background(), map[string]config.Port{"bad": {Fixed: &a, Discover: true}}, nil, Options{PortProber: &fakeProber{}})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("invalid mode = %v", err)
	}
}

func TestResolveRandomRetryExhaustionAndDiscover(t *testing.T) {
	random := []int{5000, 5000}
	prober := &fakeProber{blocked: map[int]bool{5000: true}}
	_, err := Resolve(context.Background(), map[string]config.Port{"api": {Random: random}}, nil, Options{PortProber: prober, RandomSource: bytes.NewReader([]byte{0, 0, 0, 0}), MaxAttempts: 2})
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("random exhaustion = %v", err)
	}
	discover := config.Port{Discover: true}
	got, err := Resolve(context.Background(), map[string]config.Port{"adopted": discover}, nil, Options{PortProber: prober})
	if err != nil || !got["adopted"].Discovered || got["adopted"].Port != 0 {
		t.Fatalf("discovered allocation = %+v, %v", got, err)
	}
}

func TestValidatePreStartReferencesRejectsDiscoveredPort(t *testing.T) {
	specs := map[string]config.Port{"runtime": {Discover: true}}
	if err := ValidatePreStartReferences(specs, map[string]string{"route.frontend": "runtime"}); err == nil || !strings.Contains(err.Error(), "before its owner") {
		t.Fatalf("validation error = %v", err)
	}
	if err := ValidatePreStartReferences(specs, map[string]string{"route.frontend": "missing"}); err == nil || !strings.Contains(err.Error(), "unknown port") {
		t.Fatalf("unknown validation error = %v", err)
	}
}
