package route

import "testing"

func TestBuildDomain(t *testing.T) {
	if got := BuildDomain("example.com", "myapp", "feature/auth"); got != "myapp-feature-auth.example.com" {
		t.Fatalf("BuildDomain() = %q", got)
	}
}
