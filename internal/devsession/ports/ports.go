// Package ports allocates and validates named session ports before startup.
package ports

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sort"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/primitives"
)

const defaultAttempts = 16

type Allocation struct {
	Name       string `json:"name"`
	Owner      string `json:"owner,omitempty"`
	Port       int    `json:"port,omitempty"`
	Discovered bool   `json:"discovered,omitempty"`
}

type Options struct {
	Host         string
	MaxAttempts  int
	PortProber   primitives.PortProber
	RandomSource io.Reader
}

// Resolve computes a unique port allocation in deterministic name order. A
// port is checked immediately before it is returned; the socket is then
// released because the child owns the listener.
func Resolve(ctx context.Context, specs map[string]config.Port, owners map[string]string, options Options) (map[string]Allocation, error) {
	if options.Host == "" {
		options.Host = "127.0.0.1"
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = defaultAttempts
	}
	if options.PortProber == nil {
		options.PortProber = TCPProber{}
	}
	if options.RandomSource == nil {
		options.RandomSource = rand.Reader
	}
	result := make(map[string]Allocation, len(specs))
	reserved := make(map[int]string)
	for name, spec := range specs {
		mode, err := modeOf(spec)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", name, err)
		}
		if mode == "fixed" {
			if err := validatePort(*spec.Fixed); err != nil {
				return nil, fmt.Errorf("port %q: %w", name, err)
			}
			if previous, exists := reserved[*spec.Fixed]; exists {
				return nil, fmt.Errorf("ports %q and %q both request fixed port %d", previous, name, *spec.Fixed)
			}
			reserved[*spec.Fixed] = name
		}
	}

	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := specs[name]
		mode, _ := modeOf(spec)
		allocation := Allocation{Name: name, Owner: owners[name], Discovered: mode == "discover"}
		if allocation.Discovered {
			result[name] = allocation
			continue
		}
		var port int
		var err error
		switch mode {
		case "fixed":
			port = *spec.Fixed
			if err = probe(ctx, options.PortProber, options.Host, port); err != nil {
				err = fmt.Errorf("fixed port %d is unavailable: %w", port, err)
			}
		case "first_free":
			port, err = firstFree(ctx, options.PortProber, options.Host, *spec.FirstFree, reserved)
		case "random":
			port, err = randomFree(ctx, options.PortProber, options.Host, spec.Random[0], spec.Random[1], reserved, options.RandomSource, options.MaxAttempts)
		}
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", name, err)
		}
		allocation.Port = port
		reserved[port] = name
		result[name] = allocation
	}
	return result, nil
}

// Reallocate rechecks the selected names immediately before their owning
// services start. Dynamic allocations keep their port when it is still free;
// if another process won the race, a new first-free or random port is chosen.
// Fixed allocations fail with the original availability error.
func Reallocate(ctx context.Context, specs map[string]config.Port, current map[string]Allocation, names []string, options Options) (map[string]Allocation, error) {
	if options.Host == "" {
		options.Host = "127.0.0.1"
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = defaultAttempts
	}
	if options.PortProber == nil {
		options.PortProber = TCPProber{}
	}
	if options.RandomSource == nil {
		options.RandomSource = rand.Reader
	}
	result := make(map[string]Allocation, len(current))
	for name, allocation := range current {
		result[name] = allocation
	}
	selected := make(map[string]struct{}, len(names))
	for _, name := range names {
		selected[name] = struct{}{}
	}
	reserved := make(map[int]string)
	for name, allocation := range result {
		if _, changing := selected[name]; changing {
			continue
		}
		if allocation.Port > 0 {
			reserved[allocation.Port] = name
		}
	}
	ordered := append([]string(nil), names...)
	sort.Strings(ordered)
	for _, name := range ordered {
		spec, ok := specs[name]
		if !ok {
			return nil, fmt.Errorf("port %q is not configured", name)
		}
		mode, err := modeOf(spec)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", name, err)
		}
		allocation := result[name]
		allocation.Name = name
		if mode == "discover" {
			allocation.Discovered = true
			allocation.Port = 0
			result[name] = allocation
			continue
		}
		if allocation.Port > 0 {
			if _, taken := reserved[allocation.Port]; !taken && probe(ctx, options.PortProber, options.Host, allocation.Port) == nil {
				reserved[allocation.Port] = name
				result[name] = allocation
				continue
			}
		}
		var port int
		switch mode {
		case "fixed":
			port = *spec.Fixed
			if err := probe(ctx, options.PortProber, options.Host, port); err != nil {
				return nil, fmt.Errorf("port %q: fixed port %d is unavailable: %w", name, port, err)
			}
		case "first_free":
			port, err = firstFree(ctx, options.PortProber, options.Host, *spec.FirstFree, reserved)
		case "random":
			port, err = randomFree(ctx, options.PortProber, options.Host, spec.Random[0], spec.Random[1], reserved, options.RandomSource, options.MaxAttempts)
		}
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", name, err)
		}
		allocation.Port, allocation.Discovered = port, false
		reserved[port] = name
		result[name] = allocation
	}
	return result, nil
}

