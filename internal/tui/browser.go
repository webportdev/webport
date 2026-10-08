package tui

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/webportdev/webport/internal/devsession/state"
)

// OpenURL passes an HTTP URL as one argument, never through a shell.
func OpenURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("only HTTP(S) URLs without credentials can be opened")
	}
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, command, value).Run()
}

func instanceURL(live state.LiveState, service string) string {
	names := make([]string, 0, len(live.Routes))
	for name := range live.Routes {
		if service == "" || name == service {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if value := live.Routes[name].URL; value != "" {
			return value
		}
	}
	names = names[:0]
	for name := range live.Endpoints {
		if service == "" || strings.HasPrefix(name, service+".") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if value := live.Endpoints[name]; value != "" {
			return value
		}
	}
	return ""
}

func (m Model) selectedURL() string {
	rows := m.rows()
	if m.cursor >= len(rows) {
		return ""
	}
	selected := rows[m.cursor]
	switch m.page {
	case routesPage, urlsPage:
		return selected.value
	case instancesPage:
		for _, live := range m.snapshot.Instances {
			if live.SessionID == selected.id {
				return instanceURL(live, "")
			}
		}
	case servicesPage:
		live, _ := m.instance()
		return instanceURL(live, selected.id)
	}
	return ""
}
