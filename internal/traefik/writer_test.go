package traefik

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriterCreatesAndReplacesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dynamic", "webport.yml")
	writer := NewWriter(path)
	if writer.GetPath() != path {
		t.Fatalf("GetPath() = %q, want %q", writer.GetPath(), path)
	}
	if err := writer.Write("first"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Write("second"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "second" {
		t.Fatalf("content = %q, want second", content)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0644 {
		t.Fatalf("mode = %o, want 644", info.Mode().Perm())
	}
}
