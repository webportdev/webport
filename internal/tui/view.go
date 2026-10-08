package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/webportdev/webport/internal/devsession/state"
)

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	selectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("24"))
	mutedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	goodStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	warningStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))
	badStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	borderStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("239"))
)

func fit(value string, width int) string   { return ansi.Truncate(value, max(1, width), "…") }
func plain(value string, width int) string { return fit(clean(value), width) }
func pad(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}

func (m Model) listCapacity() int { return max(1, m.height-12) }
func (m Model) pageName() string {
	return []string{"Instances", "Routes", "Daemon", "Services", "URLs", "Environment", "Logs"}[m.page]
}

func (m Model) tabs() string {
	labels := []string{"1 Instances", "2 Routes", "3 Daemon"}
	active := int(m.page)
	if m.page >= servicesPage {
		labels = []string{"s Services", "u URLs", "e Environment", "l Logs"}
		active -= int(servicesPage)
	}
	for i, label := range labels {
		if i == active {
			labels[i] = selectionStyle.Render(" " + label + " ")
		} else {
			labels[i] = mutedStyle.Render(" " + label + " ")
		}
	}
	return strings.Join(labels, "  ")
}

// panel has an exact cell width and height, including its border. Content is
// clipped before styling so resizing and long process text cannot wrap it.
func panel(title string, lines []string, width, height int) string {
	inner := max(1, width-2)
	top := "─ " + title + " "
	top = fit(top, inner)
	top += strings.Repeat("─", max(0, inner-ansi.StringWidth(top)))
	out := []string{borderStyle.Render("╭" + top + "╮")}
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(lines) {
			line = fit(lines[i], inner)
		}
		out = append(out, borderStyle.Render("│")+pad(line, inner)+borderStyle.Render("│"))
	}
	out = append(out, borderStyle.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return strings.Join(out, "\n")
}

func readiness(live state.LiveState) string {
	ready := 0
	failed := live.LastError != ""
	for _, service := range live.Services {
		if service.Ready {
			ready++
		}
		if service.LastError != "" {
			failed = true
		}
	}
	text := fmt.Sprintf("%d/%d ready", ready, len(live.Services))
	if failed {
		return badStyle.Render("● " + text)
	}
	if ready == len(live.Services) && ready > 0 {
		return goodStyle.Render("● " + text)
	}
	return warningStyle.Render("◐ " + text)
}

func (m Model) rowLabel(r row, width int) string {
	if m.page == instancesPage {
		for _, live := range m.snapshot.Instances {
			if live.SessionID == r.id {
				status := readiness(live)
				nameWidth := max(8, width-ansi.StringWidth(status)-3)
				return pad(plain(instanceName(live), nameWidth), nameWidth) + "  " + status
			}
		}
	}
	if m.page == servicesPage {
		live, _ := m.instance()
		s := live.Services[r.id]
		status := warningStyle.Render("◐ " + clean(s.State))
		if s.Ready {
			status = goodStyle.Render("● " + clean(s.State))
		}
		if s.LastError != "" {
			status = badStyle.Render("● " + clean(s.State))
		}
		nameWidth := max(8, width-ansi.StringWidth(status)-3)
		return pad(plain(r.id, nameWidth), nameWidth) + "  " + status
	}
	return plain(r.label, width)
}

func (m Model) emptyText() string {
	if m.page == logsPage && m.logError != "" {
		return m.logError
	}
	if m.page == logsPage && m.logFetching && len(m.logLines) == 0 {
		return "Loading logs…"
	}
	if m.loading {
		return "Loading…"
	}
	if m.filter != "" {
		return "No matches. Esc clears the filter."
	}
	return []string{
		"No instances. Start with webport dev -d.",
		"No routes. Start a server with webport dev.",
		"Daemon unavailable. Run webport doctor.",
		"No services in this instance.",
		"No URLs yet. Try another service with [ / ].",
		"No environment values available.",
		"No saved log output. Logging may be disabled or unavailable for this wrapper.",
	}[m.page]
}

