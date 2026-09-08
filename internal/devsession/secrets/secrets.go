// Package secrets handles generated session values and worktree-scoped
// project-lifetime values. It never renders values for diagnostics.
package secrets

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/webportdev/webport/internal/devsession/config"
)

type Resolved struct {
	Value     string
	Sensitive bool
}

type Store struct {
	Path string
}

type record struct {
	Version int               `json:"version"`
	Values  map[string]stored `json:"values"`
}

type stored struct {
	Value    string   `json:"value"`
	Lifetime string   `json:"lifetime"`
	Length   int      `json:"length"`
	Sets     []string `json:"sets,omitempty"`
	Alphabet string   `json:"alphabet,omitempty"`
}

func NewStore(path string) (Store, error) {
	if strings.TrimSpace(path) == "" {
		return Store{}, errors.New("secret store path is required")
	}
	return Store{Path: filepath.Clean(path)}, nil
}

// PreviewValues validates generators without reading or writing secret state.
// A placeholder retains sensitivity, so secret-to-argv checks still apply.
func PreviewValues(values map[string]config.Value) (map[string]config.Value, error) {
	result := make(map[string]config.Value, len(values))
	for name, value := range values {
		if value.Generate != nil {
			if value.Literal != nil {
				return nil, fmt.Errorf("generated value %q: value and generate are mutually exclusive", name)
			}
			if err := validatePolicy(*value.Generate); err != nil {
				return nil, fmt.Errorf("generated value %q: %w", name, err)
			}
			placeholder := "<generated>"
			value = config.Value{Literal: &placeholder, Sensitive: true}
		}
		result[name] = value
	}
	return result, nil
}

