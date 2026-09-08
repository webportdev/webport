// Package session connects the phase-1 configured single-service lifecycle.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/webportdev/webport/internal/devsession/command"
	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/env"
	"github.com/webportdev/webport/internal/devsession/exports"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/logs"
	"github.com/webportdev/webport/internal/devsession/plan"
	"github.com/webportdev/webport/internal/devsession/ports"
	"github.com/webportdev/webport/internal/devsession/readiness"
	routeleases "github.com/webportdev/webport/internal/devsession/routes"
	"github.com/webportdev/webport/internal/devsession/secrets"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/devsession/supervisor"
)

type Options struct {
	CurrentDir string
	ConfigPath string
	Profile    string
	Service    string
	API        string
	In         io.Reader
	Out        io.Writer
	ErrOut     io.Writer
}

func Run(ctx context.Context, options Options) (runErr error) {
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	if options.ErrOut == nil {
		options.ErrOut = os.Stderr
	}
	if options.API == "" {
		options.API = "http://127.0.0.1:8080"
	}
	cfg, err := config.Load(config.Options{CurrentDir: options.CurrentDir, ConfigPath: options.ConfigPath})
	if err != nil {
		return err
	}
	id, err := identity.Resolve(cfg, options.CurrentDir, nil)
	if err != nil {
		return err
	}
	profileName := options.Profile
	if profileName == "" && options.Service == "" {
		profileName = "default"
	}
	stateStore, err := state.NewStore(id.WorktreeRoot, "")
	if err != nil {
		return err
	}
	if err := stateStore.Acquire(); err != nil {
		return err
	}
	startedAt := time.Now()
	controlPath := strings.TrimSuffix(stateStore.LivePath, ".live.json") + ".sock"
	var cancelSession context.CancelFunc
	var activePlan plan.Plan
	activeEnvironments := make(map[string]env.Values)
	var liveState state.LiveState
	control, controlErr := state.StartControl(controlPath, func(_ context.Context, request state.Request) state.Response {
		switch request.Operation {
		case "stop":
			if cancelSession != nil {
				cancelSession()
			}
			return state.Response{OK: true, Payload: map[string]any{"session_id": id.SessionID}}
		case "status":
			statusState := liveState
			statusState.ControlToken = ""
			return state.Response{OK: true, Payload: map[string]any{"state": statusState}}
		case "config":
			encoded, _ := activePlan.JSON()
			var payload map[string]any
			_ = json.Unmarshal(encoded, &payload)
			return state.Response{OK: true, Payload: payload}
		case "env":
			showSensitive := false
			if request.Payload != nil {
				showSensitive, _ = request.Payload["show_sensitive"].(bool)
			}
			if len(activePlan.Order) == 0 {
				return state.Response{OK: true, Payload: map[string]any{"values": map[string]string{}}}
			}
			return state.Response{OK: true, Payload: map[string]any{"values": activeEnvironments[activePlan.Order[0]].Map(showSensitive)}}
		case "exec-info":
			serviceName := ""
			if request.Payload != nil {
				serviceName, _ = request.Payload["service"].(string)
			}
			service, ok := activePlan.Services[serviceName]
			if !ok {
				return state.Response{Status: 404, Error: "unknown service"}
			}
			environment := make(map[string]string)
			for _, item := range sessionEnvironment(activeEnvironments[serviceName], activePlan.Identity, activePlan.Routes, serviceName) {
				name, value, found := strings.Cut(item, "=")
				if found {
					environment[name] = value
				}
			}
			return state.Response{OK: true, Payload: map[string]any{"working_dir": service.WorkingDir, "environment": environment}}
		default:
			return state.Response{Status: 404, Error: "unknown control operation"}
		}
	})
	if controlErr != nil {
		_ = stateStore.Release()
		return controlErr
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	cancelSession = cancel
	ctx = sessionCtx
	defer func() {
		cancel()
		_ = control.Close()
		_ = stateStore.RemoveLive()
		last := state.LastSession{SessionID: id.SessionID, Worktree: id.WorktreeRoot, Profile: profileName, StartedAt: startedAt, StoppedAt: time.Now()}
		last.LogPaths = liveState.LogPaths
		if runErr != nil {
			last.Initiating = runErr.Error()
		}
		if cfg.Session.RetainLastSummary == nil || *cfg.Session.RetainLastSummary {
			_ = stateStore.WriteLast(last)
		}
		_ = stateStore.Release()
	}()
	profileEnv := map[string]config.Value{}
	if profileName != "" {
		profile, ok := cfg.Profiles[profileName]
		if !ok && options.Service == "" {
			return fmt.Errorf("profile %q is not defined", profileName)
		}
		if ok {
			profileEnv = profile.Env
		}
	}

	portOwners := make(map[string]string)
	for serviceName, service := range cfg.Services {
		if service.Route != nil {
			portOwners[service.Route.Port] = serviceName
		}
	}
	allocations, err := ports.Resolve(ctx, cfg.Ports, portOwners, ports.Options{})
	if err != nil {
		return err
	}
	portValues := make(map[string]int)
	for name, allocation := range allocations {
		if !allocation.Discovered {
			portValues[name] = allocation.Port
		}
	}
	liveState = state.LiveState{
		SessionID: id.SessionID, Worktree: id.WorktreeRoot, Profile: profileName,
		ControlPath: control.Path(), ControlToken: control.Token(), StartedAt: startedAt, Ports: portValues,
	}
	if err := stateStore.WriteLive(liveState); err != nil {
		return err
	}

	daemonClient, err := daemon.NewHTTPClient(options.API, nil, nil)
	if err != nil {
		return err
	}
	primaryService := options.Service
	if primaryService == "" && profileName != "" && len(cfg.Profiles[profileName].Services) > 0 {
		primaryService = cfg.Profiles[profileName].Services[0]
	}
	routes, err := plan.ResolveRoutes(ctx, cfg, id, daemonClient, primaryService, portValues)
	if err != nil {
		return err
	}

	secretStore, err := secrets.NewStore(filepathForSecretStore(id))
	if err != nil {
		return err
	}
	generated, err := secrets.ResolveValues(cfg.Env, secretStore, nil)
	if err != nil {
		return err
	}
	inherited := environmentFromProcess()
	dotenvFiles := make([]string, 0, len(cfg.Session.EnvFiles))
	for _, path := range cfg.Session.EnvFiles {
		resolved, resolveErr := id.ResolvePath(path)
		if resolveErr != nil {
			return fmt.Errorf("dotenv path %q: %w", path, resolveErr)
		}
		dotenvFiles = append(dotenvFiles, resolved)
	}

	resolvedPlan, err := plan.Build(cfg, id, plan.BuildOptions{
		Profile: profileName, Service: options.Service, PortSpecs: cfg.Ports,
		PortValues: allocations, Routes: routes,
	})
	if err != nil {
		return err
	}
	environments := make(map[string]env.Values, len(resolvedPlan.Order))
	for _, name := range resolvedPlan.Order {
		service := resolvedPlan.Services[name]
		values, resolveErr := env.Resolve(env.Input{
			Inherited: inherited, DotenvFiles: dotenvFiles, Top: generated,
			Profile: profileEnv, Service: service.Env, Project: id.Project,
			Branch: id.Branch, Scope: id.Scope, Ports: portValues, Routes: routes,
			ServiceName: name,
		})
		if resolveErr != nil {
			return resolveErr
		}
		environments[name] = values
	}
	activePlan = resolvedPlan
	for name, values := range environments {
		activeEnvironments[name] = values
	}
	exportPaths := make(map[string]string, len(cfg.Session.Exports))
	for shell, path := range cfg.Session.Exports {
		resolved, resolveErr := id.ResolvePath(path)
		if resolveErr != nil {
			return fmt.Errorf("export path %q: %w", path, resolveErr)
		}
		exportPaths[shell] = resolved
	}
	exportStore, err := exports.New(exportPaths)
	if err != nil {
		return err
	}
	defer exportStore.Remove()
	if len(resolvedPlan.Order) > 0 && len(exportPaths) > 0 {
		if err := exportStore.Write(exportPaths, environments[resolvedPlan.Order[0]]); err != nil {
			return err
		}
	}
	liveState.Exports = exportPaths
	fmt.Fprint(options.Out, resolvedPlan.Human())
	logManager, err := logs.New(id.ConfigDirectory, options.Out, false)
	if err != nil {
		return err
	}
	defer logManager.Close()
	sinks := make(map[string]command.OutputSink, len(resolvedPlan.Order))
	for _, name := range resolvedPlan.Order {
		sink, sinkErr := logManager.Sink(name, resolvedPlan.Services[name].Logs)
		if sinkErr != nil {
			return sinkErr
		}
		sinks[name] = sink
	}
	liveState.LogPaths = logManager.Paths()
	_ = stateStore.WriteLive(liveState)
	runtime := env.Runtime{Project: id.Project, Branch: id.Branch, Scope: id.Scope, Ports: portValues, Routes: routes}
	if len(resolvedPlan.Order) > 1 {
		var routeManager *routeleases.Manager
		if resolvedPlan.Routes.Daemon.Available && len(resolvedPlan.Routes.Routes) > 0 {
			ttl := time.Duration(resolvedPlan.Routes.Daemon.DefaultTTL) * time.Second
			if ttl <= 0 {
				ttl = 5 * time.Minute
			}
			interval := ttl / 3
			if interval <= 0 {
				interval = time.Second
			}
			routeManager, err = routeleases.NewManager(daemonClient, ttl, interval)
			if err != nil {
				return err
			}
		}
		result, runErr := supervisor.Run(ctx, resolvedPlan, supervisor.Options{
			Environments: environments, Runtime: runtime, Out: options.Out, ErrOut: options.ErrOut,
			RouteManager: routeManager, RoutePlans: resolvedPlan.Routes.Routes,
			SinkFactory: func(name string) command.OutputSink { return sinks[name] },
		})
		if runErr != nil {
			return runErr
		}
		for _, state := range result.States {
			if state == supervisor.StateFailed {
				return errors.New("development session service failed")
			}
		}
		return nil
	}
	serviceName := resolvedPlan.Order[0]
	service := resolvedPlan.Services[serviceName]
	values := environments[serviceName]
	commandValues, shellValue, err := expandCommand(service, values, runtime)
	if err != nil {
		return fmt.Errorf("service %q command: %w", serviceName, err)
	}
	sink := sinks[serviceName]
	process, err := (command.Runner{}).Start(ctx, command.Spec{
		Command: commandValues, Shell: shellValue, Dir: service.WorkingDir,
		Env: sessionEnvironment(values, id, routes, serviceName), Sink: sink,
	})
	if err != nil {
		return fmt.Errorf("start service %q: %w", serviceName, err)
	}

	readyCtx, cancelReady := context.WithCancel(ctx)
	defer cancelReady()
	go func() {
		select {
		case <-process.Done():
			cancelReady()
		case <-readyCtx.Done():
		}
	}()
	readyResult, readyErr := readiness.Check(readyCtx, service.Ready, readiness.Options{
		Expand:     func(value string) (string, error) { return values.ExpandRuntime(value, runtime) },
		WorkingDir: service.WorkingDir, Environment: sessionEnvironment(values, id, routes, serviceName),
	})
	if readyErr != nil {
		_ = process.Terminate(syscall.SIGTERM, shutdownGrace(service))
		return fmt.Errorf("service %q readiness failed after %d attempts (%s): %w", serviceName, readyResult.Attempts, readyResult.LastProbe, readyErr)
	}

	var lease *daemon.LeaseManager
	var stopHeartbeat context.CancelFunc
	if item, ok := routeForService(routes, serviceName); ok && item.Available {
		ttl := time.Duration(routes.Daemon.DefaultTTL) * time.Second
		if ttl <= 0 {
			ttl = 5 * time.Minute
		}
		interval := ttl / 3
		if interval <= 0 {
			interval = time.Second
		}
		lease, err = daemon.NewLeaseManager(daemonClient, daemon.RouteRequest{Project: item.Project, Branch: item.Branch, Port: item.Port, TTL: ttl}, interval)
		if err != nil {
			_ = process.Terminate(syscall.SIGTERM, shutdownGrace(service))
			return err
		}
		if err := lease.Start(ctx); err != nil {
			if item.Optional {
				fmt.Fprintf(options.ErrOut, "warning: optional route for %s unavailable: %v\n", serviceName, err)
				lease = nil
			} else {
				_ = process.Terminate(syscall.SIGTERM, shutdownGrace(service))
				return fmt.Errorf("register required route for %q: %w", serviceName, err)
			}
		} else {
			heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
			stopHeartbeat = cancelHeartbeat
			go func() { _ = lease.Run(heartbeatCtx) }()
			fmt.Fprintf(options.Out, "public URL: %s\n", item.URL)
		}
	}

	processErr := waitForProcess(ctx, process)
	if stopHeartbeat != nil {
		stopHeartbeat()
	}
	if ctx.Err() != nil {
		_ = process.Terminate(syscall.SIGTERM, shutdownGrace(service))
	} else if processErr == nil {
		// A process-completing service exiting successfully is still unexpected;
		// completion policy is enforced here rather than in the runner.
		if service.Completion == "" || service.Completion == "process" {
			processErr = errors.New("process exited unexpectedly with status 0")
		}
	}
	if lease != nil {
		if releaseErr := lease.Stop(context.Background()); releaseErr != nil && processErr == nil {
			processErr = fmt.Errorf("release route: %w", releaseErr)
		}
	}
	if processErr != nil {
		return fmt.Errorf("service %q failed: %w", serviceName, processErr)
	}
	return nil
}

func waitForProcess(ctx context.Context, process *command.Process) error {
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return nil
	}
}

