package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/plan"
	"github.com/webportdev/webport/internal/devsession/ports"
	"github.com/webportdev/webport/internal/devsession/state"
)

func InspectConfig(ctx context.Context, options Options) (plan.Plan, error) {
	cfg, err := config.Load(config.Options{CurrentDir: options.CurrentDir, ConfigPath: options.ConfigPath})
	if err != nil {
		return plan.Plan{}, err
	}
	id, err := identity.Resolve(cfg, options.CurrentDir, nil)
	if err != nil {
		return plan.Plan{}, err
	}
	owners := make(map[string]string)
	for name, service := range cfg.Services {
		if service.Route != nil {
			owners[service.Route.Port] = name
		}
	}
	allocations, err := ports.Resolve(ctx, cfg.Ports, owners, ports.Options{})
	if err != nil {
		return plan.Plan{}, err
	}
	portValues := make(map[string]int)
	for name, allocation := range allocations {
		if !allocation.Discovered {
			portValues[name] = allocation.Port
		}
	}
	client, err := daemon.NewHTTPClient(options.API, nil, nil)
	if err != nil {
		return plan.Plan{}, err
	}
	profile := options.Profile
	if profile == "" && options.Service == "" {
		profile = "default"
	}
	primary := options.Service
	if primary == "" && profile != "" && len(cfg.Profiles[profile].Services) > 0 {
		primary = cfg.Profiles[profile].Services[0]
	}
	routes, err := plan.ResolveRoutes(ctx, cfg, id, client, primary, portValues)
	if err != nil {
		return plan.Plan{}, err
	}
	return plan.Build(cfg, id, plan.BuildOptions{Profile: profile, Service: options.Service, PortSpecs: cfg.Ports, PortValues: allocations, Routes: routes})
}

func Status(options Options) (any, error) {
	cfg, err := config.Load(config.Options{CurrentDir: options.CurrentDir, ConfigPath: options.ConfigPath})
	if err != nil {
		return nil, err
	}
	id, err := identity.Resolve(cfg, options.CurrentDir, nil)
	if err != nil {
		return nil, err
	}
	store, err := state.NewStore(id.WorktreeRoot, "")
	if err != nil {
		return nil, err
	}
	live, liveErr := store.ReadLive()
	if liveErr == nil {
		return live, nil
	}
	if !errors.Is(liveErr, os.ErrNotExist) {
		return nil, liveErr
	}
	last, lastErr := store.ReadLast()
	if lastErr != nil {
		return nil, fmt.Errorf("no live development session: %w", lastErr)
	}
	return last, nil
}

func Control(ctx context.Context, options Options, operation string, payload map[string]any) (state.Response, error) {
	cfg, err := config.Load(config.Options{CurrentDir: options.CurrentDir, ConfigPath: options.ConfigPath})
	if err != nil {
		return state.Response{}, err
	}
	id, err := identity.Resolve(cfg, options.CurrentDir, nil)
	if err != nil {
		return state.Response{}, err
	}
	store, err := state.NewStore(id.WorktreeRoot, "")
	if err != nil {
		return state.Response{}, err
	}
	live, err := store.ReadLive()
	if err != nil {
		return state.Response{}, fmt.Errorf("no live development session: %w", err)
	}
	return state.Dial(ctx, live.ControlPath, live.ControlToken, state.Request{Operation: operation, Payload: payload})
}

func RenderEnvironment(values map[string]string, shell string) (string, error) {
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	if shell == "json" {
		encoded, err := json.MarshalIndent(values, "", "  ")
		return string(encoded) + "\n", err
	}
	var output strings.Builder
	for _, name := range keys {
		value := "'" + strings.ReplaceAll(values[name], "'", "'\\''") + "'"
		switch shell {
		case "bash", "posix", "zsh":
			fmt.Fprintf(&output, "export %s=%s\n", name, value)
		case "fish":
			fmt.Fprintf(&output, "set -gx %s %s\n", name, value)
		default:
			return "", fmt.Errorf("unsupported environment shell %q", shell)
		}
	}
	return output.String(), nil
}
