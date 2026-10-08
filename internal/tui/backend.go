// Package tui provides the interactive development session dashboard.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/route"
)

type Snapshot struct {
	Instances []state.LiveState
	Routes    []route.Route
	Daemon    map[string]any
	Error     string
}

type Backend interface {
	Snapshot(context.Context) Snapshot
	Inspect(context.Context, state.LiveState, bool, bool) (session.LiveInspection, error)
	Control(context.Context, state.LiveState, string) error
	Logs(context.Context, state.LiveState, string) ([]string, error)
}

type LocalBackend struct{ API string }

func (b LocalBackend) Snapshot(ctx context.Context) Snapshot {
	var result Snapshot
	instances, err := state.ListLive()
	result.Instances = instances
	if err != nil {
		result.Error = "Sessions: " + err.Error()
	}
	var routes route.RoutesList
	if err := b.get(ctx, "/routes", &routes); err != nil {
		result.Error = strings.TrimSpace(result.Error + " Daemon: " + err.Error())
	} else {
		result.Routes = routes.Routes
	}
	sort.Slice(result.Routes, func(i, j int) bool { return result.Routes[i].Domain < result.Routes[j].Domain })
	if err := b.get(ctx, "/status", &result.Daemon); err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	if result.Daemon != nil {
		result.Daemon["webport_api"] = b.API
		parsed, err := url.Parse(b.API)
		if err == nil && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
			address := net.JoinHostPort(parsed.Hostname(), "443")
			dialer := net.Dialer{Timeout: 250 * time.Millisecond}
			conn, err := dialer.DialContext(ctx, "tcp", address)
			status := "not listening at " + address
			if err == nil {
				status = "listening at " + address
				_ = conn.Close()
			}
			result.Daemon["traefik_https_listener"] = status
		}
	}
	return result
}

func (b LocalBackend) get(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(b.API, "/")+path, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", path, response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func (b LocalBackend) Inspect(ctx context.Context, live state.LiveState, sensitive, inherited bool) (session.LiveInspection, error) {
	response, err := state.Dial(ctx, live.ControlPath, live.ControlToken, state.Request{Operation: "inspect", Payload: map[string]any{"show_sensitive": sensitive, "include_inherited": inherited}})
	if err != nil {
		return session.LiveInspection{}, err
	}
	if !response.OK {
		return session.LiveInspection{}, fmt.Errorf("%s", response.Error)
	}
	data, err := json.Marshal(response.Payload["inspection"])
	if err != nil {
		return session.LiveInspection{}, err
	}
	var result session.LiveInspection
	err = json.Unmarshal(data, &result)
	return result, err
}

func (b LocalBackend) Control(ctx context.Context, live state.LiveState, operation string) error {
	response, err := state.Dial(ctx, live.ControlPath, live.ControlToken, state.Request{Operation: operation})
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("%s", response.Error)
	}
	return nil
}

const requestTimeout = 3 * time.Second
