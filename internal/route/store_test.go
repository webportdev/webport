package route

import (
	"sync"
	"testing"
	"time"
)

func TestStore_AddAndGet(t *testing.T) {
	store := NewStore()
	route := Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      3000,
		Domain:    "myapp-main.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	err := store.Add(route)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	id := RouteID{Project: "myapp", Branch: "main"}
	got, ok := store.Get(id)
	if !ok {
		t.Fatal("Route not found")
	}

	if got.Project != route.Project {
		t.Errorf("Project = %v, want %v", got.Project, route.Project)
	}
	if got.Port != route.Port {
		t.Errorf("Port = %v, want %v", got.Port, route.Port)
	}
}

func TestStore_AddPreservesCreatedAt(t *testing.T) {
	store := NewStore()
	originalTime := time.Now().Add(-time.Hour)

	route1 := Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      3000,
		Domain:    "myapp-main.example.com",
		CreatedAt: originalTime,
		ExpiresAt: time.Now().Add(time.Hour),
	}

	store.Add(route1)

	// Update with zero CreatedAt
	route2 := Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      4000, // Changed port
		Domain:    "myapp-main.example.com",
		CreatedAt: time.Time{}, // Zero
		ExpiresAt: time.Now().Add(time.Hour),
	}

	store.Add(route2)

	id := RouteID{Project: "myapp", Branch: "main"}
	got, _ := store.Get(id)

	if !got.CreatedAt.Equal(originalTime) {
		t.Errorf("CreatedAt not preserved, got %v, want %v", got.CreatedAt, originalTime)
	}
	if got.Port != 4000 {
		t.Errorf("Port not updated, got %v, want 4000", got.Port)
	}
}

func TestStore_AddSetsCreatedAtForNew(t *testing.T) {
	store := NewStore()

	route := Route{
		Project: "myapp",
		Branch:  "main",
		Port:    3000,
		Domain:  "myapp-main.example.com",
		// CreatedAt left as zero
		ExpiresAt: time.Now().Add(time.Hour),
	}

	store.Add(route)

	id := RouteID{Project: "myapp", Branch: "main"}
	got, _ := store.Get(id)

	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set for new route")
	}
}

