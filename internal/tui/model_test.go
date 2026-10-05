package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
)

type fakeBackend struct {
	actions              []string
	inspection           session.LiveInspection
	inspectErr           error
	inspectedID          string
	sensitive, inherited bool
}

func (b *fakeBackend) Snapshot(context.Context) Snapshot { return Snapshot{} }
func (b *fakeBackend) Inspect(_ context.Context, live state.LiveState, sensitive, inherited bool) (session.LiveInspection, error) {
	b.inspectedID, b.sensitive, b.inherited = live.SessionID, sensitive, inherited
	return b.inspection, b.inspectErr
}
func (b *fakeBackend) Control(_ context.Context, live state.LiveState, operation string) error {
	b.actions = append(b.actions, live.SessionID+":"+operation)
	return nil
}
func key(m Model, name string) (Model, tea.Cmd) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
	switch name {
	case "enter":
		msg.Type = tea.KeyEnter
		msg.Runes = nil
	case "esc":
		msg.Type = tea.KeyEsc
		msg.Runes = nil
	case "down":
		msg.Type = tea.KeyDown
		msg.Runes = nil
	}
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}
func fixtureModel() (Model, *fakeBackend) {
	backend := &fakeBackend{}
	m := New(backend, func(string) (string, error) { return "copied", nil })
	m.loading = false
	m.fetching = false
	m.snapshot = Snapshot{Instances: []state.LiveState{
		{SessionID: "a", Worktree: "/projects/alpha", Profile: "default", Services: map[string]state.ServiceState{"server": {State: "ready", Ready: true}}, Routes: map[string]state.RouteState{"server": {State: "active", URL: "https://alpha.test"}}},
		{SessionID: "b", Worktree: "/projects/beta", Profile: "default"},
	}}
	return m, backend
}
func TestActionConfirmationKeepsOriginalTargetAcrossRefresh(t *testing.T) {
	m, b := fixtureModel()
	m, cmd := key(m, "x")
	if cmd != nil || len(b.actions) > 0 {
		t.Fatal("action ran before confirmation")
	}
	m.snapshot.Instances = []state.LiveState{m.snapshot.Instances[1]}
	m, cmd = key(m, "enter")
	if cmd == nil {
		t.Fatal("confirmation did not execute action")
	}
	cmd()
	if len(b.actions) != 1 || b.actions[0] != "a:stop" {
		t.Fatalf("actions: %v", b.actions)
	}
	m, _ = key(m, "r")
	m, _ = key(m, "esc")
	if m.confirm != nil {
		t.Fatal("cancel did not dismiss confirmation")
	}
}
func TestCopyUsesUntruncatedValueWhileKeepingItHidden(t *testing.T) {
	m, backend := fixtureModel()
	m.instanceID = "a"
	m.service = "server"
	m.page = envPage
	copied := ""
	m.copy = func(value string) (string, error) { copied = value; return "copied", nil }
	m.inspection.Environment = map[string]map[string]string{"server": {"SECRET": "<redacted>"}}
	backend.inspection.Environment = map[string]map[string]string{"server": {"SECRET": "multi\nline=value"}}
	m.inherited = true
	m, cmd := key(m, "c")
	if cmd == nil {
		t.Fatal("hidden value cannot be copied")
	}
	result := cmd()
	updated, _ := m.Update(result)
	m = updated.(Model)
	if copied != "multi\nline=value" {
		t.Fatalf("clipboard = %q", copied)
	}
	if m.sensitive || m.inspection.Environment["server"]["SECRET"] != "<redacted>" || strings.Contains(m.View(), "multi") {
		t.Fatal("copy revealed the value")
	}
	if !backend.sensitive || !backend.inherited || backend.inspectedID != "a" {
		t.Fatal("copy inspection used wrong scope")
	}
	_, cmd = key(m, "Y")
	cmd()
	if copied != "SECRET=multi\nline=value" {
		t.Fatalf("clipboard = %q", copied)
	}
	m.page = urlsPage
	m.cursor = 0
	_, cmd = key(m, "c")
	cmd()
	if copied != "https://alpha.test" {
		t.Fatalf("URL = %q", copied)
	}
}
func TestOldInspectionCannotExposePreviouslyRevealedSecrets(t *testing.T) {
	m, _ := fixtureModel()
	m.instanceID = "a"
	m.service = "server"
	m.page = envPage
	m.sensitive = true
	m.generation = 3
	m.inspection.Environment = map[string]map[string]string{"server": {"TOKEN": "secret"}}
	m, cmd := key(m, "v")
	if cmd == nil || m.sensitive || len(m.inspection.Environment) != 0 {
		t.Fatal("hide did not discard sensitive data")
	}
	updated, _ := m.Update(inspectionMsg{id: "a", generation: 3, value: session.LiveInspection{Environment: map[string]map[string]string{"server": {"TOKEN": "secret"}}}})
	if strings.Contains(updated.(Model).View(), "secret") {
		t.Fatal("old response revealed secret")
	}
}
func TestSelectionSurvivesInsertionAndSearchDoesNotRunHotkeys(t *testing.T) {
	m, b := fixtureModel()
	m.cursor = 1
	snapshot := m.snapshot
	snapshot.Instances = append([]state.LiveState{{SessionID: "new", Worktree: "/projects/000"}}, snapshot.Instances...)
	updated, _ := m.Update(snapshotMsg{snapshot, m.snapshotGeneration})
	m = updated.(Model)
	if m.rows()[m.cursor].id != "b" {
		t.Fatal("refresh changed selected instance")
	}
	m, _ = key(m, "/")
	m, _ = key(m, "x")
	if m.confirm != nil || len(b.actions) != 0 || m.filter != "x" {
		t.Fatal("search invoked action")
	}
}
func TestTerminalOutputIsSanitizedAndFitsViewport(t *testing.T) {
	m, _ := fixtureModel()
	m.width = 60
	m.height = 18
	m.snapshot.Instances[0].Worktree = "/projects/\x1b]52;c;bad\a\x1b[31mred\x1b[0m\nnext"
	view := m.View()
	if strings.Contains(view, "52;c") || strings.Contains(view, "\nnext") {
		t.Fatalf("unsafe output %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 58 {
			t.Fatalf("line exceeds viewport: %q", line)
		}
	}
	if strings.Count(view, "\n") > m.height {
		t.Fatal("dashboard exceeds terminal height")
	}
	if !strings.Contains(view, "? Help") {
		t.Fatal("help shortcut hidden")
	}
}

func TestCopyHiddenEnvironmentHandlesMissingEmptyAndLiteralPlaceholder(t *testing.T) {
	m, backend := fixtureModel()
	m.instanceID, m.service, m.page = "a", "server", envPage
	m.inspection.Environment = map[string]map[string]string{"server": {"VALUE": "<redacted>"}}
	copied := false
	var value string
	m.copy = func(v string) (string, error) { copied = true; value = v; return "Copied", nil }
	for _, actual := range []string{"", "<redacted>"} {
		backend.inspection.Environment = map[string]map[string]string{"server": {"VALUE": actual}}
		_, cmd := key(m, "c")
		result := cmd().(resultMsg)
		if result.err != nil || !copied || value != actual {
			t.Fatalf("copy %q: copied=%v error=%v", actual, copied, result.err)
		}
	}
	copied = false
	backend.inspection.Environment = nil
	_, cmd := key(m, "c")
	result := cmd().(resultMsg)
	if result.err == nil || copied {
		t.Fatal("missing entry was copied")
	}
	backend.inspectErr = errors.New("instance stopped")
	_, cmd = key(m, "Y")
	result = cmd().(resultMsg)
	if result.err == nil || copied {
		t.Fatal("inspection failure wrote the clipboard")
	}
}