func (m Model) listLines(width int) []string {
	rows := m.rows()
	capacity := m.listCapacity()
	start := max(0, m.cursor-capacity+1)
	lines := []string{mutedStyle.Render(pad("  "+m.pageName(), max(1, width-12)) + fmt.Sprintf("%d/%d", min(m.cursor+1, len(rows)), len(rows)))}
	for i := start; i < min(len(rows), start+capacity); i++ {
		label := m.rowLabel(rows[i], width-2)
		prefix := "  "
		if i == m.cursor {
			prefix = "› "
			// One continuous highlight, even over an independently styled badge.
			label = ansi.Strip(label)
			lines = append(lines, selectionStyle.Render(pad(fit(prefix+label, width), width)))
		} else {
			lines = append(lines, fit(prefix+label, width))
		}
	}
	if len(rows) == 0 {
		lines = append(lines, mutedStyle.Render(plain(m.emptyText(), width)))
	}
	return lines
}

func (m Model) detailLines(width int) []string {
	rows := m.rows()
	if m.cursor >= len(rows) {
		return []string{mutedStyle.Render("Select a row to see details.")}
	}
	r := rows[m.cursor]
	var lines []string
	add := func(label, value string) {
		lines = append(lines, mutedStyle.Render(label))
		lines = append(lines, strings.Split(ansi.Hardwrap(clean(value), max(1, width), false), "\n")...)
		lines = append(lines, "")
	}
	var live state.LiveState
	if m.page == instancesPage {
		for _, candidate := range m.snapshot.Instances {
			if candidate.SessionID == r.id {
				live = candidate
				break
			}
		}
	} else {
		live, _ = m.instance()
	}
	switch m.page {
	case instancesPage:
		add("PROJECT / BRANCH", instanceName(live))
		lines = append(lines, readiness(live), "")
		add("PROFILE", live.Profile)
		add("WORKTREE", live.Worktree)
		if !live.StartedAt.IsZero() {
			add("STARTED", live.StartedAt.Local().Format("Jan 02 15:04:05"))
		}
		if value := instanceURL(live, ""); value != "" {
			add("URL · o Open · c Copy", value)
		}
		if live.LastError != "" {
			add("ERROR", live.LastError)
		}
	case servicesPage:
		s := live.Services[r.id]
		add("SERVICE", r.id)
		add("STATE", s.State)
		add("PROCESS", fmt.Sprintf("PID %d", s.PID))
		if value := instanceURL(live, r.id); value != "" {
			add("URL · o Open · c Copy", value)
		}
		if value := live.LogPaths[r.id]; value != "" {
			add("LOG FILE · l View logs", value)
		}
		names := make([]string, 0, len(live.Ports))
		for name := range live.Ports {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add("INSTANCE PORT", fmt.Sprintf("%s : %d", name, live.Ports[name]))
		}
		if s.LastError != "" {
			add("ERROR", s.LastError)
		}
	case routesPage:
		for _, route := range m.snapshot.Routes {
			if route.Project+":"+route.Branch == r.id {
				add("ROUTE ID", r.id)
				add("URL · o Open · c Copy", r.value)
				add("BACKEND", fmt.Sprintf("%s:%d", backendHost(route.Host), route.Port))
				add("SOURCE", route.Source)
				if !route.ExpiresAt.IsZero() {
					add("EXPIRES", route.ExpiresAt.Local().Format("Jan 02 15:04:05"))
				}
				break
			}
		}
	case urlsPage:
		add("ENDPOINT", r.id)
		add("URL · o Open · c Copy", r.value)
	case envPage:
		add("ENVIRONMENT KEY", r.id)
		add("VALUE · c Copy · Y Copy entry", r.value)
		lines = append(lines, mutedStyle.Render("Copy works while values stay hidden."))
	case daemonPage:
		add("PUBLICATION STATUS", r.id)
		add("VALUE", fmt.Sprint(m.snapshot.Daemon[r.id]))
	}
	return lines
}

