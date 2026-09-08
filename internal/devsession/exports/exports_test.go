package exports

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/env"
)

func TestExportsRenderEscapedShellFishAndJSONValuesAtomically(t *testing.T) {
	secret := "sensitive ' value"
	values, err := env.Resolve(env.Input{Top: map[string]config.Value{
		"PLAIN":  {Literal: stringPtr("hello world")},
		"SECRET": {Literal: &secret, Sensitive: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := map[string]string{"bash": filepath.Join(dir, "env"), "fish": filepath.Join(dir, "env.fish"), "json": filepath.Join(dir, "env.json")}
	store, err := New(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(paths, values); err != nil {
		t.Fatal(err)
	}
	bash, _ := os.ReadFile(paths["bash"])
	if !strings.Contains(string(bash), `export SECRET='sensitive '\'' value'`) {
		t.Fatalf("bash = %q", bash)
	}
	fish, _ := os.ReadFile(paths["fish"])
	if !strings.Contains(string(fish), "set -gx PLAIN 'hello world'") {
		t.Fatalf("fish = %q", fish)
	}
	jsonValue, _ := os.ReadFile(paths["json"])
	if !strings.Contains(string(jsonValue), `"SECRET": "sensitive ' value"`) {
		t.Fatalf("json = %q", jsonValue)
	}
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("export mode %s = %v, %v", path, info, statErr)
		}
	}
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("export remains: %s", path)
		}
	}
}

func TestNewRejectsUnknownFormats(t *testing.T) {
	if _, err := New(map[string]string{"powershell": "/tmp/env"}); err == nil {
		t.Fatal("New() error = nil")
	}
}

func stringPtr(value string) *string { return &value }
