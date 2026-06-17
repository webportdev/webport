package caddy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewWriter(t *testing.T) {
	writer := NewWriter("/tmp/test/Caddyfile", "caddy reload")

	if writer.GetPath() != "/tmp/test/Caddyfile" {
		t.Errorf("GetPath() = %v, want /tmp/test/Caddyfile", writer.GetPath())
	}
}

func TestWriteCreatesDirectory(t *testing.T) {
	// Create a temp directory
	tmpDir := t.TempDir()
	caddyfilePath := filepath.Join(tmpDir, "subdir", "Caddyfile")
	writer := NewWriter(caddyfilePath, "echo reload")

	content := "# Test Caddyfile"
	err := writer.Write(content)

	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Verify the directory was created
	if _, err := os.Stat(filepath.Dir(caddyfilePath)); os.IsNotExist(err) {
		t.Error("Directory was not created")
	}

	// Verify the file was created
	data, err := os.ReadFile(caddyfilePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(data) != content {
		t.Errorf("File content = %v, want %v", string(data), content)
	}
}

func TestWriteOverwritesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	caddyfilePath := filepath.Join(tmpDir, "Caddyfile")
	writer := NewWriter(caddyfilePath, "echo reload")

	// Write initial content
	initialContent := "# Initial content"
	if err := writer.Write(initialContent); err != nil {
		t.Fatalf("First Write() error = %v", err)
	}

	// Write new content
	newContent := "# New content"
	if err := writer.Write(newContent); err != nil {
		t.Fatalf("Second Write() error = %v", err)
	}

	// Verify the file has the new content
	data, err := os.ReadFile(caddyfilePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(data) != newContent {
		t.Errorf("File content = %v, want %v", string(data), newContent)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	caddyfilePath := filepath.Join(tmpDir, "Caddyfile")
	writer := NewWriter(caddyfilePath, "echo reload")

	// Write initial content
	initialContent := "# Initial"
	if err := writer.Write(initialContent); err != nil {
		t.Fatalf("First Write() error = %v", err)
	}

	// Create a temporary file with .tmp suffix to check it's cleaned up
	tmpPath := caddyfilePath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte("temp"), 0644); err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// Write new content - should clean up the .tmp file
	newContent := "# New"
	if err := writer.Write(newContent); err != nil {
		t.Fatalf("Second Write() error = %v", err)
	}

	// Verify the .tmp file was cleaned up
	if _, err := os.Stat(tmpPath); err == nil {
		t.Error(".tmp file still exists after Write()")
	}

	// Verify the main file has the new content
	data, err := os.ReadFile(caddyfilePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(data) != newContent {
		t.Errorf("File content = %v, want %v", string(data), newContent)
	}
}

func TestWriteExecutesReloadCommand(t *testing.T) {
	tmpDir := t.TempDir()
	caddyfilePath := filepath.Join(tmpDir, "Caddyfile")

	// Create a marker file that our reload command will touch
	markerFile := filepath.Join(tmpDir, "reloaded")
	reloadCmd := "touch " + markerFile

	writer := NewWriter(caddyfilePath, reloadCmd)

	content := "# Test"
	if err := writer.Write(content); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Check that the marker file was created (reload was executed)
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Error("Reload command was not executed")
	}
}

func TestWriteWithBadReloadCommand(t *testing.T) {
	tmpDir := t.TempDir()
	caddyfilePath := filepath.Join(tmpDir, "Caddyfile")

	// Use a non-existent command
	writer := NewWriter(caddyfilePath, "this-command-does-not-exist")

	content := "# Test"
	err := writer.Write(content)

	// Should fail due to reload command failure
	if err == nil {
		t.Error("Expected error from failed reload command")
	}

	// But the file should still be written
	data, err := os.ReadFile(caddyfilePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(data) != content {
		t.Errorf("File content = %v, want %v", string(data), content)
	}
}
