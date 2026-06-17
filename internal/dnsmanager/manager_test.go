package dnsmanager

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

type fakeProvider struct {
	zones   map[string][]libdns.Record
	deleted []libdns.Record
	added   []libdns.Record
}

func (f *fakeProvider) GetRecords(_ context.Context, zone string) ([]libdns.Record, error) {
	records, ok := f.zones[zone]
	if !ok {
		return nil, fmt.Errorf("zone not found")
	}
	return records, nil
}

func (f *fakeProvider) AppendRecords(_ context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	f.added = append(f.added, records...)
	f.zones[zone] = append(f.zones[zone], records...)
	return records, nil
}

func (f *fakeProvider) DeleteRecords(_ context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	f.deleted = append(f.deleted, records...)
	f.zones[zone] = nil
	return records, nil
}

func TestDiscoverZone(t *testing.T) {
	manager := New(&fakeProvider{zones: map[string][]libdns.Record{"example.com.": {}}})
	zone, err := manager.DiscoverZone(context.Background(), "dev.example.com", "")
	if err != nil || zone != "example.com." {
		t.Fatalf("DiscoverZone() = %q, %v", zone, err)
	}
}

func TestSyncReplacesWildcardAddresses(t *testing.T) {
	provider := &fakeProvider{zones: map[string][]libdns.Record{
		"example.com.": {
			libdns.Address{Name: "*.dev", IP: mustAddr("192.0.2.1"), TTL: time.Minute},
			libdns.RR{Name: "unrelated", Type: "TXT", Data: "keep"},
		},
	}}
	status, err := New(provider).Sync(context.Background(), "dev.example.com", "", "198.51.100.2", "2001:db8::2", DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	if status.Zone != "example.com." || len(status.Records) != 2 || len(provider.deleted) != 1 {
		t.Fatalf("unexpected sync result: %+v deleted=%d", status, len(provider.deleted))
	}
	if got := status.Records[0].RR().Name; got != "*.dev" {
		t.Fatalf("record name = %q, want *.dev", got)
	}
}

func TestSyncValidation(t *testing.T) {
	manager := New(&fakeProvider{})
	if _, err := manager.Sync(context.Background(), "example.com", "", "invalid", "", DefaultTTL); err == nil {
		t.Fatal("expected invalid IPv4 error")
	}
	if _, err := manager.Sync(context.Background(), "example.com", "", "", "", DefaultTTL); err == nil {
		t.Fatal("expected missing address error")
	}
}

func TestSyncWithoutIPv6RemovesExistingAAAA(t *testing.T) {
	provider := &fakeProvider{zones: map[string][]libdns.Record{
		"example.com.": {
			libdns.Address{Name: "*", IP: mustAddr("2001:db8::1"), TTL: time.Minute},
		},
	}}
	status, err := New(provider).Sync(context.Background(), "example.com", "example.com", "192.0.2.5", "", DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.deleted) != 1 || len(status.Records) != 1 || status.Records[0].RR().Type != "A" {
		t.Fatalf("unexpected IPv4-only result: %+v deleted=%d", status, len(provider.deleted))
	}
}

func mustAddr(value string) (addr netip.Addr) {
	addr = netip.MustParseAddr(value)
	return
}
