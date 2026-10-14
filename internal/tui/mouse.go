package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Terminals report individual presses; two on the same row within this
// interval form a double click.
const doubleClickInterval = 500 * time.Millisecond

type click struct {
	page page
	row  string
	at   time.Time
}

func (m Model) mouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress || m.searching {
		return m, nil
	}
	if m.confirm != nil {
		// Only the explicit Confirm / Cancel targets act on a pending action.
		if msg.Button == tea.MouseButtonLeft && msg.Y == m.height-2 {
			return m.hintClick(m.hints(), msg.X)
		}
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		delta := 3
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -3
		}
		if m.help || m.detail != "" {
			m.helpOffset = min(m.overlayLimit(), max(0, m.helpOffset+delta))
		} else {
			m.cursor = min(max(0, len(m.rows())-1), max(0, m.cursor+delta))
		}
		return m, nil
	case tea.MouseButtonBackward:
		return m.update(tea.KeyMsg{Type: tea.KeyEsc})
	case tea.MouseButtonLeft:
	default:
		return m, nil
	}
	if m.width < 48 || m.height < 14 {
		return m, nil
	}
	if m.help || m.detail != "" {
		if msg.Y == m.height-2 {
			return m.hintClick(overlayHints, msg.X)
		}
		return m, nil
	}
	listWidth, _ := m.listWidth()
	switch {
	case msg.Y == 1:
		return m.tabClick(msg.X)
	case msg.Y >= listTop && msg.Y < listTop+m.listCapacity() && msg.X < listWidth:
		return m.rowClick(m.listStart() + msg.Y - listTop)
	case msg.Y == m.height-4:
		m.searching = true
	case msg.Y == m.height-2:
		return m.hintClick(m.hints(), msg.X)
	case msg.Y == m.height-1:
		return m.hintClick(footerHints, msg.X)
	}
	return m, nil
}

func (m Model) rowClick(index int) (tea.Model, tea.Cmd) {
	rows := m.rows()
	if index >= len(rows) {
		return m, nil
	}
	m.cursor = index
	now := m.now()
	previous := m.lastClick
	m.lastClick = click{m.page, rows[index].id, now}
	if previous.page != m.page || previous.row != rows[index].id || now.Sub(previous.at) > doubleClickInterval {
		return m, nil
	}
	// A third press starts a new double click instead of repeating the action.
	m.lastClick = click{}
	var cmd tea.Cmd
	switch m.page {
	case envPage, urlsPage, routesPage:
		cmd = m.copySelected(false)
	default:
		cmd = m.activate()
	}
	return m, cmd
}

func (m Model) tabClick(x int) (tea.Model, tea.Cmd) {
	labels, first := m.tabLabels()
	start := 0
	for i, label := range labels {
		end := start + ansi.StringWidth(label) + 2
		if x >= start && x < end {
			p := first + page(i)
			if p == m.page {
				return m, nil
			}
			cmd := m.openTab(p)
			return m, cmd
		}
		start = end + 2
	}
	return m, nil
}

// hintClick sends the key named by the clicked "key Label" hint segment.
func (m Model) hintClick(hints string, x int) (tea.Model, tea.Cmd) {
	start := 0
	for _, segment := range strings.Split(hints, " · ") {
		end := start + ansi.StringWidth(segment)
		if x >= start && x < end {
			if msg, ok := hintKey(segment); ok {
				return m.update(msg)
			}
			return m, nil
		}
		start = end + ansi.StringWidth(" · ")
	}
	return m, nil
}

func hintKey(segment string) (tea.KeyMsg, bool) {
	fields := strings.Fields(segment)
	if len(fields) == 0 {
		return tea.KeyMsg{}, false
	}
	switch fields[0] {
	case "Enter":
		return tea.KeyMsg{Type: tea.KeyEnter}, true
	case "Esc":
		return tea.KeyMsg{Type: tea.KeyEsc}, true
	case "Tab":
		return tea.KeyMsg{Type: tea.KeyTab}, true
	case "End":
		return tea.KeyMsg{Type: tea.KeyEnd}, true
	case "F5":
		return tea.KeyMsg{Type: tea.KeyF5}, true
	}
	runes := []rune(fields[0])
	if len(runes) != 1 || runes[0] == '[' {
		return tea.KeyMsg{}, false
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: runes}, true
}
