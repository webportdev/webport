package route

import (
	"errors"
	"testing"
	"time"
)

func TestControllerOverlappingLeases(t *testing.T) {
	store := NewStore()
	controller := NewController(store, func() error { return nil })
	now := time.Now()
	req := RegisterRequest{Project: "app", Branch: "main", Port: 3000}

	first, _, _, err := controller.CreateLease("client-one", req, "example.com", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := controller.CreateLease("client-two", req, "example.com", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if store.Count() != 1 {
		t.Fatalf("routes = %d, want 1", store.Count())
	}
	if err := controller.DeleteLease(first.LeaseID, now, "example.com"); err != nil {
		t.Fatal(err)
	}
	if store.Count() != 1 {
		t.Fatal("releasing one of two leases removed the route")
	}
	if err := controller.DeleteLease(second.LeaseID, now, "example.com"); err != nil {
		t.Fatal(err)
	}
	if store.Count() != 0 {
		t.Fatal("last lease release did not remove the route")
	}
}

func TestControllerRejectsPortAndHostnameConflicts(t *testing.T) {
	controller := NewController(NewStore(), func() error { return nil })
	now := time.Now()
	_, _, _, err := controller.CreateLease("one", RegisterRequest{
		Project: "app", Branch: "feature/auth", Port: 3000,
	}, "example.com", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = controller.CreateLease("two", RegisterRequest{
		Project: "app", Branch: "feature-auth", Port: 3001,
	}, "example.com", now, 30*time.Second)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("hostname collision error = %v, want ErrConflict", err)
	}
	_, _, _, err = controller.CreateLease("three", RegisterRequest{
		Project: "app", Branch: "feature/auth", Port: 4000,
	}, "example.com", now, 30*time.Second)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("port collision error = %v, want ErrConflict", err)
	}
}

func TestControllerRollsBackFailedPublication(t *testing.T) {
	store := NewStore()
	controller := NewController(store, func() error { return errors.New("disk full") })
	_, _, _, err := controller.CreateLease("client", RegisterRequest{
		Project: "app", Branch: "main", Port: 3000,
	}, "example.com", time.Now(), 30*time.Second)
	if err == nil {
		t.Fatal("expected publication error")
	}
	if store.Count() != 0 {
		t.Fatal("failed publication left route committed")
	}
	if controller.Ready() {
		t.Fatal("controller reports ready after publication error")
	}
}

func TestControllerLegacyHeartbeatPreservesRegisteredTTL(t *testing.T) {
	controller := NewController(NewStore(), func() error { return nil })
	now := time.Now()
	_, err := controller.RegisterLegacy(RegisterRequest{
		Project: "app", Branch: "main", Port: 3000,
	}, "example.com", now, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	route, err := controller.HeartbeatLegacy(
		RouteID{Project: "app", Branch: "main"}, 0, 30*time.Second,
		now.Add(10*time.Second), "example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(55 * time.Second)
	if !route.ExpiresAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v", route.ExpiresAt, want)
	}
}

func TestControllerExpiresUncleanLease(t *testing.T) {
	store := NewStore()
	controller := NewController(store, func() error { return nil })
	now := time.Now()
	_, _, _, err := controller.CreateLease("client", RegisterRequest{
		Project: "app", Branch: "main", Port: 3000,
	}, "example.com", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	count, err := controller.Expire(now.Add(31*time.Second), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || store.Count() != 0 {
		t.Fatalf("expired = %d, routes = %d", count, store.Count())
	}
}
