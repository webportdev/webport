package tui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
)

type page int

const (
	instancesPage page = iota
	routesPage
	daemonPage
	servicesPage
	urlsPage
	envPage
)

type row struct{ id, label, value string }
type tickMsg time.Time
type snapshotMsg struct {
	Snapshot
	generation int
}
type inspectionMsg struct {
	id         string
	generation int
	value      session.LiveInspection
	err        error
}
type resultMsg struct {
	message string
	err     error
}
type confirmation struct {
	operation string
	instance  state.LiveState
}

type Model struct {
	helpOffset                                           int
	detail                                               string
	fetching                                             bool
	snapshotGeneration                                   int
	backend                                              Backend
	copy                                                 func(string) (string, error)
	snapshot                                             Snapshot
	inspection                                           session.LiveInspection
	page                                                 page
	cursor, width, height                                int
	instanceID, service, filter, message                 string
	searching, help, sensitive, inherited, loading, busy bool
	generation                                           int
	confirm                                              *confirmation
}

func New(backend Backend, copy func(string) (string, error)) Model {
	return Model{backend: backend, copy: copy, width: 80, height: 24, message: "Loading active instances…", loading: true, fetching: true, snapshotGeneration: 1}
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.fetch(), tick()) }
func tick() tea.Cmd           { return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m *Model) refresh() tea.Cmd {
	if m.fetching {
		return nil
	}
	m.fetching = true
	m.snapshotGeneration++
	return m.fetch()
}
func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		return snapshotMsg{m.backend.Snapshot(ctx), m.snapshotGeneration}
	}
}
func (m Model) instance() (state.LiveState, bool) {
	for _, live := range m.snapshot.Instances {
		if live.SessionID == m.instanceID {
			return live, true
		}
	}
	return state.LiveState{}, false
}
func (m *Model) inspect() tea.Cmd {
	live, ok := m.instance()
	if !ok {
		return nil
	}
	m.generation++
	generation := m.generation
	m.loading = true
	sensitive, inherited := m.sensitive, m.inherited
	backend := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		value, err := backend.Inspect(ctx, live, sensitive, inherited)
		return inspectionMsg{live.SessionID, generation, value, err}
	}
}
func (m *Model) navigate(p page) { m.page = p; m.cursor = 0; m.filter = ""; m.searching = false }
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		cmd := m.refresh()
		return m, tea.Batch(cmd, tick())
	case snapshotMsg:
		if msg.generation != m.snapshotGeneration {
			return m, nil
		}
		m.fetching = false
		if m.message == "Loading active instances…" || m.message == "Refreshing…" {
			m.message = ""
		}
		selected := ""
		rows := m.rows()
		if m.cursor < len(rows) {
			selected = rows[m.cursor].id
		}
		m.snapshot = msg.Snapshot
		if m.page <= daemonPage {
			m.loading = false
		}
		rows = m.rows()
		m.cursor = min(m.cursor, max(0, len(rows)-1))
		for i, r := range rows {
			if r.id == selected {
				m.cursor = i
				break
			}
		}
		if m.page >= servicesPage {
			if _, ok := m.instance(); !ok {
				m.navigate(instancesPage)
				m.inspection = session.LiveInspection{}
				m.generation++
				m.loading = false
				m.message = "Instance stopped. Select another instance."
			} else if !m.loading {
				cmd := m.inspect()
				return m, cmd
			}
		}
	case inspectionMsg:
		if msg.id != m.instanceID || msg.generation != m.generation {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.message = "Inspection failed: " + msg.err.Error()
			m.inspection = session.LiveInspection{}
		} else {
			m.inspection = msg.value
			if m.message != "" && strings.HasPrefix(m.message, "Inspection failed:") {
				m.message = ""
			}
		}
		m.cursor = min(m.cursor, max(0, len(m.rows())-1))
	case resultMsg:
		m.busy = false
		if msg.err != nil {
			m.message = "Failed: " + msg.err.Error()
		} else {
			m.message = msg.message
		}
		cmd := m.refresh()
		return m, cmd
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.confirm != nil {
			if key == "esc" || key == "n" {
				m.confirm = nil
				return m, nil
			}
			if key == "y" || key == "enter" {
				action := *m.confirm
				m.confirm = nil
				m.busy = true
				m.message = action.operation + " requested…"
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
					defer cancel()
					err := m.backend.Control(ctx, action.instance, action.operation)
					return resultMsg{action.operation + " requested; waiting for session state", err}
				}
			}
			return m, nil
		}
		if m.searching {
			switch key {
			case "esc":
				m.filter = ""
				m.searching = false
			case "enter":
				m.searching = false
			case "backspace":
				r := []rune(m.filter)
				if len(r) > 0 {
					m.filter = string(r[:len(r)-1])
				}
			default:
				if msg.Type == tea.KeyRunes {
					m.filter += string(msg.Runes)
				}
			}
			m.cursor = 0
			return m, nil
		}
		if m.help || m.detail != "" {
			switch key {
			case "?", "esc", "q", "enter":
				m.help = false
				m.detail = ""
				m.helpOffset = 0
			case "down", "j":
				m.helpOffset = min(m.helpOffset+1, m.overlayLimit())
			case "up", "k":
				m.helpOffset = max(0, m.helpOffset-1)
			case "pgdown":
				m.helpOffset = min(m.helpOffset+max(1, m.height-5), m.overlayLimit())
			case "pgup":
				m.helpOffset = max(0, m.helpOffset-max(1, m.height-5))
			}
			return m, nil
		}
		switch key {
		case "q":
			return m, tea.Quit
		case "?":
			m.help = true
			m.helpOffset = 0
		case "/":
			m.searching = true
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(max(0, len(m.rows())-1), m.cursor+1)
		case "pgup":
			m.cursor = max(0, m.cursor-max(1, m.height-14))
		case "pgdown":
			m.cursor = min(max(0, len(m.rows())-1), m.cursor+max(1, m.height-14))
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = max(0, len(m.rows())-1)
		case "1":
			m.navigate(instancesPage)
		case "2":
			m.navigate(routesPage)
		case "3":
			m.navigate(daemonPage)
		case "esc", "backspace":
			if m.filter != "" {
				m.filter = ""
				m.cursor = 0
			} else if m.page == envPage || m.page == urlsPage {
				m.navigate(servicesPage)
			} else {
				m.navigate(instancesPage)
			}
		case "f5":
			m.message = "Refreshing…"
			cmd := m.refresh()
			return m, cmd
		case "enter":
			rows := m.rows()
			if m.cursor >= len(rows) {
				break
			}
			selected := rows[m.cursor]
			if m.page == instancesPage {
				m.instanceID = selected.id
				m.service = ""
				if names := m.services(); len(names) > 0 {
					m.service = names[0]
				}
				m.inspection = session.LiveInspection{}
				m.navigate(servicesPage)
				cmd := m.inspect()
				return m, cmd
			}
			if m.page == servicesPage {
				m.service = selected.id
				m.navigate(urlsPage)
			} else if m.page == envPage || m.page == urlsPage || m.page == routesPage || m.page == daemonPage {
				m.detail = selected.label
				m.helpOffset = 0
			}
		case "tab", "shift+tab":
			if m.page >= servicesPage {
				p := m.page + 1
				if key == "shift+tab" {
					p = m.page - 1
				}
				if p > envPage {
					p = servicesPage
				}
				if p < servicesPage {
					p = envPage
				}
				m.navigate(p)
			} else {
				p := (int(m.page) + 1) % 3
				if key == "shift+tab" {
					p = (int(m.page) + 2) % 3
				}
				m.navigate(page(p))
			}
		case "e", "u", "s":
			if m.page >= servicesPage {
				if m.page == servicesPage {
					r := m.rows()
					if m.cursor < len(r) {
						m.service = r[m.cursor].id
					}
				}
				p := servicesPage
				if key == "e" {
					p = envPage
				}
				if key == "u" {
					p = urlsPage
				}
				m.navigate(p)
			}
		case "[", "]":
			if m.page == envPage || m.page == urlsPage {
				names := m.services()
				if len(names) > 0 {
					index := 0
					for i, n := range names {
						if n == m.service {
							index = i
						}
					}
					if key == "]" {
						index = (index + 1) % len(names)
					} else {
						index = (index + len(names) - 1) % len(names)
					}
					m.service = names[index]
					m.cursor = 0
					m.filter = ""
				}
			}
		case "v", "i":
			if m.page == envPage {
				if key == "v" {
					m.sensitive = !m.sensitive
				} else {
					m.inherited = !m.inherited
				}
				m.inspection = session.LiveInspection{}
				cmd := m.inspect()
				return m, cmd
			}
		case "r", "x":
			if m.busy {
				break
			}
			live, ok := m.instance()
			if m.page == instancesPage {
				r := m.rows()
				if m.cursor < len(r) {
					for _, v := range m.snapshot.Instances {
						if v.SessionID == r[m.cursor].id {
							live = v
							ok = true
						}
					}
				} else {
					ok = false
				}
			}
			if (m.page == instancesPage || m.page >= servicesPage) && ok {
				operation := "restart"
				if key == "x" {
					operation = "stop"
				}
				m.confirm = &confirmation{operation, live}
			}
		case "c", "y", "Y":
			rows := m.rows()
			if m.cursor >= len(rows) {
				m.message = "Nothing to copy"
				break
			}
			selected := rows[m.cursor]
			if m.page == envPage {
				live, ok := m.instance()
				if !ok {
					m.message = "Instance is no longer active"
					break
				}
				service := m.service
				if service == "" {
					names := m.services()
					if len(names) > 0 {
						service = names[0]
					}
				}
				return m, m.copyEnvironment(live, service, selected.id, key == "Y")
			}
			value := selected.value
			if value == "" {
				m.message = "Select a URL or environment value to copy"
				break
			}
			return m, func() tea.Msg { message, err := m.copy(value); return resultMsg{message, err} }
		}
	}
	return m, nil
}

