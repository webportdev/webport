package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCLIUsesProvidedStreamsForSimpleCommands(t *testing.T) {
	var out bytes.Buffer
	if err := runCLI([]string{"version"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("version output is empty")
	}

	out.Reset()
	if err := runCLI([]string{"help"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "webport dev") {
		t.Fatalf("help output = %q", out.String())
	}
}

func TestRunCLIReportsUnknownCommandWithoutExiting(t *testing.T) {
	var out bytes.Buffer
	err := runCLI([]string{"unknown"}, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), `unknown command "unknown"`) {
		t.Fatalf("runCLI() error = %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("unknown command output = %q", out.String())
	}
}
