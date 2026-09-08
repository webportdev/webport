// Package exports renders the active resolved environment for other tools.
package exports

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/webportdev/webport/internal/devsession/env"
)

type Store struct {
	paths []string
}

func New(paths map[string]string) (Store, error) {
	result := make([]string, 0, len(paths))
	for shell, path := range paths {
		if shell != "bash" && shell != "posix" && shell != "zsh" && shell != "fish" && shell != "json" {
			return Store{}, fmt.Errorf("unsupported export format %q", shell)
		}
		if strings.TrimSpace(path) == "" {
			return Store{}, fmt.Errorf("export path for %s is empty", shell)
		}
		result = append(result, path)
	}
	return Store{paths: result}, nil
}

func (s Store) Write(paths map[string]string, values env.Values) error {
	for shell, path := range paths {
		content, err := render(shell, values)
		if err != nil {
			return err
		}
		if err := writeAtomic(path, []byte(content)); err != nil {
			return fmt.Errorf("write %s export %s: %w", shell, path, err)
		}
	}
	return nil
}

func (s Store) Remove() error {
	var combined error
	for _, path := range s.paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			combined = errors.Join(combined, fmt.Errorf("remove export %s: %w", path, err))
		}
	}
	return combined
}

func render(shell string, values env.Values) (string, error) {
	mapValues := values.Map(true)
	keys := make([]string, 0, len(mapValues))
	for name := range mapValues {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var output bytes.Buffer
	switch shell {
	case "json":
		encoded, err := json.MarshalIndent(mapValues, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded) + "\n", nil
	case "bash", "posix", "zsh":
		for _, name := range keys {
			fmt.Fprintf(&output, "export %s=%s\n", name, quote(mapValues[name]))
		}
	case "fish":
		for _, name := range keys {
			fmt.Fprintf(&output, "set -gx %s %s\n", name, quote(mapValues[name]))
		}
	default:
		return "", fmt.Errorf("unsupported export format %q", shell)
	}
	return output.String(), nil
}

func writeAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".webport-export-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
