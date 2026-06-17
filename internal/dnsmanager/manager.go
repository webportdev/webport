package dnsmanager

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/libdns/libdns"
)

const DefaultTTL = 300 * time.Second

type Provider interface {
	libdns.RecordGetter
	libdns.RecordAppender
	libdns.RecordDeleter
}

type Manager struct {
	provider Provider
}

type Status struct {
	Zone    string
	Records []libdns.Record
}

func New(provider Provider) *Manager {
	return &Manager{provider: provider}
}

func (m *Manager) DiscoverZone(ctx context.Context, baseDomain, override string) (string, error) {
	if override != "" {
		base := strings.Trim(baseDomain, ".")
		zoneName := strings.Trim(override, ".")
		if base != zoneName && !strings.HasSuffix(base, "."+zoneName) {
			return "", fmt.Errorf("base domain %s is not within DNS zone %s", baseDomain, override)
		}
		if _, err := m.provider.GetRecords(ctx, fqdn(override)); err != nil {
			return "", fmt.Errorf("access DNS zone %s: %w", override, err)
		}
		return fqdn(override), nil
	}

	labels := strings.Split(strings.Trim(baseDomain, "."), ".")
	var lastErr error
	for i := 0; i < len(labels)-1; i++ {
		zone := fqdn(strings.Join(labels[i:], "."))
		if _, err := m.provider.GetRecords(ctx, zone); err == nil {
			return zone, nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("no accessible DNS zone found for %s; use --zone to specify it: %w", baseDomain, lastErr)
	}
	return "", fmt.Errorf("no accessible DNS zone found for %s; use --zone to specify it", baseDomain)
}

func (m *Manager) Status(ctx context.Context, baseDomain, zoneOverride string) (Status, error) {
	zone, err := m.DiscoverZone(ctx, baseDomain, zoneOverride)
	if err != nil {
		return Status{}, err
	}
	records, err := m.provider.GetRecords(ctx, zone)
	if err != nil {
		return Status{}, fmt.Errorf("list DNS records: %w", err)
	}
	return Status{Zone: zone, Records: wildcardAddresses(records, baseDomain, zone)}, nil
}

func (m *Manager) Sync(ctx context.Context, baseDomain, zoneOverride, ipv4, ipv6 string, ttl time.Duration) (Status, error) {
	if ipv4 == "" {
		return Status{}, fmt.Errorf("IPv4 address is required")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	addresses, err := parseAddresses(baseDomain, ipv4, ipv6, ttl)
	if err != nil {
		return Status{}, err
	}
	zone, err := m.DiscoverZone(ctx, baseDomain, zoneOverride)
	if err != nil {
		return Status{}, err
	}

	records, err := m.provider.GetRecords(ctx, zone)
	if err != nil {
		return Status{}, fmt.Errorf("list DNS records: %w", err)
	}
	existing := wildcardAddresses(records, baseDomain, zone)
	if len(existing) > 0 {
		if _, err := m.provider.DeleteRecords(ctx, zone, existing); err != nil {
			return Status{}, fmt.Errorf("delete existing wildcard address records: %w", err)
		}
	}

	relativeName := libdns.RelativeName("*."+strings.Trim(baseDomain, "."), zone)
	toCreate := make([]libdns.Record, 0, len(addresses))
	for _, address := range addresses {
		address.Name = relativeName
		toCreate = append(toCreate, address)
	}
	created, err := m.provider.AppendRecords(ctx, zone, toCreate)
	if err != nil {
		if len(existing) > 0 {
			if _, restoreErr := m.provider.AppendRecords(ctx, zone, existing); restoreErr != nil {
				return Status{}, fmt.Errorf("create wildcard address records: %w; restoring previous records also failed: %v", err, restoreErr)
			}
		}
		return Status{}, fmt.Errorf("create wildcard address records: %w", err)
	}
	return Status{Zone: zone, Records: created}, nil
}

func parseAddresses(baseDomain, ipv4, ipv6 string, ttl time.Duration) ([]libdns.Address, error) {
	name := "*." + strings.Trim(baseDomain, ".")
	var addresses []libdns.Address
	if ipv4 != "" {
		ip, err := netip.ParseAddr(ipv4)
		if err != nil || !ip.Is4() {
			return nil, fmt.Errorf("invalid IPv4 address %q", ipv4)
		}
		addresses = append(addresses, libdns.Address{Name: name, IP: ip, TTL: ttl})
	}
	if ipv6 != "" {
		ip, err := netip.ParseAddr(ipv6)
		if err != nil || !ip.Is6() {
			return nil, fmt.Errorf("invalid IPv6 address %q", ipv6)
		}
		addresses = append(addresses, libdns.Address{Name: name, IP: ip, TTL: ttl})
	}
	return addresses, nil
}

func wildcardAddresses(records []libdns.Record, baseDomain, zone string) []libdns.Record {
	wildcard := "*." + strings.Trim(baseDomain, ".") + "."
	var matches []libdns.Record
	for _, record := range records {
		rr := record.RR()
		if (rr.Type == "A" || rr.Type == "AAAA") && libdns.AbsoluteName(rr.Name, zone) == wildcard {
			matches = append(matches, record)
		}
	}
	return matches
}

func fqdn(name string) string {
	return strings.Trim(name, ".") + "."
}
