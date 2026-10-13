package tui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyUsesTerminalWithNativeHelpersAvailable(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"pbcopy", "wl-copy", "xclip", "xsel"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")
	for _, test := range []struct {
		name, value, encoded string
	}{
		{"url", "https://app.test", "aHR0cHM6Ly9hcHAudGVzdA=="},
		{"environment", "TOKEN=one\ntwo=✓\x1b\a", "VE9LRU49b25lCnR3bz3inJMbBw=="},
		{"empty", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			message, err := Copy(&output, test.value)
			if err != nil {
				t.Fatal(err)
			}
			if want := "\x1b]52;c;" + test.encoded + "\a"; output.String() != want {
				t.Fatalf("clipboard request = %q, want %q", output.String(), want)
			}
			if message != "Clipboard request sent (terminal must support OSC 52)" {
				t.Fatalf("message = %q", message)
			}
		})
	}
}

type failingClipboardWriter struct{ err error }

func (w failingClipboardWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCopyReportsTerminalWriteFailure(t *testing.T) {
	want := errors.New("terminal closed")
	_, err := Copy(failingClipboardWriter{want}, "https://app.test")
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
