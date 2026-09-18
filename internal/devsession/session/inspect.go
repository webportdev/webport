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
	"sync"
	"time"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/plan"
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
	if options.PreferLive {
		store, err := state.NewStore(id.WorktreeRoot, "")
		if err != nil {
			return plan.Plan{}, err
		}
		live, err := store.ReadLive()
		if err == nil {
			response, err := state.Dial(ctx, live.ControlPath, live.ControlToken, state.Request{
				Operation: "config", Payload: map[string]any{"structured": true, "show_sensitive": options.ShowSensitive, "include_inherited": options.IncludeInherited},
			})
			if err != nil {
				return plan.Plan{}, err
			}
			if !response.OK {
				return plan.Plan{}, fmt.Errorf("inspect live config: %s", response.Error)
			}
			encoded, err := json.Marshal(response.Payload["plan"])
			if err != nil {
				return plan.Plan{}, err
			}
			var p plan.Plan
			if err := json.Unmarshal(encoded, &p); err != nil {
				return plan.Plan{}, err
			}
			return p, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return plan.Plan{}, err
		}
	}
	if options.API == "" {
		options.API = "http://127.0.0.1:8080"
	}
	client, err := daemon.NewHTTPClient(options.API, nil, nil)
	if err != nil {
		return plan.Plan{}, err
	}
	p, environments, err := prepare(ctx, cfg, id, options, client)
	if err != nil {
		return plan.Plan{}, err
	}
	return inspectEnvironments(p, environments, options.IncludeInherited), nil
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
		// The control token is an implementation credential, not session
		// inspection data. Keep it available only to Control, which reads the
		// live file directly for authenticated dialing.
		live.ControlToken = ""
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
			if service != "" {
				if _, ok := stateValue.Services[service]; !ok {
					return nil, fmt.Errorf("unknown service %q", service)
				}
			}
			return selectedLogPaths(stateValue.LogPaths, service)
		case state.LastSession:
			if service != "" {
				if _, ok := stateValue.FinalStates[service]; !ok {
					return nil, fmt.Errorf("unknown service %q", service)
				}
			}
			return selectedLogPaths(stateValue.LogPaths, service)
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
		if _, ok := cfg.Services[service]; !ok {
			return nil, fmt.Errorf("unknown service %q", service)
		}
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
		if settings == nil || settings.Destination == "" || settings.Destination == "session" || settings.Destination == "none" || settings.Path == "" {
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
		if service != "" && cfg.Services[service].Logs != nil && cfg.Services[service].Logs.Destination == "none" {
			return nil, fmt.Errorf("logging is disabled for service %q", service)
		}
		return nil, errors.New("no live or retained logs for development session")
	}
	return paths, nil
}

func selectedLogPaths(paths map[string]string, service string) ([]string, error) {
	selected := selectLogPaths(paths, service)
	if len(selected) > 0 {
		return selected, nil
	}
	if service != "" {
		return nil, fmt.Errorf("logging is disabled for service %q", service)
	}
	return nil, errors.New("development session has no retained logs")
}

func StreamLogs(ctx context.Context, options Options, service string, follow bool, output io.Writer) error {
	paths, err := LogFiles(options, service)
	if err != nil {
		return err
	}
	if follow && len(paths) > 1 {
		followContext, cancel := context.WithCancel(ctx)
		defer cancel()
		var wait sync.WaitGroup
		results := make(chan error, len(paths))
		locked := &lockedWriter{writer: output}
		for _, path := range paths {
			wait.Add(1)
			go func(path string) {
				defer wait.Done()
				err := streamFile(followContext, path, true, locked)
				if err != nil {
					cancel()
				}
				results <- err
			}(path)
		}
		wait.Wait()
		close(results)
		for err := range results {
			if err != nil {
				return err
			}
		}
		return nil
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
	defer func() { _ = file.Close() }()
	var offset int64
	for {
		if info, statErr := file.Stat(); statErr == nil && offset > info.Size() {
			offset = 0
		}
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
		if replacement, statErr := os.Open(path); statErr == nil {
			currentInfo, currentErr := file.Stat()
			replacementInfo, replacementErr := replacement.Stat()
			if currentErr == nil && replacementErr == nil && !os.SameFile(currentInfo, replacementInfo) {
				_ = file.Close()
				file = replacement
				offset = 0
				continue
			}
			_ = replacement.Close()
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

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(value)
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
	if err := store.Acquire(); err != nil {
		if errors.Is(err, state.ErrLocked) {
			return errors.New("cannot clean project secrets while a development session is active")
		}
		return err
	}
	defer func() { _ = store.Release() }()
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
