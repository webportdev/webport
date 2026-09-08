package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webportdev/webport/internal/devsession/config"
)

func TestGenerateNamedSetsAndExplicitAlphabet(t *testing.T) {
	value, err := Generate(config.Generate{Length: 32, Sets: []string{"lower", "upper", "digit"}}, bytes.NewReader(bytes.Repeat([]byte{0}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	if len(value) != 32 || strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" {
		t.Fatalf("generated value = %q", value)
	}
	value, err = Generate(config.Generate{Length: 12, Alphabet: "ab"}, bytes.NewReader(bytes.Repeat([]byte{1}, 128)))
	if err != nil || len(value) != 12 || strings.Trim(value, "ab") != "" {
		t.Fatalf("alphabet value = %q, %v", value, err)
	}
}

func TestGenerateRejectsInvalidPolicies(t *testing.T) {
	for _, policy := range []config.Generate{
		{Length: 0, Alphabet: "ab"},
		{Length: 2, Sets: []string{"unknown"}},
		{Length: 2, Sets: []string{"lower"}, Alphabet: "ab"},
		{Length: 2},
	} {
		if _, err := Generate(policy, bytes.NewReader(make([]byte, 100))); err == nil {
			t.Errorf("Generate(%+v) error = nil", policy)
		}
	}
}

func TestProjectValuesAreReusedAndSessionValuesAreNotPersisted(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "state", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]config.Value{
		"PROJECT": {Generate: &config.Generate{Lifetime: "project", Length: 20, Sets: []string{"lower"}}},
		"SESSION": {Generate: &config.Generate{Lifetime: "session", Length: 20, Sets: []string{"upper"}}},
	}
	first, err := ResolveValues(values, store, bytes.NewReader(bytes.Repeat([]byte{1}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveValues(values, store, bytes.NewReader(bytes.Repeat([]byte{2}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	if *first["PROJECT"].Literal != *second["PROJECT"].Literal {
		t.Fatal("project value was not reused")
	}
	if *first["SESSION"].Literal == *second["SESSION"].Literal {
		t.Fatal("session value was persisted or deterministic")
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %04o", info.Mode().Perm())
	}
	if !first["PROJECT"].Sensitive || !first["SESSION"].Sensitive {
		t.Fatal("generated values are not marked sensitive")
	}
}

func TestProjectStoreRejectsUnsafePermissionsAndCleanIsExplicit(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "state", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]config.Value{"PROJECT": {Generate: &config.Generate{Lifetime: "project", Length: 8, Alphabet: "ab"}}}
	if _, err := ResolveValues(values, store, bytes.NewReader(bytes.Repeat([]byte{1}, 100))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveValues(values, store, bytes.NewReader(bytes.Repeat([]byte{1}, 100))); err == nil {
		t.Fatal("ResolveValues() accepted unsafe secret permissions")
	}
	if err := os.Chmod(store.Path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Clean(store); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Fatalf("secret store still exists: %v", err)
	}
}

func TestStoredPolicyMismatchDoesNotRevealValue(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "state", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]config.Value{"PROJECT": {Generate: &config.Generate{Lifetime: "project", Length: 8, Alphabet: "ab"}}}
	if _, err := ResolveValues(values, store, bytes.NewReader(bytes.Repeat([]byte{1}, 100))); err != nil {
		t.Fatal(err)
	}
	values["PROJECT"] = config.Value{Generate: &config.Generate{Lifetime: "project", Length: 9, Alphabet: "ab"}}
	_, err = ResolveValues(values, store, bytes.NewReader(bytes.Repeat([]byte{1}, 100)))
	if err == nil || !strings.Contains(err.Error(), "policy does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}
