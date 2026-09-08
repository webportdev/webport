package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/plan"
	"github.com/webportdev/webport/internal/devsession/ports"
	"github.com/webportdev/webport/internal/devsession/secrets"
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

func LogFiles(options Options, service string) ([]string, error) {
	value, statusErr := Status(options)
	if statusErr == nil {
		switch stateValue := value.(type) {
		case state.LiveState:
			if paths := selectLogPaths(stateValue.LogPaths, service); len(paths) > 0 {
				return paths, nil
			}
		case state.LastSession:
			if paths := selectLogPaths(stateValue.LogPaths, service); len(paths) > 0 {
				return paths, nil
			}
		}
	}
	cfg, err := config.Load(config.Options{CurrentDir: options.CurrentDir, ConfigPath: options.ConfigPath})
	if err != nil {
		return nil, err
	}
	id, err := identity.Resolve(cfg, options.CurrentDir, nil)
	if err != nil {
		return nil, err
	}
	services := make([]string, 0, len(cfg.Services))
	if service != "" {
		services = append(services, service)
	} else {
		for name := range cfg.Services {
			services = append(services, name)
		}
		sort.Strings(services)
	}
	var paths []string
	for _, name := range services {
		settings := cfg.Services[name].Logs
		if settings == nil || settings.Path == "" || settings.Destination == "none" {
			continue
		}
		path, resolveErr := id.ResolvePath(settings.Path)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if settings.Destination == "directory" {
			path = filepath.Join(path, name+".log")
		}
		if settings.Streams == "separate" {
			extension := filepath.Ext(path)
			paths = append(paths, strings.TrimSuffix(path, extension)+".stdout"+extension, strings.TrimSuffix(path, extension)+".stderr"+extension)
		} else {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("no configured logs for development session")
	}
	return paths, nil
}

func StreamLogs(ctx context.Context, options Options, service string, follow bool, output io.Writer) error {
	paths, err := LogFiles(options, service)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := streamFile(ctx, path, follow, output); err != nil {
			return err
		}
	}
	return nil
}

func streamFile(ctx context.Context, path string, follow bool, output io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open log %s: %w", path, err)
	}
	defer file.Close()
	var offset int64
	for {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		count, err := io.Copy(output, file)
		if err != nil {
			return err
		}
		offset += count
		if !follow {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func ExecInfo(ctx context.Context, options Options, service string) (string, []string, error) {
	response, err := Control(ctx, options, "exec-info", map[string]any{"service": service})
	if err != nil {
		return "", nil, err
	}
	if !response.OK {
		return "", nil, errors.New(response.Error)
	}
	directory, _ := response.Payload["working_dir"].(string)
	values := make(map[string]string)
	if raw, ok := response.Payload["environment"].(map[string]any); ok {
		for name, value := range raw {
			if text, ok := value.(string); ok {
				values[name] = text
			}
		}
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, name := range keys {
		environment = append(environment, name+"="+values[name])
	}
	return directory, environment, nil
}

func Exec(ctx context.Context, options Options, service string, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("exec command is required")
	}
	directory, environment, err := ExecInfo(ctx, options, service)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Dir, command.Env = directory, environment
	command.Stdin, command.Stdout, command.Stderr = in, out, errOut
	return command.Run()
}

func CleanSecrets(options Options) error {
	cfg, err := config.Load(config.Options{CurrentDir: options.CurrentDir, ConfigPath: options.ConfigPath})
	if err != nil {
		return err
	}
	id, err := identity.Resolve(cfg, options.CurrentDir, nil)
	if err != nil {
		return err
	}
	store, err := state.NewStore(id.WorktreeRoot, "")
	if err != nil {
		return err
	}
	if _, err := store.ReadLive(); err == nil {
		return errors.New("cannot clean project secrets while a development session is active")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	secretStore, err := secrets.NewStore(filepathForSecretStore(id))
	if err != nil {
		return err
	}
	return secrets.Clean(secretStore)
}

func selectLogPaths(paths map[string]string, service string) []string {
	result := make([]string, 0)
	for name, path := range paths {
		if service == "" || name == service || strings.HasPrefix(name, service+".") {
			result = append(result, path)
		}
	}
	sort.Strings(result)
	return result
}
