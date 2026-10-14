package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/hub"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/tui"
)

// DaemonBackend serves the dashboard from inside the daemon: sessions from
// the hub, routes from the route store, and status from the daemon itself.
type DaemonBackend struct {
	Hub    *hub.Hub
	Routes func() []route.Route
	Status func(context.Context) map[string]any
}

func (b DaemonBackend) Snapshot(ctx context.Context) tui.Snapshot {
	routes := b.Routes()
	sort.Slice(routes, func(i, j int) bool { return routes[i].Domain < routes[j].Domain })
	return tui.Snapshot{Instances: b.Hub.List(), Routes: routes, Daemon: b.Status(ctx)}
}

func (b DaemonBackend) request(ctx context.Context, live state.LiveState, operation string, payload any) (state.Response, error) {
	response, err := b.Hub.Request(ctx, live.SessionID, operation, payload)
	if err != nil {
		return response, err
	}
	if !response.OK {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (b DaemonBackend) Inspect(ctx context.Context, live state.LiveState, sensitive, inherited bool) (session.LiveInspection, error) {
	response, err := b.request(ctx, live, "inspect", map[string]any{"show_sensitive": sensitive, "include_inherited": inherited})
	if err != nil {
		return session.LiveInspection{}, err
	}
	data, err := json.Marshal(response.Payload["inspection"])
	if err != nil {
		return session.LiveInspection{}, err
	}
	var result session.LiveInspection
	if err := json.Unmarshal(data, &result); err != nil {
		return session.LiveInspection{}, fmt.Errorf("decode inspection: %w", err)
	}
	return result, nil
}

func (b DaemonBackend) Control(ctx context.Context, live state.LiveState, operation string) error {
	_, err := b.request(ctx, live, operation, nil)
	return err
}

func (b DaemonBackend) Logs(ctx context.Context, live state.LiveState, service string) ([]string, error) {
	response, err := b.request(ctx, live, "logs", map[string]any{"service": service})
	if err != nil {
		return nil, err
	}
	var lines []string
	if items, ok := response.Payload["lines"].([]any); ok {
		for _, item := range items {
			if line, ok := item.(string); ok {
				lines = append(lines, state.PlainText(line))
			}
		}
	}
	if message, _ := response.Payload["error"].(string); message != "" {
		return lines, errors.New(message)
	}
	return lines, nil
}
