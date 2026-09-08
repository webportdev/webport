// Package session connects configured service lifecycles.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/webportdev/webport/internal/devsession/command"
	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/env"
	"github.com/webportdev/webport/internal/devsession/exports"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/logs"
	"github.com/webportdev/webport/internal/devsession/plan"
	routeleases "github.com/webportdev/webport/internal/devsession/routes"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/devsession/supervisor"
)

type Options struct {
	CurrentDir    string
	ConfigPath    string
	Profile       string
	Service       string
	API           string
	In            io.Reader
	Out           io.Writer
	ErrOut        io.Writer
	StopSignal    func() os.Signal
	PreferLive    bool
	ShowSensitive bool
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
	// All services and lifecycle messages share an output lock.
	outputMu := new(sync.Mutex)
	options.Out = synchronizedWriter{outputMu, options.Out}
	options.ErrOut = synchronizedWriter{outputMu, options.ErrOut}
	cfg, err := config.Load(config.Options{CurrentDir: options.CurrentDir, ConfigPath: options.ConfigPath})
	if err != nil {
		return err
	}
	id, err := identity.Resolve(cfg, options.CurrentDir, nil)
	if err != nil {
		return err
	}
	stateStore, err := state.NewStore(id.WorktreeRoot, "")
	if err != nil {
		return err
	}
	if err := stateStore.Acquire(); err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, stateStore.Release()) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client, err := daemon.NewHTTPClient(options.API, nil, nil)
	if err != nil {
		return err
	}
	resolvedPlan, _, err := prepare(ctx, cfg, id, options, client)
	if err != nil {
		return err
	}
	environments, err := composeEnvironments(cfg, resolvedPlan, true)
	if err != nil {
		return err
	}
	inspectionPlan := inspectEnvironments(resolvedPlan, environments)
	runtime := runtimeFor(resolvedPlan)
	live := state.LiveState{
		SessionID: id.SessionID, Worktree: id.WorktreeRoot, Profile: resolvedPlan.Profile,
		StartedAt: time.Now(), Ports: runtime.Ports, Services: make(map[string]state.ServiceState),
		Routes: make(map[string]state.RouteState), Endpoints: make(map[string]string),
		PIDs: []state.ProcessIdentity{state.IdentifyProcess(os.Getpid())},
	}
	for _, name := range resolvedPlan.Order {
		live.Services[name] = state.ServiceState{State: string(supervisor.StatePending)}
		for label, endpoint := range resolvedPlan.Services[name].Endpoints {
			live.Endpoints[name+"."+label] = endpoint
		}
	}
	for _, item := range resolvedPlan.Routes.Routes {
		status := "pending"
		if !item.Available {
			status = "unavailable"
		}
		live.Routes[item.Service] = state.RouteState{State: status, Project: item.Project, Branch: item.Branch, Host: item.Host, URL: item.URL, Optional: item.Optional}
	}
	var liveMu sync.Mutex
	controlPath := strings.TrimSuffix(stateStore.LivePath, ".live.json") + ".sock"
	control, err := state.StartControl(controlPath, func(_ context.Context, request state.Request) state.Response {
		showSensitive, _ := request.Payload["show_sensitive"].(bool)
		switch request.Operation {
		case "stop":
			cancel()
			return state.Response{OK: true, Payload: map[string]any{"session_id": id.SessionID}}
		case "status":
			// Read an atomic disk snapshot rather than sharing mutable maps.
			value, err := stateStore.ReadLive()
			if err != nil {
				return state.Response{Error: err.Error()}
			}
			value.ControlToken = ""
			return state.Response{OK: true, Payload: map[string]any{"state": value}}
		case "config":
			if structured, _ := request.Payload["structured"].(bool); structured {
				value := inspectionPlan
				if !showSensitive {
					value.Services = cloneServices(value.Services)
					hide := func(values map[string]config.Value) map[string]config.Value {
						result := make(map[string]config.Value, len(values))
						for name, item := range values {
							if item.Sensitive {
								redacted := "<redacted>"
								item.Literal = &redacted
							}
							result[name] = item
						}
						return result
					}
					value.Environment = hide(value.Environment)
					for name, service := range value.Services {
						service.Env = hide(service.Env)
						value.Services[name] = service
					}
				}
				return state.Response{OK: true, Payload: map[string]any{"plan": value}}
			}
			encoded, err := inspectionPlan.JSON(showSensitive)
			if err != nil {
				return state.Response{Error: err.Error()}
			}
			var payload map[string]any
			_ = json.Unmarshal(encoded, &payload)
			return state.Response{OK: true, Payload: payload}
		case "env":
			return state.Response{OK: true, Payload: map[string]any{"values": environments[resolvedPlan.Roots[0]].Map(showSensitive)}}
		case "exec-info":
			name, _ := request.Payload["service"].(string)
			service, ok := resolvedPlan.Services[name]
			if !ok {
				return state.Response{Status: 404, Error: "unknown service"}
			}
			return state.Response{OK: true, Payload: map[string]any{"working_dir": service.WorkingDir, "environment": environments[name].Map(true)}}
		default:
			return state.Response{Status: 404, Error: "unknown control operation"}
		}
	})
	if err != nil {
		return err
	}
	live.ControlPath, live.ControlToken = control.Path(), control.Token()
	last := state.LastSession{SessionID: id.SessionID, Worktree: id.WorktreeRoot, Profile: resolvedPlan.Profile, StartedAt: live.StartedAt}
	defer func() {
		if options.StopSignal != nil && ctx.Err() != nil && last.Initiating == "" {
			if sig := options.StopSignal(); sig != nil {
				last.Signal = sig.String()
			}
		}
		cancel()
		runErr = errors.Join(runErr, control.Close())
		last.StoppedAt = time.Now()
		last.LogPaths, last.Endpoints, last.Routes = live.LogPaths, live.Endpoints, live.Routes
		if runErr != nil {
			if last.Initiating == "" {
				last.Initiating = runErr.Error()
			}
			if last.ExitCode == 0 {
				last.ExitCode = 1
			}
		}
		if cfg.Session.RetainLastSummary == nil || *cfg.Session.RetainLastSummary {
			runErr = errors.Join(runErr, stateStore.WriteLast(last))
		}
		runErr = errors.Join(runErr, stateStore.RemoveLive())
	}()
	exportPaths := make(map[string]string)
	for shell, path := range cfg.Session.Exports {
		resolved, err := id.ResolvePath(path)
		if err != nil {
			return err
		}
		exportPaths[shell] = resolved
	}
	exportStore, err := exports.New(exportPaths)
	if err != nil {
		return err
	}
	defer exportStore.Remove()
	if len(exportPaths) > 0 {
		if err := exportStore.Write(exportPaths, environments[resolvedPlan.Roots[0]]); err != nil {
			return err
		}
	}
	live.Exports = exportPaths
	fmt.Fprint(options.Out, resolvedPlan.Human())
	logManager, err := logs.New(id.ConfigDirectory, options.Out, false)
	if err != nil {
		return err
	}
	defer logManager.Close()
	sinks := make(map[string]command.OutputSink)
	for _, name := range resolvedPlan.Order {
		sink, err := logManager.Sink(name, resolvedPlan.Services[name].Logs)
		if err != nil {
			return err
		}
		sinks[name] = sink
	}
	live.LogPaths = logManager.Paths()
	if err := stateStore.WriteLive(live); err != nil {
		return err
	}
	var routeManager *routeleases.Manager
	if resolvedPlan.Routes.Daemon.Available && len(resolvedPlan.Routes.Routes) > 0 {
		ttl := time.Duration(resolvedPlan.Routes.Daemon.DefaultTTL) * time.Second
		if ttl <= 0 {
			ttl = 5 * time.Minute
		}
		routeManager, err = routeleases.NewManager(client, ttl, ttl/3)
		if err != nil {
			return err
		}
	}
	refreshRoutes := func() {
		if routeManager == nil {
			return
		}
		for name, entry := range routeManager.Snapshot() {
			value := live.Routes[name]
			value.State = string(entry.State)
			live.Routes[name] = value
			if entry.Error != "" {
				live.LastError = entry.Error
			}
		}
	}
	var stateErr error
	writeLive := func() {
		refreshRoutes()
		if err := stateStore.WriteLive(live); err != nil {
			stateErr = errors.Join(stateErr, err)
			cancel()
		}
	}
	// Route recovery can change independently of process state.
	pollCtx, stopPoll := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				liveMu.Lock()
				writeLive()
				liveMu.Unlock()
			}
		}
	}()
	result, err := supervisor.Run(ctx, resolvedPlan, supervisor.Options{
		Environments: environments, Runtime: runtime, Out: options.Out, ErrOut: options.ErrOut,
		RouteManager: routeManager, RoutePlans: resolvedPlan.Routes.Routes,
		StopSignal:  options.StopSignal,
		SinkFactory: func(name string) command.OutputSink { return sinks[name] },
		OnEvent: func(event supervisor.Event) {
			liveMu.Lock()
			defer liveMu.Unlock()
			value := live.Services[event.Service]
			if event.Process.PID != 0 && value.PID == 0 {
				identity := state.IdentifyProcess(event.Process.PID)
				if identity.StartTime != "" {
					live.PIDs = append(live.PIDs, identity)
				}
			}
			value.State, value.PID = string(event.State), event.Process.PID
			value.StartedAt, value.EndedAt = event.Process.StartTime, event.Process.EndTime
			value.Ready = event.State == supervisor.StateReady || event.State == supervisor.StateSuccess
			if event.Error != nil {
				value.LastError, live.LastError = event.Error.Error(), event.Error.Error()
				if last.Initiating == "" {
					last.Initiating = event.Error.Error()
					last.ExitCode, last.Signal = event.Process.ExitCode, event.Process.Signal
				}
				fmt.Fprintf(options.ErrOut, "service %s (PID %d) failed: %v; log: %s\n", event.Service, event.Process.PID, event.Error, live.LogPaths[event.Service])
				for _, line := range logManager.Tail(event.Service) {
					fmt.Fprintln(options.ErrOut, line)
				}
			}
			live.Services[event.Service] = value
			writeLive()
			if event.State == supervisor.StateReady {
				if item, ok := routeForService(resolvedPlan.Routes, event.Service); ok && item.Available {
					fmt.Fprintf(options.Out, "public URL: %s\n", item.URL)
				}
				for label, endpoint := range resolvedPlan.Services[event.Service].Endpoints {
					fmt.Fprintf(options.Out, "%s %s: %s\n", event.Service, label, endpoint)
				}
			}
		},
	})
	stopPoll()
	<-pollDone
	refreshRoutes()
	last.FinalStates = make(map[string]string)
	last.CleanupErrors = result.CleanupErrors
	for name, value := range result.States {
		last.FinalStates[name] = string(value)
	}
	return errors.Join(err, stateErr)
}

type synchronizedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w synchronizedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
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

func sessionValues(id identity.Identity, routes plan.Routes, service string) map[string]string {
	replacements := map[string]string{
		"WEBPORT_PROJECT": id.Project,
		"WEBPORT_BRANCH":  id.Branch,
		"WEBPORT_ROUTE":   id.Project + ":" + id.Branch,
		// Generic route context must not opt this child into independent
		// daemon discovery before the supervisor publishes its ready lease.
		"WEBPORT_SESSION_MANAGED": "1",
		"WEBPORT_HOST":            "", "WEBPORT_URL": "", "WEBPORT_APP_PORT": "",
	}
	if item, ok := routeForService(routes, service); ok {
		replacements["WEBPORT_PROJECT"] = item.Project
		replacements["WEBPORT_BRANCH"] = item.Branch
		replacements["WEBPORT_ROUTE"] = item.Project + ":" + item.Branch
		if item.Host != "" {
			replacements["WEBPORT_HOST"] = item.Host
			replacements["WEBPORT_URL"] = item.URL
		}
		if item.Port > 0 {
			replacements["WEBPORT_APP_PORT"] = fmt.Sprint(item.Port)
		}
	}
	return replacements
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