func ValidatePreStartReferences(specs map[string]config.Port, references map[string]string) error {
	for reference, portName := range references {
		spec, ok := specs[portName]
		if !ok {
			return fmt.Errorf("%s references unknown port %q", reference, portName)
		}
		mode, _ := modeOf(spec)
		if mode == "discover" {
			return fmt.Errorf("%s references discovered port %q before its owner reveals it", reference, portName)
		}
	}
	return nil
}

type TCPProber struct{}

func (TCPProber) Probe(ctx context.Context, host string, port int) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return err
	}
	return listener.Close()
}

func modeOf(spec config.Port) (string, error) {
	count := 0
	mode := ""
	if spec.Fixed != nil {
		count++
		mode = "fixed"
	}
	if spec.FirstFree != nil {
		count++
		mode = "first_free"
	}
	if len(spec.Random) > 0 {
		count++
		mode = "random"
	}
	if spec.Discover {
		count++
		mode = "discover"
	}
	if count != 1 {
		return "", errors.New("exactly one allocation mode is required")
	}
	if mode == "random" && len(spec.Random) != 2 {
		return "", errors.New("random must contain exactly two bounds")
	}
	if mode == "random" && (spec.Random[0] > spec.Random[1] || validatePort(spec.Random[0]) != nil || validatePort(spec.Random[1]) != nil) {
		return "", errors.New("random bounds must be between 1 and 65535 and ordered")
	}
	if mode == "first_free" && validatePort(*spec.FirstFree) != nil {
		return "", errors.New("first_free must be between 1 and 65535")
	}
	return mode, nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d must be between 1 and 65535", port)
	}
	return nil
}

func probe(ctx context.Context, prober primitives.PortProber, host string, port int) error {
	if err := prober.Probe(ctx, host, port); err != nil {
		return err
	}
	return nil
}

func firstFree(ctx context.Context, prober primitives.PortProber, host string, start int, reserved map[int]string) (int, error) {
	for port := start; port <= 65535; port++ {
		if _, exists := reserved[port]; exists {
			continue
		}
		if err := probe(ctx, prober, host, port); err == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port at or above %d", start)
}

func randomFree(ctx context.Context, prober primitives.PortProber, host string, lower, upper int, reserved map[int]string, randomSource io.Reader, attempts int) (int, error) {
	for attempt := 0; attempt < attempts; attempt++ {
		port, err := randomPort(lower, upper, randomSource)
		if err != nil {
			return 0, err
		}
		if _, exists := reserved[port]; exists {
			continue
		}
		if err := probe(ctx, prober, host, port); err == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free random port after %d attempts in %d-%d", attempts, lower, upper)
}

func randomPort(lower, upper int, randomSource io.Reader) (int, error) {
	value, err := rand.Int(randomSource, big.NewInt(int64(upper-lower+1)))
	if err != nil {
		return 0, fmt.Errorf("select random port: %w", err)
	}
	return lower + int(value.Int64()), nil
}