// ResolveValues converts generated config values into literals. Session values
// are generated for this call only. Project values are loaded or atomically
// persisted through store and are never returned in diagnostics by this
// package.
func ResolveValues(values map[string]config.Value, store Store, randomSource io.Reader) (map[string]config.Value, error) {
	if _, err := PreviewValues(values); err != nil {
		return nil, err
	}
	result := make(map[string]config.Value, len(values))
	projectPolicies := make(map[string]config.Generate)
	for name, value := range values {
		result[name] = value
		if value.Generate != nil && normalizeLifetime(value.Generate.Lifetime) == "project" {
			projectPolicies[name] = *value.Generate
		}
	}

	existing, err := store.load()
	if err != nil {
		return nil, err
	}
	if len(projectPolicies) > 0 && store.Path == "" {
		return nil, errors.New("project-lifetime values require a secret store")
	}
	changed := false
	for name, value := range values {
		if value.Generate == nil {
			continue
		}
		policy := *value.Generate
		if err := validatePolicy(policy); err != nil {
			return nil, fmt.Errorf("generated value %q: %w", name, err)
		}
		var generated string
		if normalizeLifetime(policy.Lifetime) == "project" {
			entry, ok := existing[name]
			if ok {
				if err := validateStored(entry, policy); err != nil {
					return nil, fmt.Errorf("stored generated value %q is invalid: %w", name, err)
				}
				generated = entry.Value
			} else {
				generated, err = Generate(policy, randomSource)
				if err != nil {
					return nil, fmt.Errorf("generate project value %q: %w", name, err)
				}
				existing[name] = storedFromPolicy(generated, policy)
				changed = true
			}
		} else {
			generated, err = Generate(policy, randomSource)
			if err != nil {
				return nil, fmt.Errorf("generate session value %q: %w", name, err)
			}
		}
		generatedCopy := generated
		result[name] = config.Value{Literal: &generatedCopy, Sensitive: true}
	}
	if changed {
		if err := store.save(existing); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func Generate(policy config.Generate, randomSource io.Reader) (string, error) {
	if err := validatePolicy(policy); err != nil {
		return "", err
	}
	if randomSource == nil {
		randomSource = rand.Reader
	}
	alphabet := policyAlphabet(policy)
	var builder strings.Builder
	for i := 0; i < policy.Length; i++ {
		index, err := rand.Int(randomSource, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("read secure randomness: %w", err)
		}
		builder.WriteByte(alphabet[index.Int64()])
	}
	return builder.String(), nil
}

func Clean(store Store) error {
	if store.Path == "" {
		return errors.New("secret store path is required")
	}
	info, err := os.Lstat(store.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return errors.New("secret store is not a regular file")
	}
	if err := checkUserOnly(store.Path, info, 0o600); err != nil {
		return err
	}
	return os.Remove(store.Path)
}

func validatePolicy(policy config.Generate) error {
	if policy.Length <= 0 {
		return errors.New("length must be positive")
	}
	if policy.Lifetime != "" && policy.Lifetime != "session" && policy.Lifetime != "project" {
		return fmt.Errorf("unsupported lifetime %q", policy.Lifetime)
	}
	if len(policy.Sets) > 0 && policy.Alphabet != "" {
		return errors.New("sets and alphabet are mutually exclusive")
	}
	if len(policy.Sets) == 0 && policy.Alphabet == "" {
		return errors.New("one of sets or alphabet is required")
	}
	if policy.Alphabet != "" && len(policy.Alphabet) < 2 {
		return errors.New("alphabet must contain at least two characters")
	}
	seen := make(map[string]struct{})
	for _, set := range policy.Sets {
		if _, ok := namedSets[set]; !ok {
			return fmt.Errorf("unknown character set %q", set)
		}
		if _, ok := seen[set]; ok {
			return fmt.Errorf("duplicate character set %q", set)
		}
		seen[set] = struct{}{}
	}
	return nil
}

func validateStored(value stored, policy config.Generate) error {
	if value.Lifetime != normalizeLifetime(policy.Lifetime) || value.Length != policy.Length || value.Alphabet != policy.Alphabet || !sameStrings(value.Sets, policy.Sets) {
		return errors.New("stored generation policy does not match configuration")
	}
	alphabet := policyAlphabet(policy)
	if len([]byte(value.Value)) != policy.Length {
		return errors.New("stored value length does not match policy")
	}
	for _, char := range []byte(value.Value) {
		if !strings.ContainsRune(alphabet, rune(char)) {
			return errors.New("stored value contains a character outside its policy")
		}
	}
	return nil
}

func storedFromPolicy(value string, policy config.Generate) stored {
	sets := append([]string(nil), policy.Sets...)
	sort.Strings(sets)
	return stored{Value: value, Lifetime: normalizeLifetime(policy.Lifetime), Length: policy.Length, Sets: sets, Alphabet: policy.Alphabet}
}

func policyAlphabet(policy config.Generate) string {
	if policy.Alphabet != "" {
		return policy.Alphabet
	}
	var builder strings.Builder
	for _, set := range policy.Sets {
		builder.WriteString(namedSets[set])
	}
	return builder.String()
}

func normalizeLifetime(value string) string {
	if value == "" {
		return "session"
	}
	return value
}

func sameStrings(left, right []string) bool {
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	if len(leftCopy) != len(rightCopy) {
		return false
	}
	for i := range leftCopy {
		if leftCopy[i] != rightCopy[i] {
			return false
		}
	}
	return true
}

func (s Store) load() (map[string]stored, error) {
	result := make(map[string]stored)
	if s.Path == "" {
		return result, nil
	}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect secret store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return nil, errors.New("secret store is not a regular file")
	}
	if err := checkUserOnly(s.Path, info, 0o600); err != nil {
		return nil, err
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("open secret store: %w", err)
	}
	defer file.Close()
	var document record
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode secret store: %w", err)
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("unsupported secret store version %d", document.Version)
	}
	for name, value := range document.Values {
		result[name] = value
	}
	return result, nil
}

func (s Store) save(values map[string]stored) error {
	if s.Path == "" {
		return errors.New("secret store path is required")
	}
	directory := filepath.Dir(s.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create secret store directory: %w", err)
	}
	if info, err := os.Stat(directory); err == nil {
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("secret store directory is accessible by group or other users (mode %04o)", info.Mode().Perm())
		}
	}
	document := record{Version: 1, Values: values}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode secret store: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".webport-secrets-*")
	if err != nil {
		return fmt.Errorf("create secret store temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure secret store temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write secret store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync secret store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close secret store: %w", err)
	}
	if err := os.Rename(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("replace secret store: %w", err)
	}
	directoryFile, err := os.Open(directory)
	if err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

var namedSets = map[string]string{
	"lower":     "abcdefghijklmnopqrstuvwxyz",
	"upper":     "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"digit":     "0123456789",
	"hex":       "0123456789abcdef",
	"base64url": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_",
}
