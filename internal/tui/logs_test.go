package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/webportdev/webport/internal/devsession/state"
)

func TestLogsReadCapturedServiceStreamsAndReopenRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	if err := os.WriteFile(stdout, []byte("hello\x1b]52;c;unsafe\a\nworld\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderr, []byte("error\n"), 0600); err != nil {
		t.Fatal(err)
	}
	live := state.LiveState{LogPaths: map[string]string{"api.stdout": stdout, "api.stderr": stderr, "worker": "/missing"}}
	backend := LocalBackend{}
	lines, err := backend.Logs(context.Background(), live, "api")
	text := strings.Join(lines, "\n")
	if err != nil || !strings.Contains(text, "hello\nworld") || !strings.Contains(text, "error") || strings.Contains(text, "unsafe") || strings.Contains(text, "missing") {
		t.Fatalf("logs = %q, %v", text, err)
	}
	if err := os.Rename(stdout, stdout+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdout, []byte("replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lines, err = backend.Logs(context.Background(), live, "api")
	if err != nil || !strings.Contains(strings.Join(lines, "\n"), "replacement") {
		t.Fatalf("rotated logs = %v, %v", lines, err)
	}
	if _, err := backend.Logs(context.Background(), live, "disabled"); err == nil {
		t.Fatal("missing logs not reported")
	}
	if err := os.Remove(stderr); err != nil {
		t.Fatal(err)
	}
	lines, _ = backend.Logs(context.Background(), live, "api")
	if !strings.Contains(strings.Join(lines, "\n"), "open log:") {
		t.Fatal("missing file not reported")
	}
}

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

func TestLogsNavigationFollowingAndStaleResponses(t *testing.T) {
	m, _ := fixtureModel()
	m.instanceID, m.page = "a", servicesPage
	m, cmd := key(m, "l")
	if m.page != logsPage || m.service != "server" || cmd == nil {
		t.Fatal("logs did not select service")
	}
	updated, _ := m.Update(cmd())
	m = updated.(Model)
	if len(m.logLines) != 1 || m.logFetching {
		t.Fatal("logs not loaded")
	}
	apply := func(lines []string) {
		updated, _ := m.Update(logsMsg{id: "a", service: "server", generation: m.logGeneration, lines: lines})
		m = updated.(Model)
	}
	apply([]string{"one", "two", "three"})
	if m.cursor != 2 {
		t.Fatal("did not follow new output")
	}
	m.cursor = 0
	apply([]string{"one", "two", "three", "four"})
	if m.cursor != 0 {
		t.Fatal("refresh interrupted scrolling")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(Model)
	apply([]string{"one", "two", "three", "four", "five"})
	if m.cursor != 4 {
		t.Fatal("End did not resume following")
	}
	m.snapshot.Instances[0].Services["worker"] = state.ServiceState{}
	old := logsMsg{id: "a", service: "server", generation: m.logGeneration, lines: []string{"stale"}}
	m, cmd = key(m, "]")
	if m.service != "worker" || len(m.logLines) != 0 || cmd == nil {
		t.Fatal("service switch did not reload logs")
	}
	updated, _ = m.Update(old)
	m = updated.(Model)
	if len(m.logLines) != 0 {
		t.Fatal("stale logs displayed for new service")
	}
	m, _ = key(m, "e")
	if m.page != envPage {
		t.Fatal("environment hotkey changed")
	}
}
