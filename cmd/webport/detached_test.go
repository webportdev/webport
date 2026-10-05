package main

import (
	"reflect"
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