// Fetch cleartext only for this clipboard operation. It never enters the model
// or an inspection message, so copying does not change display visibility.
func (m Model) copyEnvironment(live state.LiveState, service, name string, entry bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		inspection, err := m.backend.Inspect(ctx, live, true, m.inherited)
		if err != nil {
			return resultMsg{err: err}
		}
		value, ok := inspection.Environment[service][name]
		if !ok {
			return resultMsg{err: fmt.Errorf("environment entry %q is no longer available", name)}
		}
		if entry {
			value = name + "=" + value
		}
		message, err := m.copy(value)
		return resultMsg{message: message, err: err}
	}
}

func (m Model) services() []string {
	live, ok := m.instance()
	if !ok {
		return nil
	}
	names := make([]string, 0, len(live.Services))
	for name := range live.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (m Model) rows() []row {
	var rows []row
	switch m.page {
	case instancesPage:
		for _, live := range m.snapshot.Instances {
			ready := 0
			for _, s := range live.Services {
				if s.Ready {
					ready++
				}
			}
			label := fmt.Sprintf("%s  ·  %s  ·  %d/%d ready", instanceName(live), live.Profile, ready, len(live.Services))
			rows = append(rows, row{live.SessionID, label + "  ·  " + live.Worktree, ""})
		}
	case routesPage:
		for _, r := range m.snapshot.Routes {
			url := "https://" + r.Domain
			rows = append(rows, row{r.Project + ":" + r.Branch, r.Project + ":" + r.Branch + "  ·  " + url + fmt.Sprintf("  → %s:%d", backendHost(r.Host), r.Port), url})
		}
	case daemonPage:
		keys := make([]string, 0, len(m.snapshot.Daemon))
		for name := range m.snapshot.Daemon {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, key := range keys {
			rows = append(rows, row{key, fmt.Sprintf("%s: %v", key, m.snapshot.Daemon[key]), ""})
		}
	case servicesPage:
		live, _ := m.instance()
		for _, name := range m.services() {
			s := live.Services[name]
			rows = append(rows, row{name, fmt.Sprintf("%s  ·  %s  ·  PID %d", name, s.State, s.PID) + "  " + s.LastError, ""})
		}
	case urlsPage:
		live, _ := m.instance()
		for name, r := range live.Routes {
			if m.service != "" && name != m.service {
				continue
			}
			if r.URL != "" {
				rows = append(rows, row{name + ".public", name + "  ·  " + r.State + "  ·  " + r.URL, r.URL})
			}
		}
		for name, url := range live.Endpoints {
			if m.service != "" && !strings.HasPrefix(name, m.service+".") {
				continue
			}
			rows = append(rows, row{name, name + "  ·  " + url, url})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	case envPage:
		service := m.service
		if service == "" {
			names := m.services()
			if len(names) > 0 {
				service = names[0]
			}
		}
		for name, value := range m.inspection.Environment[service] {
			rows = append(rows, row{name, name + " = " + value, value})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	}
	if m.filter != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if strings.Contains(strings.ToLower(clean(r.label)), strings.ToLower(m.filter)) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	return rows
}
func instanceName(live state.LiveState) string {
	names := make([]string, 0, len(live.Routes))
	for name := range live.Routes {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		r := live.Routes[names[0]]
		if r.Project != "" {
			return r.Project + ":" + r.Branch
		}
	}
	return filepath.Base(live.Worktree)
}
func backendHost(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

// Treat process output as text: no escape sequences or control characters can
// change the terminal or trigger clipboard writes when inspecting an env value.
func clean(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

var titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
var selectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("24"))
var mutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

const helpText = `Navigate
↑/↓ or j/k  Select a row       Enter  Open instance/service
Tab / Shift+Tab  Next/previous tab    Esc  Back / clear filter
1 Instances   2 Routes   3 Daemon     / Search this list
PgUp/PgDn / Home/End  Jump through lists

Instance details
s Services   u URLs   e Environment   [ / ] Previous/next service
r Restart instance   x Kill (graceful stop)   y/Enter Confirm
Actions apply to the entire selected instance, including its services.

Copy
c or y  Copy selected URL / env value   Y  Copy NAME=value
Copy works while values stay hidden.
v Reveal/hide sensitive env   i Include/exclude inherited env
Native clipboard tools are used when available; otherwise OSC 52 is
sent to your terminal. Clipboard support depends on the terminal.

F5 Refresh   q / Ctrl+C Quit dashboard (instances keep running)
? / Esc Close help`

func (m Model) overlayLimit() int {
	text := helpText
	if m.detail != "" {
		text = clean(m.detail)
	}
	lines := strings.Split(ansi.Hardwrap(text, max(20, m.width-2), false), "\n")
	return max(0, len(lines)-max(1, m.height-5))
}

func (m Model) View() string {
	width := max(20, m.width-2)
	if m.width < 48 || m.height < 14 {
		return "WEBPORT\nResize to at least 48 × 14.\nq Quit · ? Help\n"
	}
	header := titleStyle.Render("WEBPORT") + "  Development dashboard"
	tabs := "1 Instances   2 Routes   3 Daemon"
	switch m.page {
	case instancesPage:
		tabs = "[1 Instances]   2 Routes   3 Daemon"
	case routesPage:
		tabs = "1 Instances   [2 Routes]   3 Daemon"
	case daemonPage:
		tabs = "1 Instances   2 Routes   [3 Daemon]"
	}
	contextLine := "Auto-refresh every 2s · " + fmt.Sprintf("%d active instances · %d routes", len(m.snapshot.Instances), len(m.snapshot.Routes))
	if m.page >= servicesPage {
		live, _ := m.instance()
		contextLine = clean(live.Worktree) + " · " + clean(live.Profile)
		tabs = "s Services   u URLs   e Environment"
		switch m.page {
		case servicesPage:
			tabs = "[s Services]   u URLs   e Environment"
		case urlsPage:
			tabs = "s Services   [u URLs]   e Environment"
		case envPage:
			tabs = "s Services   u URLs   [e Environment]"
		}
		if m.service != "" {
			contextLine += " · " + clean(m.service)
		}
	}
	if m.help || m.detail != "" {
		text := helpText
		if m.detail != "" {
			text = clean(m.detail)
		}
		lines := strings.Split(ansi.Hardwrap(text, width, false), "\n")
		capacity := max(1, m.height-5)
		offset := min(m.helpOffset, max(0, len(lines)-capacity))
		return header + "\n\n" + strings.Join(lines[offset:min(len(lines), offset+capacity)], "\n") + "\n\n↑↓ / PgUp PgDn Scroll · Esc Close\n"
	}
	rows := m.rows()
	capacity := max(1, m.height-14)
	start := 0
	if m.cursor >= capacity {
		start = m.cursor - capacity + 1
	}
	lines := make([]string, 0, capacity)
	for i := start; i < min(len(rows), start+capacity); i++ {
		prefix := "  "
		if i == m.cursor {
			prefix = "› "
		}
		line := ansi.Truncate(prefix+clean(rows[i].label), width, "…")
		if i == m.cursor {
			line = selectionStyle.Render(line)
		}
		lines = append(lines, line)
	}
	if len(rows) == 0 {
		empty := "No matching rows. Esc clears the filter."
		if m.filter == "" {
			switch m.page {
			case instancesPage:
				empty = "No active instances. Start one with webport dev -d."
			case routesPage:
				empty = "No published routes. Start a server with webport dev."
			case servicesPage:
				empty = "No services in this instance."
			case urlsPage:
				empty = "No URLs for this service yet. Select another service with [ / ]."
			case envPage:
				empty = "No environment values available."
			case daemonPage:
				empty = "Daemon unavailable. Run webport doctor to diagnose."
			}
		}
		if m.loading {
			empty = "Loading…"
		}
		lines = append(lines, empty)
	}
	for len(lines) < capacity {
		lines = append(lines, "")
	}
	preview := ""
	if m.cursor < len(rows) {
		preview = clean(rows[m.cursor].label)
	}
	preview = ansi.Truncate(preview, width*2, "…")
	preview = ansi.Hardwrap(preview, width, false)
	previewLines := strings.Split(preview, "\n")
	for len(previewLines) < 2 {
		previewLines = append(previewLines, "")
	}
	filter := "/ Search"
	if m.filter != "" || m.searching {
		filter = "/ " + clean(m.filter)
		if m.searching {
			filter += "▏  Enter done · Esc clear"
		}
	}
	position := fmt.Sprintf("%d/%d", min(m.cursor+1, len(rows)), len(rows))
	message := m.message
	if m.snapshot.Error != "" {
		contextLine += " · " + clean(m.snapshot.Error)
	}
	if m.page == envPage {
		visibility := "sensitive hidden"
		if m.sensitive {
			visibility = "SENSITIVE SHOWN"
		}
		scope := "managed"
		if m.inherited {
			scope = "inherited included"
		}
		filter += " · " + visibility + " · " + scope
	}
	if m.confirm != nil {
		message = fmt.Sprintf("%s %s (%s)? All services affected. y/Enter confirm · Esc cancel", strings.ToUpper(m.confirm.operation), filepath.Base(m.confirm.instance.Worktree), m.confirm.instance.Profile)
	}
	if m.busy {
		message = "Working… " + message
	}
	hints := "↑↓ Select · Enter Open · r Restart · x Kill"
	if m.page == routesPage {
		hints = "↑↓ Select · c Copy URL"
	}
	if m.page == envPage || m.page == urlsPage {
		hints = "Enter Expand · c Copy URL · [ / ] Service · Esc Back"
		if m.page == envPage {
			hints = "Enter Expand · c Copy · Y NAME=value · v Reveal · i Inherited"
		}
	}
	if m.confirm != nil {
		hints = "y / Enter Confirm · Esc Cancel"
	}
	return header + "\n" + ansi.Truncate(tabs, width, "…") + "\n" + mutedStyle.Render(ansi.Truncate(contextLine, width, "…")) + "\n\n" + strings.Join(lines, "\n") + "\n\n" + strings.Join(previewLines, "\n") + "\n" + mutedStyle.Render(ansi.Truncate(filter+" · "+position, width, "…")) + "\n" + ansi.Truncate(clean(message), width, "…") + "\n" + mutedStyle.Render(ansi.Truncate(hints, width, "…")) + "\n" + mutedStyle.Render(ansi.Truncate("? Help · q Quit · Tab Tabs · / Search · F5 Refresh", width, "…")) + "\n"
}

func Run(in io.Reader, out io.Writer, backend Backend) error {
	m := New(backend, func(value string) (string, error) { return Copy(out, value) })
	_, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen()).Run()
	return err
}
