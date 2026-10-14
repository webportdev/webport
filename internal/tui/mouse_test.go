package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/webportdev/webport/internal/devsession/session"
)

func press(m Model, x, y int) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	return updated.(Model), cmd
}

// screenPosition finds text on the rendered screen so tests follow the layout.
func screenPosition(t *testing.T, m Model, text string) (int, int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q not on screen", text)
	return 0, 0
}

func TestEnvironmentClickSelectsAndDoubleClickCopiesValue(t *testing.T) {
	m, b := fixtureModel()
	clock := time.Unix(0, 0)
	m.now = func() time.Time { return clock }
	var copied []string
	m.copy = func(value string) (string, error) { copied = append(copied, value); return "copied", nil }
	m.instanceID, m.service, m.page = "a", "server", envPage
	m.inspection.Environment = map[string]map[string]string{"server": {"A": "one", "B": "••••"}}
	b.inspection = session.LiveInspection{Environment: map[string]map[string]string{"server": {"A": "one", "B": "secret"}}}

	m, cmd := press(m, 4, listTop+1)
	if m.cursor != 1 || cmd != nil {
		t.Fatalf("single click: cursor %d, command %v", m.cursor, cmd != nil)
	}
	clock = clock.Add(doubleClickInterval + time.Millisecond)
	m, cmd = press(m, 4, listTop+1)
	if cmd != nil {
		t.Fatal("slow second click acted as a double click")
	}
	clock = clock.Add(100 * time.Millisecond)
	m, cmd = press(m, 4, listTop+1)
	if cmd == nil {
		t.Fatal("double click did not copy")
	}
	cmd()
	if len(copied) != 1 || copied[0] != "secret" || m.inspection.Environment["server"]["B"] != "••••" {
		t.Fatalf("copied %v without revealing", copied)
	}
	clock = clock.Add(100 * time.Millisecond)
	if _, cmd = press(m, 4, listTop+1); cmd != nil {
		t.Fatal("third click repeated the double click")
	}
	if _, cmd = press(m, 4, listTop+5); cmd != nil {
		t.Fatal("click below the rows acted")
	}
}

func TestDoubleClickOpensInstance(t *testing.T) {
	m, _ := fixtureModel()
	m, _ = press(m, 4, listTop)
	m, cmd := press(m, 4, listTop)
	if m.page != servicesPage || m.instanceID != "a" || cmd == nil {
		t.Fatalf("page %d, instance %q", m.page, m.instanceID)
	}
}

func TestClickingRowKeepsListScrollPosition(t *testing.T) {
	m, _ := fixtureModel()
	m.page = logsPage
	for i := range 40 {
		m.logLines = append(m.logLines, fmt.Sprint("line ", i))
	}
	m.cursor = 39
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	start := m.listStart()
	m, _ = press(m, 4, listTop)
	if m.cursor != start || m.listStart() != start {
		t.Fatalf("cursor %d, start %d → %d", m.cursor, start, m.listStart())
	}
	if !strings.Contains(m.View(), fmt.Sprint("› line ", start)) {
		t.Fatal("rows moved under the pointer")
	}
}

func TestClickingTabsSearchAndHints(t *testing.T) {
	m, _ := fixtureModel()
	x, y := screenPosition(t, m, "2 Routes")
	m, _ = press(m, x, y)
	if m.page != routesPage {
		t.Fatalf("tab click opened page %d", m.page)
	}
	x, y = screenPosition(t, m, "/ Search")
	m, _ = press(m, x, y)
	if !m.searching {
		t.Fatal("search click did not start search")
	}
	m, _ = key(m, "esc")
	x, y = screenPosition(t, m, "? Help")
	m, _ = press(m, x+3, y)
	if !m.help {
		t.Fatal("footer click did not open help")
	}
	x, y = screenPosition(t, m, "Esc Close")
	m, _ = press(m, x, y)
	if m.help {
		t.Fatal("overlay click did not close help")
	}
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonBackward, Action: tea.MouseActionPress})
	if updated.(Model).page != instancesPage {
		t.Fatal("back button did not return to instances")
	}
}

func TestMouseConfirmationRequiresConfirmTarget(t *testing.T) {
	m, b := fixtureModel()
	m, _ = key(m, "x")
	m, cmd := press(m, 4, listTop+1)
	if cmd != nil || m.cursor != 0 || m.confirm == nil {
		t.Fatal("row click changed a pending action")
	}
	x, y := screenPosition(t, m, "y / Enter Confirm")
	m, cmd = press(m, x, y)
	if cmd == nil || m.confirm != nil {
		t.Fatal("confirm click did not run the action")
	}
	cmd()
	if len(b.actions) != 1 || b.actions[0] != "a:stop" {
		t.Fatalf("actions: %v", b.actions)
	}
}
