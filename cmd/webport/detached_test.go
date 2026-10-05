package main

import (
	"bytes"
	"github.com/webportdev/webport/internal/devsession/state"
	"reflect"
	"strings"
	"testing"
)

func TestDetachedArgsPreservesCommandFlags(t *testing.T) {
	got, err := detachedArgs([]string{"-d", "--profile", "local", "--", "server", "-d"})
	want := []string{"dev", "--profile", "local", "--", "server", "-d"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, %v", got, err)
	}
	for _, operation := range []string{"stop", "restart", "logs", "exec"} {
		if _, err := detachedArgs([]string{"-d", operation}); err == nil {
			t.Fatalf("accepted %s", operation)
		}
	}
}

func TestDetachedSummaryPrintsOnlyOwnActiveServiceAndEndpointURLs(t *testing.T) {
	instances := []state.LiveState{
		{PIDs: []state.ProcessIdentity{{PID: 11}}, Routes: map[string]state.RouteState{"other": {State: "active", URL: "https://other.test"}}},
		{PIDs: []state.ProcessIdentity{{PID: 22}, {PID: 11}},
			Routes: map[string]state.RouteState{
				"web":     {State: "active", URL: "https://web.test"},
				"api":     {State: "active", URL: "https://api.test"},
				"pending": {State: "pending", URL: "https://pending.test"},
			},
			Endpoints:    map[string]string{"api.health": "http://127.0.0.1:3000/health", "empty": ""},
			ControlToken: "private-token",
		},
	}
	var output bytes.Buffer
	if err := writeDetachedSummary(&output, 22, "/tmp/dev.log", instances); err != nil {
		t.Fatal(err)
	}
	want := "development session detached (PID 22); log: /tmp/dev.log\napi: https://api.test\napi.health: http://127.0.0.1:3000/health\nweb: https://web.test\n"
	if output.String() != want {
		t.Fatalf("summary = %q", output.String())
	}
}

func TestDetachedSummaryPrintsWrapperURL(t *testing.T) {
	instances := []state.LiveState{{PIDs: []state.ProcessIdentity{{PID: 33}}, Routes: map[string]state.RouteState{"command": {State: "active", URL: "https://wrapper.test"}}}}
	var output bytes.Buffer
	if err := writeDetachedSummary(&output, 33, "/tmp/wrapper.log", instances); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "command: https://wrapper.test\n") {
		t.Fatalf("summary = %q", output.String())
	}
}

func TestDetachedSummarySupportsSessionWithoutURLs(t *testing.T) {
	var output bytes.Buffer
	if err := writeDetachedSummary(&output, 44, "/tmp/no-routes.log", nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != "development session detached (PID 44); log: /tmp/no-routes.log\n" {
		t.Fatalf("summary = %q", output.String())
	}
}