func routeForService(routes plan.Routes, service string) (plan.Route, bool) {
	for _, item := range routes.Routes {
		if item.Service == service {
			return item, true
		}
	}
	return plan.Route{}, false
}

func environmentFromProcess() map[string]string {
	result := make(map[string]string)
	for _, item := range os.Environ() {
		if index := strings.IndexByte(item, '='); index > 0 {
			result[item[:index]] = item[index+1:]
		}
	}
	return result
}

func environmentList(values env.Values) []string {
	mapValues := values.Map(true)
	keys := make([]string, 0, len(mapValues))
	for key := range mapValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+mapValues[key])
	}
	return result
}

func sessionEnvironment(values env.Values, id identity.Identity, routes plan.Routes, service string) []string {
	base := environmentList(values)
	replacements := map[string]string{
		"WEBPORT_PROJECT": id.Project,
		"WEBPORT_BRANCH":  id.Branch,
		"WEBPORT_ROUTE":   id.Project + ":" + id.Branch,
	}
	if item, ok := routeForService(routes, service); ok {
		if item.Host != "" {
			replacements["WEBPORT_HOST"] = item.Host
			replacements["WEBPORT_URL"] = item.URL
		}
		if item.Port > 0 {
			replacements["WEBPORT_APP_PORT"] = fmt.Sprint(item.Port)
		}
	}
	return replaceEnvironment(base, replacements)
}

