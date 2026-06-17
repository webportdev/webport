package caddy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Writer interface for writing Caddyfile and reloading Caddy
type Writer interface {
	Write(content string) error
	GetPath() string
}

// FileWriter writes Caddyfile and reloads Caddy atomically
type FileWriter struct {
	caddyfilePath  string
	reloadCmd      string
}

// NewFileWriter creates a new Caddyfile writer that actually writes to disk
func NewFileWriter(caddyfilePath, reloadCmd string) Writer {
	return &FileWriter{
		caddyfilePath: caddyfilePath,
		reloadCmd:     reloadCmd,
	}
}

// Write atomically writes the Caddyfile and reloads Caddy
func (w *FileWriter) Write(content string) error {
	// Ensure directory exists
	dir := filepath.Dir(w.caddyfilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Write to temp file in same directory (atomic rename requirement)
	tmpPath := w.caddyfilePath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, w.caddyfilePath); err != nil {
		os.Remove(tmpPath) // cleanup
		return fmt.Errorf("rename file: %w", err)
	}

	// Reload Caddy
	return w.reload()
}

// reload executes the caddy reload command
func (w *FileWriter) reload() error {
	// Split command into args
	parts := strings.Fields(w.reloadCmd)
	if len(parts) == 0 {
		return fmt.Errorf("empty reload command")
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("caddy reload failed: %w, output: %s", err, output)
	}

	return nil
}

// GetPath returns the Caddyfile path
func (w *FileWriter) GetPath() string {
	return w.caddyfilePath
}

// NewWriter creates a new Caddyfile writer (alias for NewFileWriter for backward compatibility)
func NewWriter(caddyfilePath, reloadCmd string) Writer {
	return NewFileWriter(caddyfilePath, reloadCmd)
}