func (m Model) View() string {
	width := max(1, m.width-2)
	if m.width < 48 || m.height < 14 {
		lines := []string{"WEBPORT", "Resize to at least 48 × 14.", "q Quit · ? Help"}
		for i := range lines {
			lines[i] = plain(lines[i], width)
		}
		return strings.Join(lines[:min(len(lines), max(1, m.height))], "\n")
	}
	header := titleStyle.Render("◆ WEBPORT") + mutedStyle.Render("  /  development dashboard")
	if m.help || m.detail != "" {
		text := helpText
		title := "Keyboard shortcuts"
		if m.detail != "" {
			text = m.detailText()
			title = "Selected value"
		}
		lines := strings.Split(ansi.Hardwrap(text, width-2, false), "\n")
		capacity := max(1, m.height-5)
		offset := min(m.helpOffset, max(0, len(lines)-capacity))
		return fit(header, width) + "\n" + panel(title, lines[offset:min(len(lines), offset+capacity)], width, m.height-3) + "\n" + fit(mutedStyle.Render("↑↓ / PgUp PgDn Scroll · Esc Close"), width)
	}

	refresh := "LIVE · 2s"
	if m.paused {
		refresh = "PAUSED · p Resume"
	} else if m.fetching || m.loading {
		refresh = "Refreshing…"
	}
	if !m.updatedAt.IsZero() {
		refresh += " · " + m.updatedAt.Local().Format("15:04:05")
	}
	summary := fmt.Sprintf("%d instances   %d routes", len(m.snapshot.Instances), len(m.snapshot.Routes))
	context := "Current user · " + refresh
	if m.page >= servicesPage {
		live, _ := m.instance()
		context = clean(instanceName(live)) + " / " + clean(live.Profile)
		if m.service != "" {
			context += " / " + clean(m.service)
		}
		context += " · " + refresh
	}
	lines := []string{fit(header, width), fit(m.tabs(), width), fit(titleStyle.Render(summary), width), fit(mutedStyle.Render(context), width), ""}
	bodyHeight := m.height - 9
	if width >= 98 && m.page != logsPage {
		leftWidth := (width - 1) * 55 / 100
		rightWidth := width - leftWidth - 1
		left := panel(m.pageName(), m.listLines(leftWidth-2), leftWidth, bodyHeight)
		right := panel("Selection details", m.detailLines(rightWidth-2), rightWidth, bodyHeight)
		lines = append(lines, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right), "\n")...)
	} else {
		lines = append(lines, strings.Split(panel(m.pageName(), m.listLines(width-2), width, bodyHeight), "\n")...)
	}
	filter := "/ Search"
	if m.filter != "" || m.searching {
		filter = "/ " + clean(m.filter)
		if m.searching {
			filter += "▏ · Enter done · Esc clear"
		}
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
	message := m.message
	if message == "" && m.snapshot.Error != "" {
		message = m.snapshot.Error
	}
	if message == "" && width < 98 {
		rows := m.rows()
		if m.cursor < len(rows) {
			message = rows[m.cursor].label
		}
	}
	messageLine := plain(message, width)
	if m.snapshot.Error != "" || strings.HasPrefix(m.message, "Failed:") || strings.HasPrefix(m.message, "Inspection failed:") {
		messageLine = warningStyle.Render(messageLine)
	}
	hints := "↑↓ Select · Enter Open · o Browser · c Copy · r Restart · x Stop"
	if m.page == routesPage || m.page == urlsPage {
		hints = "↑↓ Select · Enter Expand · o Browser · c Copy · Esc Back"
	}
	if m.page == logsPage {
		hints = "↑↓ Scroll · End Follow · [ / ] Service · Esc Back"
	}
	if m.page == daemonPage {
		hints = "↑↓ Select · Enter Expand · Esc Back"
	}
	if m.page == envPage {
		hints = "Enter Expand · c Copy · Y NAME=value · v Reveal · i Inherited"
	}
	if m.busy {
		messageLine = warningStyle.Render(plain("Working… "+message, width))
	}
	if m.confirm != nil {
		messageLine = warningStyle.Render(plain(fmt.Sprintf("%s %s (%s)? All services affected.", strings.ToUpper(m.confirm.operation), instanceName(m.confirm.instance), m.confirm.instance.Profile), width))
		hints = "y / Enter Confirm · n / Esc Cancel"
	}
	lines = append(lines, fit(mutedStyle.Render(filter), width), messageLine, fit(mutedStyle.Render(hints), width), fit(mutedStyle.Render("? Help · q Quit · d Details · Tab Tabs · p Pause · F5 Refresh"), width))
	return strings.Join(lines, "\n")
}
