package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/route"
)

func TestServiceDescriptionsAreVisibleSearchableAndPlainText(t *testing.T) {
	m, _ := fixtureModel()
	m.instanceID, m.service, m.page = "a", "server", servicesPage
	m.width, m.height = 140, 40
	m.snapshot.Instances[0].Routes = nil
	service := m.snapshot.Instances[0].Services["server"]
	service.Description = "Application frontend\n\x1b[31mSecond line\x1b[0m\a"
	m.snapshot.Instances[0].Services["server"] = service
	rows := m.rows()
	if len(rows) != 1 || !strings.Contains(m.rowLabel(rows[0], 90), "Application frontend") {
		t.Fatalf("service row omitted description: %+v", rows)
	}
	details := ansi.Strip(strings.Join(m.detailLines(90), "\n"))
	if !strings.Contains(details, "DESCRIPTION\nApplication frontend Second line") {
		t.Fatalf("service details = %q", details)
	}
	view := m.View()
	if strings.Contains(view, "\x1b[31m") || strings.ContainsRune(view, '\a') {
		t.Fatalf("description rendered control characters: %q", view)
	}
	expanded, _ := key(m, "d")
	if !strings.Contains(expanded.View(), "Application frontend") {
		t.Fatal("expanded details omitted description")
	}
	m.filter = "frontend"
	if rows := m.rows(); len(rows) != 1 || rows[0].id != "server" {
		t.Fatalf("description search = %+v", rows)
	}
	service.Description = ""
	m.snapshot.Instances[0].Services["server"] = service
	if len(m.rows()) != 0 {
		t.Fatal("description filter matched service without a description")
	}
	m.filter = ""
	if strings.Contains(strings.Join(m.detailLines(90), "\n"), "DESCRIPTION") {
		t.Fatal("empty description has a detail field")
	}
}

func TestLongURLLinksSurviveWrappingClippingAndOverlays(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	domain := strings.Repeat("long-project-branch-", 6) + "example.test"
	target := "https://" + domain
	for _, width := range []int{48, 100, 140} {
		for _, p := range []page{instancesPage, servicesPage, routesPage, urlsPage} {
			for _, action := range []string{"", "enter", "d"} {
				if action == "enter" && (p == instancesPage || p == servicesPage) {
					continue
				}
				t.Run(fmt.Sprintf("width=%d/page=%d/action=%s", width, p, action), func(t *testing.T) {
					m, _ := fixtureModel()
					m.width, m.height, m.page = width, 40, p
					m.instanceID, m.service = "a", "server"
					m.snapshot.Instances[0].Routes["server"] = state.RouteState{State: "active", URL: target}
					m.snapshot.Routes = []route.Route{{Project: "app", Branch: "main", Domain: domain, Port: 3000}}
					if action != "" {
						m, _ = key(m, action)
						// Resize while the overlay is open; links must still refer to
						// the original full URL after reflowing the text.
						m.width = 48
					}
					view := m.View()
					fragments := checkURLLinks(t, view, target, m.width-2)
					if (action != "" || width >= 100) && fragments < 2 {
						t.Fatalf("expected multiple linked fragments, got %d: %q", fragments, view)
					}
					if action == "" && width == 48 && (p == routesPage || p == urlsPage) && fragments == 0 {
						t.Fatal("compact URL row lost its hyperlink")
					}
					if action != "" {
						m.helpOffset = 2
						checkURLLinks(t, m.View(), target, m.width-2)
					}
				})
			}
		}
	}
}

// Every fragment carries the complete destination and closes before the next
// line, preventing terminal links from including adjacent panes or borders.
func checkURLLinks(t *testing.T, view, target string, width int) int {
	t.Helper()
	count := 0
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > width {
			t.Fatalf("line exceeds viewport: %q", line)
		}
		for rest := line; strings.Contains(rest, "\x1b]8;"); {
			_, rest, _ = strings.Cut(rest, "\x1b]8;")
			uri, restAfterURI, ok := strings.Cut(rest, "\a")
			if !ok || uri != ";"+target {
				t.Fatalf("wrong or incomplete link target: %q", uri)
			}
			fragment, after, ok := strings.Cut(restAfterURI, ansi.ResetHyperlink())
			if !ok || ansi.StringWidth(fragment) == 0 || strings.ContainsAny(ansi.Strip(fragment), "│╭╮╰╯") {
				t.Fatalf("link leaks outside URL text: %q", restAfterURI)
			}
			count++
			rest = after
		}
	}
	return count
}

func TestTerminalHyperlinksRejectUnsafeURLsAndEnvironmentValues(t *testing.T) {
	for _, target := range []string{
		"https://example.test\x1b]52;c;bad\a",
		"https://example.test\x9cunsafe",
		"https://user:secret@example.test",
		"file:///tmp/private",
		"javascript:alert(1)",
	} {
		m, _ := fixtureModel()
		m.width, m.height = 140, 40
		m.instanceID, m.service, m.page = "a", "server", urlsPage
		m.snapshot.Instances[0].Routes["server"] = state.RouteState{URL: target}
		for _, action := range []string{"", "enter", "d"} {
			candidate := m
			if action != "" {
				candidate, _ = key(candidate, action)
			}
			if view := candidate.View(); strings.Contains(view, "\x1b]8;") || strings.Contains(view, "52;c;bad") || strings.ContainsRune(view, '\x9c') {
				t.Fatalf("unsafe link for %q: %q", target, view)
			}
		}
	}
	m, _ := fixtureModel()
	m.width, m.height = 140, 40
	m.instanceID, m.service, m.page = "a", "server", envPage
	m.inspection.Environment = map[string]map[string]string{"server": {"URL": "https://example.test"}}
	for _, action := range []string{"", "enter", "d"} {
		candidate := m
		if action != "" {
			candidate, _ = key(candidate, action)
		}
		if strings.Contains(candidate.View(), "\x1b]8;") {
			t.Fatal("environment value rendered as a terminal hyperlink")
		}
	}
}

func TestReadinessIsGreenOnlyWithoutErrorsAndRedOnlyWhenNotReady(t *testing.T) {
	ready := map[string]state.ServiceState{"web": {Ready: true}}
	for _, test := range []struct {
		live state.LiveState
		want string
	}{
		{state.LiveState{Services: ready}, goodStyle.Render("● 1/1 ready")},
		{state.LiveState{Services: ready, LastError: "heartbeat lease: daemon unavailable"}, warningStyle.Render("◐ 1/1 ready")},
		{state.LiveState{Services: map[string]state.ServiceState{"web": {LastError: "exited"}}}, badStyle.Render("● 0/1 ready")},
		{state.LiveState{Services: map[string]state.ServiceState{"web": {}}}, warningStyle.Render("◐ 0/1 ready")},
	} {
		if got := readiness(test.live); got != test.want {
			t.Fatalf("readiness(%+v) = %q, want %q", test.live, got, test.want)
		}
	}
}
