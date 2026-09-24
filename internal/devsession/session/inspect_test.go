package session

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/plan"
	"github.com/webportdev/webport/internal/devsession/state"
)

func TestInspectLiveFallsBackToOlderSessionControl(t *testing.T) {
	options := sessionFixture(t, "profiles: {default: {services: [frontend, backend]}}\nservices: {frontend: {command: ['true']}, backend: {command: ['true']}}\n")
	root := filepath.Dir(options.ConfigPath)
	store, err := state.NewStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	var rejectInspect atomic.Bool
	control, err := state.StartControl(strings.TrimSuffix(store.LivePath, ".live.json")+".sock", func(_ context.Context, request state.Request) state.Response {
		switch request.Operation {
		case "inspect":
			if rejectInspect.Load() {
				return state.Response{Status: 403, Error: "access denied"}
			}
			return state.Response{Status: 404, Error: "unknown control operation"}
		case "config":
			if request.Payload["structured"] != true {
				return state.Response{Error: "structured plan required"}
			}
			secret := "actual-secret"
			frontend := map[string]config.Value{
				"FRONTEND_ONLY": {Literal: stringPointer("frontend")},
				"SECRET":        {Literal: &secret, Sensitive: true},
			}
			if request.Payload["include_inherited"] == true {
				frontend["INHERITED"] = config.Value{Literal: stringPointer("inherited")}
			}
			return state.Response{OK: true, Payload: map[string]any{"plan": plan.Plan{
				Profile: "default", Services: map[string]plan.Service{
					"frontend": {Env: frontend},
					"backend":  {Env: map[string]config.Value{"BACKEND_ONLY": {Literal: stringPointer("backend")}}},
				},
			}}}
		default:
			return state.Response{Status: 404, Error: "unknown control operation"}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := store.WriteLive(state.LiveState{
		Worktree: root, Profile: "default", ControlPath: control.Path(), ControlToken: control.Token(),
		Routes: map[string]state.RouteState{
			"frontend": {State: "active", Project: "sample", Branch: "feature/test", URL: "https://sample-feature-test.example"},
			"backend":  {State: "pending", Project: "sample-backend", Branch: "feature/test", URL: "https://pending.example"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	value, err := InspectLive(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if value.Project != "sample" || len(value.Routes) != 1 || value.Routes[0].Service != "frontend" {
		t.Fatalf("older session routes = %+v", value)
	}
	if value.Environment["frontend"]["SECRET"] != "<redacted>" || value.Environment["frontend"]["FRONTEND_ONLY"] != "frontend" || value.Environment["backend"]["BACKEND_ONLY"] != "backend" {
		t.Fatalf("older session environment = %+v", value.Environment)
	}
	if _, inherited := value.Environment["frontend"]["INHERITED"]; inherited {
		t.Fatal("older session included inherited environment by default")
	}
	options.ShowSensitive, options.IncludeInherited = true, true
	visible, err := InspectLive(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if visible.Environment["frontend"]["SECRET"] != "actual-secret" || visible.Environment["frontend"]["INHERITED"] != "inherited" {
		t.Fatalf("older session explicit environment = %+v", visible.Environment)
	}
	rejectInspect.Store(true)
	_, err = InspectLive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("unexpected control error was hidden: %v", err)
	}
}

func stringPointer(value string) *string { return &value }
