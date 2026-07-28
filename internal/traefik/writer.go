package traefik

import (
	"fmt"
	"os"
	"path/filepath"
)

// Writer publishes Traefik dynamic configuration.
type Writer interface {
	Write(content string) error
	GetPath() string
}

// FileWriter atomically replaces a file-provider configuration file.
type FileWriter struct {
	path string
}

func NewWriter(path string) Writer {
	return &FileWriter{path: path}
}

func (w *FileWriter) Write(content string) error {
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dynamic configuration directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".webport-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary dynamic configuration: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("set temporary configuration permissions: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary dynamic configuration: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary dynamic configuration: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary dynamic configuration: %w", err)
	}
	if err := os.Rename(tmpPath, w.path); err != nil {
		return fmt.Errorf("replace dynamic configuration: %w", err)
	}
	return nil
}

func (w *FileWriter) GetPath() string {
	return w.path
}
