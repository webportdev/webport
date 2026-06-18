package runtime

import (
	"testing"
)

func TestBuildRouteID(t *testing.T) {
	manager := &Manager{cfg: Config{Project: "myapp", Branch: "feature/auth-oauth"}}

	if got := manager.buildRouteID(); got != "myapp:feature/auth-oauth" {
		t.Errorf("buildRouteID() = %v, want myapp:feature/auth-oauth", got)
	}
}

func TestBuildEscapedRouteID(t *testing.T) {
	manager := &Manager{cfg: Config{Project: "myapp", Branch: "feature/auth-oauth"}}

	if got := manager.buildEscapedRouteID(); got != "myapp:feature%2Fauth-oauth" {
		t.Errorf("buildEscapedRouteID() = %v, want myapp:feature%%2Fauth-oauth", got)
	}
}