func replaceEnvironment(base []string, replacements map[string]string) []string {
	result := make([]string, 0, len(base)+len(replacements))
	for _, item := range base {
		name, _, found := strings.Cut(item, "=")
		if found {
			if _, replace := replacements[name]; replace {
				continue
			}
		}
		result = append(result, item)
	}
	keys := make([]string, 0, len(replacements))
	for name := range replacements {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		result = append(result, name+"="+replacements[name])
	}
	return result
}

func expandCommand(service plan.Service, values env.Values, runtime env.Runtime) ([]string, string, error) {
	commandValues := make([]string, len(service.Command))
	for index, value := range service.Command {
		expanded, err := values.ExpandRuntime(value, runtime)
		if err != nil {
			return nil, "", fmt.Errorf("argument %d: %w", index, err)
		}
		commandValues[index] = expanded
	}
	shellValue := service.Shell
	if shellValue != "" {
		var err error
		shellValue, err = values.ExpandRuntime(shellValue, runtime)
		if err != nil {
			return nil, "", fmt.Errorf("shell: %w", err)
		}
	}
	return commandValues, shellValue, nil
}

func filepathForSecretStore(id identity.Identity) string {
	return id.ConfigDirectory + string(os.PathSeparator) + ".webport" + string(os.PathSeparator) + "secrets.json"
}

func shutdownGrace(service plan.Service) time.Duration {
	if service.Shutdown != nil && service.Shutdown.GracePeriod.Duration() > 0 {
		return service.Shutdown.GracePeriod.Duration()
	}
	return 5 * time.Second
}
