package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogTailBoundsAndRejectsNonRegularFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("old output\n", 10000)+"latest\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lines, err := readLogTail(path)
	if err != nil || len(lines) != logTailLines || lines[len(lines)-1] != "latest" {
		t.Fatalf("tail lines = %d, %v", len(lines), err)
	}
	if _, err := readLogTail(filepath.Dir(path)); err == nil {
		t.Fatal("accepted directory")
	}
}