func TestStore_Delete(t *testing.T) {
	store := NewStore()
	route := Route{
		Project:   "myapp",
		Branch:    "main",
		Port:      3000,
		Domain:    "myapp-main.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	store.Add(route)

	id := RouteID{Project: "myapp", Branch: "main"}
	if !store.Delete(id) {
		t.Error("Delete returned false")
	}

	// Verify it's gone
	if _, ok := store.Get(id); ok {
		t.Error("Route still exists after Delete")
	}

	// Delete again should return false
	if store.Delete(id) {
		t.Error("Second Delete returned true")
	}
}

func TestStore_List(t *testing.T) {
	store := NewStore()

	routes := []Route{
		{
			Project:   "app1",
			Branch:    "main",
			Port:      3000,
			Domain:    "app1-main.example.com",
			ExpiresAt: time.Now().Add(time.Hour),
		},
		{
			Project:   "app2",
			Branch:    "dev",
			Port:      4000,
			Domain:    "app2-dev.example.com",
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}

	for _, r := range routes {
		store.Add(r)
	}

	list := store.List()
	if len(list) != 2 {
		t.Errorf("List returned %d routes, want 2", len(list))
	}
}

func TestStore_DeleteExpired(t *testing.T) {
	store := NewStore()
	now := time.Now()

	// Add expired route
	expiredRoute := Route{
		Project:   "expired",
		Branch:    "main",
		Port:      3000,
		Domain:    "expired-main.example.com",
		ExpiresAt: now.Add(-time.Hour),
	}
	store.Add(expiredRoute)

	// Add active route
	activeRoute := Route{
		Project:   "active",
		Branch:    "main",
		Port:      4000,
		Domain:    "active-main.example.com",
		ExpiresAt: now.Add(time.Hour),
	}
	store.Add(activeRoute)

	expired := store.DeleteExpired(now)

	if len(expired) != 1 {
		t.Errorf("DeleteExpired returned %d routes, want 1", len(expired))
	}
	if expired[0].Project != "expired" {
		t.Errorf("Expired route project = %v, want 'expired'", expired[0].Project)
	}

	// Verify active route still exists
	id := RouteID{Project: "active", Branch: "main"}
	if _, ok := store.Get(id); !ok {
		t.Error("Active route was deleted")
	}
}

func TestStore_DeleteExpiredKeepsDiscoveredRoutes(t *testing.T) {
	store := NewStore()
	store.Add(Route{
		Project: "app", Branch: "main", Port: 3000,
		Domain: "app-main.example.com", Source: SourceProcess,
	})

	if expired := store.DeleteExpired(time.Now()); len(expired) != 0 {
		t.Fatalf("expired %d discovered routes", len(expired))
	}
	if store.Count() != 1 {
		t.Fatal("discovered route was removed by TTL cleanup")
	}
}

func TestStore_ReconcileDiscoveredLifecycle(t *testing.T) {
	store := NewStore()
	first := Route{
		Project: "app", Branch: "main", Host: "127.0.0.1", Port: 5173,
		Domain: "app-main.example.com", Source: SourceProcess, Owner: "pid:10:1",
	}

	changed, conflicts := store.ReconcileDiscovered([]Route{first})
	if !changed || len(conflicts) != 0 {
		t.Fatalf("initial reconcile = changed %v, conflicts %v", changed, conflicts)
	}
	changed, conflicts = store.ReconcileDiscovered([]Route{first})
	if changed || len(conflicts) != 0 {
		t.Fatalf("stable reconcile = changed %v, conflicts %v", changed, conflicts)
	}

	first.Port = 5174
	changed, conflicts = store.ReconcileDiscovered([]Route{first})
	if !changed || len(conflicts) != 0 {
		t.Fatalf("port update = changed %v, conflicts %v", changed, conflicts)
	}
	got, _ := store.Get(RouteID{Project: "app", Branch: "main"})
	if got.Port != 5174 {
		t.Fatalf("port = %d, want 5174", got.Port)
	}

	changed, _ = store.ReconcileDiscovered(nil)
	if !changed || store.Count() != 0 {
		t.Fatal("stale discovered route was not removed")
	}
}

func TestStore_ReconcileDiscoveredDoesNotReplaceManualRoute(t *testing.T) {
	store := NewStore()
	store.Add(Route{
		Project: "app", Branch: "main", Port: 3000,
		Domain: "app-main.example.com", Source: SourceManual,
	})

	changed, conflicts := store.ReconcileDiscovered([]Route{{
		Project: "app", Branch: "main", Port: 5173,
		Domain: "app-main.example.com", Source: SourceProcess, Owner: "pid:10:1",
	}})
	if changed || len(conflicts) != 1 {
		t.Fatalf("reconcile = changed %v, conflicts %v", changed, conflicts)
	}
	got, _ := store.Get(RouteID{Project: "app", Branch: "main"})
	if got.Port != 3000 || got.IsDiscovered() {
		t.Fatalf("manual route was replaced: %#v", got)
	}
}

func TestStore_ReconcileDiscoveredRejectsMultipleClaimants(t *testing.T) {
	store := NewStore()
	base := Route{
		Project: "app", Branch: "main", Domain: "app-main.example.com",
		Source: SourceProcess,
	}
	first := base
	first.Port = 5173
	first.Owner = "pid:10:1"
	second := base
	second.Port = 5174
	second.Owner = "pid:11:1"

	changed, conflicts := store.ReconcileDiscovered([]Route{first, second})
	if changed || len(conflicts) != 1 {
		t.Fatalf("reconcile = changed %v, conflicts %v", changed, conflicts)
	}
	if store.Count() != 0 {
		t.Fatal("ambiguous route should fail closed")
	}
}

func TestStore_ReconcileDiscoveredRejectsDomainSlugCollision(t *testing.T) {
	store := NewStore()
	first := Route{
		Project: "app", Branch: "feature/auth", Port: 5173,
		Domain: "app-feature-auth.example.com", Source: SourceProcess, Owner: "pid:10:1",
	}
	second := Route{
		Project: "app", Branch: "feature-auth", Port: 5174,
		Domain: "app-feature-auth.example.com", Source: SourceProcess, Owner: "pid:11:1",
	}

	changed, conflicts := store.ReconcileDiscovered([]Route{first, second})
	if changed || len(conflicts) != 1 {
		t.Fatalf("reconcile = changed %v, conflicts %v", changed, conflicts)
	}
	if store.Count() != 0 {
		t.Fatal("domain collision should fail closed for both routes")
	}
}

func TestStore_Clear(t *testing.T) {
	store := NewStore()

	routes := []Route{
		{
			Project:   "app1",
			Branch:    "main",
			Port:      3000,
			Domain:    "app1-main.example.com",
			ExpiresAt: time.Now().Add(time.Hour),
		},
		{
			Project:   "app2",
			Branch:    "dev",
			Port:      4000,
			Domain:    "app2-dev.example.com",
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}

	for _, r := range routes {
		store.Add(r)
	}

	cleared := store.Clear()

	if len(cleared) != 2 {
		t.Errorf("Clear returned %d routes, want 2", len(cleared))
	}

	if store.Count() != 0 {
		t.Errorf("Store count after Clear = %d, want 0", store.Count())
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	store := NewStore()
	var wg sync.WaitGroup

	// Concurrent writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			route := Route{
				Project:   "app",
				Branch:    "branch",
				Port:      3000 + i,
				Domain:    "app.example.com",
				ExpiresAt: time.Now().Add(time.Hour),
			}
			store.Add(route)
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.List()
			store.Get(RouteID{Project: "app", Branch: "branch"})
		}()
	}

	wg.Wait()

	// Should have exactly 1 route (all adds were for the same ID)
	if store.Count() != 1 {
		t.Errorf("Store count = %d, want 1", store.Count())
	}
}

func TestRouteID_String(t *testing.T) {
	tests := []struct {
		name     string
		project  string
		branch   string
		expected string
	}{
		{
			name:     "simple branch",
			project:  "myapp",
			branch:   "main",
			expected: "myapp:main",
		},
		{
			name:     "branch with slash",
			project:  "myapp",
			branch:   "feature/auth",
			expected: "myapp:feature/auth",
		},
		{
			name:     "branch with multiple slashes",
			project:  "myapp",
			branch:   "feature/auth/oauth",
			expected: "myapp:feature/auth/oauth",
		},
		{
			name:     "branch with dash",
			project:  "myapp",
			branch:   "feature-auth",
			expected: "myapp:feature-auth",
		},
		{
			name:     "branch with slash and dash",
			project:  "myapp",
			branch:   "feature/auth-oauth",
			expected: "myapp:feature/auth-oauth",
		},
		{
			name:     "project with dash",
			project:  "my-app",
			branch:   "main",
			expected: "my-app:main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := RouteID{Project: tt.project, Branch: tt.branch}
			if got := id.String(); got != tt.expected {
				t.Errorf("String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRouteIDFromString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantID  RouteID
		wantErr bool
	}{
		{
			name:    "simple",
			input:   "myapp:main",
			wantID:  RouteID{Project: "myapp", Branch: "main"},
			wantErr: false,
		},
		{
			name:    "branch with dash",
			input:   "myapp:feature-auth",
			wantID:  RouteID{Project: "myapp", Branch: "feature-auth"},
			wantErr: false,
		},
		{
			name:    "branch with slash",
			input:   "myapp:feature/auth",
			wantID:  RouteID{Project: "myapp", Branch: "feature/auth"},
			wantErr: false,
		},
		{
			name:    "project with dash",
			input:   "my-app:main",
			wantID:  RouteID{Project: "my-app", Branch: "main"},
			wantErr: false,
		},
		{
			name:    "branch with slash and dash",
			input:   "myapp:feature/auth-oauth",
			wantID:  RouteID{Project: "myapp", Branch: "feature/auth-oauth"},
			wantErr: false,
		},
		{
			name:    "invalid - no colon",
			input:   "myapp",
			wantErr: true,
		},
		{
			name:    "invalid - colon at start",
			input:   ":main",
			wantErr: true,
		},
		{
			name:    "invalid - colon at end",
			input:   "myapp:",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RouteIDFromString(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("RouteIDFromString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.wantID {
				t.Errorf("RouteIDFromString() = %v, want %v", got, tt.wantID)
			}
		})
	}
}

func TestValidProjectName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"simple", "myapp", true},
		{"with dash", "my-app", true},
		{"with underscore", "my_app", true},
		{"alphanumeric", "app123", true},
		{"empty", "", false},
		{"with space", "my app", false},
		{"with special char", "my$app", false},
		{"with dot", "my.app", false},
		{"starts with dash", "-myapp", false},
		{"starts with number", "1app", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidProjectName(tt.input); got != tt.want {
				t.Errorf("ValidProjectName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidBranchName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"simple", "main", true},
		{"with slash", "feature/auth", true},
		{"multiple slashes", "feature/auth/oauth", true},
		{"with dash", "feature-auth", true},
		{"with underscore", "feature_auth", true},
		{"empty", "", false},
		{"trailing slash", "feature/", false},
		{"leading slash", "/feature", false},
		{"double slash", "feature//auth", false},
		{"with space", "feature auth", false},
		{"with special char", "feature$auth", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidBranchName(tt.input); got != tt.want {
				t.Errorf("ValidBranchName() = %v, want %v", got, tt.want)
			}
		})
	}
}
