// Package session connects configured service lifecycles.
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
	"github.com/webportdev/webport/internal/devsession/ports"
	"github.com/webportdev/webport/internal/devsession/readiness"
	routeleases "github.com/webportdev/webport/internal/devsession/routes"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/devsession/supervisor"
	"github.com/webportdev/webport/internal/discovery"
)

type Options struct {
	CurrentDir       string
	ConfigPath       string
	Profile          string
	Service          string
	API              string
	In               io.Reader
	Out              io.Writer
	ErrOut           io.Writer
	StopSignal       func() os.Signal
	PreferLive       bool
	ShowSensitive    bool
	IncludeInherited bool
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
	inspectionPlan := inspectEnvironments(resolvedPlan, environments, true)
	managedInspectionPlan := inspectEnvironments(resolvedPlan, environments, false)
	runtime := runtimeFor(resolvedPlan)
	selectedConfig, err := plan.Select(cfg, plan.BuildOptions{Profile: options.Profile, Service: options.Service})
	if err != nil {
		return err
	}
	// Acquire planMu before environmentMu whenever both protect a read or write.
	var planMu, portMu, environmentMu sync.RWMutex
	beforeStart := func(startCtx context.Context, serviceName string) (supervisor.StartPlan, error) {
		planMu.Lock()
		defer planMu.Unlock()
		portMu.Lock()
		for name, allocation := range resolvedPlan.Ports {
			if allocation.Discovered {
				if port := runtime.Ports[name]; port > 0 {
					allocation.Port = port
					resolvedPlan.Ports[name] = allocation
					for index := range resolvedPlan.Routes.Routes {
						if resolvedPlan.Routes.Routes[index].PortName == name {
							resolvedPlan.Routes.Routes[index].Port = port
						}
					}
				}
			}
		}
		var names []string
		for portName, owner := range resolvedPlan.PortOwners {
			if owner == serviceName && !resolvedPlan.Ports[portName].Discovered {
				names = append(names, portName)
			}
		}
		updated, err := ports.Reallocate(startCtx, selectedConfig.Ports, resolvedPlan.Ports, names, ports.Options{})
		if err != nil {
			portMu.Unlock()
			return supervisor.StartPlan{}, fmt.Errorf("recheck ports for %s: %w", serviceName, err)
		}
		resolvedPlan.Ports = updated
		for name, allocation := range updated {
			if allocation.Discovered {
				delete(runtime.Ports, name)
				continue
			}
			runtime.Ports[name] = allocation.Port
		}
		for index := range resolvedPlan.Routes.Routes {
			if allocation, ok := updated[resolvedPlan.Routes.Routes[index].PortName]; ok && !allocation.Discovered {
				resolvedPlan.Routes.Routes[index].Port = allocation.Port
			}
		}
		currentRuntime := runtimeFor(resolvedPlan)
		for name, port := range runtime.Ports {
			currentRuntime.Ports[name] = port
		}
		for name := range currentRuntime.DeferredPorts {
			if currentRuntime.Ports[name] > 0 {
				delete(currentRuntime.DeferredPorts, name)
			}
		}
		portMu.Unlock()
		var values env.Values
		environmentMu.RLock()
		values = environments[serviceName]
		environmentMu.RUnlock()
		if item, ok := routeForService(resolvedPlan.Routes, serviceName); ok {
			port := ""
			if item.Port > 0 {
				port = fmt.Sprint(item.Port)
			}
			values = values.With(map[string]string{"WEBPORT_APP_PORT": port})
		}
		environmentMu.Lock()
		environments[serviceName] = values
		environmentMu.Unlock()
		service := resolvedPlan.Services[serviceName]
		service.Endpoints, err = readiness.ResolveEndpoints(selectedConfig.Services[serviceName].Endpoints, func(value string) (string, error) {
			return values.ExpandRuntime(value, currentRuntime)
		})
		if err != nil {
			return supervisor.StartPlan{}, fmt.Errorf("service %q endpoints: %w", serviceName, err)
		}
		var discoverNames []string
		for portName, owner := range resolvedPlan.PortOwners {
			if owner == serviceName && resolvedPlan.Ports[portName].Discovered {
				discoverNames = append(discoverNames, portName)
			}
		}
		return supervisor.StartPlan{
			Service: service, Values: values, Runtime: currentRuntime,
			RoutePlans: append([]plan.Route(nil), resolvedPlan.Routes.Routes...), DiscoverPorts: discoverNames,
		}, nil
	}
	discover := func(discoverCtx context.Context, serviceName string, pid int, names []string) (map[string]int, error) {
		if len(names) != 1 {
			return nil, fmt.Errorf("service %q owns %d discovered ports; one listener per service is supported", serviceName, len(names))
		}
		ctx, cancel := context.WithTimeout(discoverCtx, 30*time.Second)
		defer cancel()
		scanner := discovery.Scanner{IncludeSessionManaged: true, ProbeTimeout: 250 * time.Millisecond}
		for {
			port, err := scanner.ScanProcess(ctx, pid)
			if err == nil {
				return map[string]int{names[0]: port}, nil
			}
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("service %q: %w", serviceName, ctx.Err())
			case <-timer.C:
			}
		}
	}
	live := state.LiveState{
		SessionID: id.SessionID, Worktree: id.WorktreeRoot, Profile: resolvedPlan.Profile,
		StartedAt: time.Now(), Ports: copyIntMap(runtime.Ports), Services: make(map[string]state.ServiceState),
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
	startupPrinted := false
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
	controlPath := strings.TrimSuffix(stateStore.LivePath, ".live.json") + ".sock"
	control, err := state.StartControl(controlPath, func(_ context.Context, request state.Request) state.Response {
		showSensitive, _ := request.Payload["show_sensitive"].(bool)
		includeInherited, _ := request.Payload["include_inherited"].(bool)
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
			selectedPlan := managedInspectionPlan
			if includeInherited {
				selectedPlan = inspectionPlan
			}
			if structured, _ := request.Payload["structured"].(bool); structured {
				value := selectedPlan
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
			encoded, err := selectedPlan.JSON(showSensitive)
			if err != nil {
				return state.Response{Error: err.Error()}
			}
			var payload map[string]any
			_ = json.Unmarshal(encoded, &payload)
			return state.Response{OK: true, Payload: payload}
		case "env":
			planMu.RLock()
			environmentMu.RLock()
			values := sessionEnvironment(resolvedPlan, environments)
			environmentMu.RUnlock()
			currentRuntime := runtimeFor(resolvedPlan)
			planMu.RUnlock()
			if resolved, resolveErr := values.ExpandRuntimeValues(currentRuntime); resolveErr == nil {
				values = resolved
			}
			if !includeInherited {
				values = values.ManagedOnly()
			}
			return state.Response{OK: true, Payload: map[string]any{"values": values.Map(showSensitive)}}
		case "inspect":
			planMu.RLock()
			currentRuntime := runtimeFor(resolvedPlan)
			order := append([]string(nil), resolvedPlan.Order...)
			planMu.RUnlock()
			serviceValues := make(map[string]map[string]string, len(order))
			environmentMu.RLock()
			for _, name := range order {
				values := environments[name]
				if resolved, resolveErr := values.ExpandRuntimeValues(currentRuntime); resolveErr == nil {
					values = resolved
				}
				if !includeInherited {
					values = values.ManagedOnly()
				}
				serviceValues[name] = values.Map(showSensitive)
			}
			environmentMu.RUnlock()
			routes := make([]InspectionRoute, 0)
			if routeManager != nil {
				routes = activeInspectionRoutes(routeManager.Snapshot())
			}
			return state.Response{OK: true, Payload: map[string]any{"inspection": LiveInspection{
				Project: id.Project, Branch: id.Branch, Worktree: id.WorktreeRoot,
				Profile: resolvedPlan.Profile, Routes: routes, Environment: serviceValues,
			}}}
		case "exec-info":
			name, _ := request.Payload["service"].(string)
			service, ok := resolvedPlan.Services[name]
			if !ok {
				return state.Response{Status: 404, Error: "unknown service"}
			}
			environmentMu.RLock()
			values := environments[name]
			environmentMu.RUnlock()
			planMu.RLock()
			currentRuntime := runtimeFor(resolvedPlan)
			planMu.RUnlock()
			if resolved, resolveErr := values.ExpandRuntimeValues(currentRuntime); resolveErr == nil {
				values = resolved
			}
			return state.Response{OK: true, Payload: map[string]any{"working_dir": service.WorkingDir, "environment": values.Map(true)}}
		default:
			return state.Response{Status: 404, Error: "unknown control operation"}
		}
	})
	if err != nil {
		return err
	}
	live.ControlPath, live.ControlToken = control.Path(), control.Token()
	last := state.LastSession{SessionID: id.SessionID, Worktree: id.WorktreeRoot, Profile: resolvedPlan.Profile, StartedAt: live.StartedAt}
	retainLast := cfg.Session.RetainLastSummary == nil || *cfg.Session.RetainLastSummary
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
		if retainLast {
			runErr = errors.Join(runErr, stateStore.WriteLast(last))
		} else {
			runErr = errors.Join(runErr, stateStore.RemoveLast())
		}
		runErr = errors.Join(runErr, stateStore.RemoveLive())
		if !retainLast {
			runErr = errors.Join(runErr, stateStore.RemoveSessionLogs(id.SessionID))
		}
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
		exported := sessionEnvironment(resolvedPlan, environments)
		if resolved, resolveErr := exported.ExpandRuntimeValues(runtimeFor(resolvedPlan)); resolveErr == nil {
			exported = resolved
		}
		if err := exportStore.Write(exportPaths, exported); err != nil {
			return err
		}
	}
	live.Exports = exportPaths
	fmt.Fprint(options.Out, resolvedPlan.Human())
	sessionLogDirectory, err := stateStore.SessionLogDirectory(id.SessionID)
	if err != nil {
		return err
	}
	logManager, err := logs.New(id.ConfigDirectory, sessionLogDirectory, options.Out, false)
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
	if err := stateStore.PruneSessionLogs(id.SessionID); err != nil {
		return err
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
		RouteManager: routeManager, RoutePlans: append([]plan.Route(nil), resolvedPlan.Routes.Routes...),
		StopSignal:  options.StopSignal,
		BeforeStart: beforeStart, Discover: discover, PortMu: &portMu, EnvironmentMu: &environmentMu,
		SinkFactory: func(name string) command.OutputSink { return sinks[name] },
		OnEvent: func(event supervisor.Event) {
			liveMu.Lock()
			defer liveMu.Unlock()
			planMu.Lock()
			for name, port := range event.Ports {
				if allocation, ok := resolvedPlan.Ports[name]; ok {
					allocation.Port = port
					resolvedPlan.Ports[name] = allocation
				}
				live.Ports[name] = port
				for index := range resolvedPlan.Routes.Routes {
					if resolvedPlan.Routes.Routes[index].PortName == name {
						resolvedPlan.Routes.Routes[index].Port = port
					}
				}
			}
			planMu.Unlock()
			for label, endpoint := range event.Endpoints {
				live.Endpoints[event.Service+"."+label] = endpoint
			}
			if len(exportPaths) > 0 && len(event.Ports) > 0 {
				planMu.RLock()
				environmentMu.RLock()
				exported := sessionEnvironment(resolvedPlan, environments)
				environmentMu.RUnlock()
				currentRuntime := runtimeFor(resolvedPlan)
				planMu.RUnlock()
				if resolved, resolveErr := exported.ExpandRuntimeValues(currentRuntime); resolveErr == nil {
					exported = resolved
					if exportErr := exportStore.Write(exportPaths, exported); exportErr != nil {
						live.LastError = exportErr.Error()
					}
				}
			}
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
				planMu.RLock()
				item, routeAvailable := routeForService(resolvedPlan.Routes, event.Service)
				planMu.RUnlock()
				if routeAvailable && item.Available {
					fmt.Fprintf(options.Out, "public URL: %s\n", item.URL)
				}
				for label, endpoint := range event.Endpoints {
					fmt.Fprintf(options.Out, "%s %s: %s\n", event.Service, label, endpoint)
				}
				if !startupPrinted {
					ready := true
					for _, root := range resolvedPlan.Roots {
						stateValue := live.Services[root].State
						if stateValue != string(supervisor.StateReady) && stateValue != string(supervisor.StateSuccess) {
							ready = false
							break
						}
					}
					if ready {
						planMu.RLock()
						summary := startupSummary(resolvedPlan, live, &stateStore, exportPaths, cfg)
						planMu.RUnlock()
						fmt.Fprint(options.Out, summary)
						startupPrinted = true
					}
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
	if !startupPrinted {
		planMu.RLock()
		summary := startupSummary(resolvedPlan, live, &stateStore, exportPaths, cfg)
		planMu.RUnlock()
		fmt.Fprint(options.Out, summary)
	}
	return errors.Join(err, stateErr)
}

func activeInspectionRoutes(snapshot map[string]routeleases.Entry) []InspectionRoute {
	routes := make([]InspectionRoute, 0, len(snapshot))
	for name, entry := range snapshot {
		if entry.State == routeleases.Active {
			routes = append(routes, InspectionRoute{
				Service: name, Project: entry.Route.Project, Branch: entry.Route.Branch, URL: entry.Route.URL,
			})
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Service < routes[j].Service })
	return routes
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
	store, err := state.NewStore(id.WorktreeRoot, "")
	if err != nil {
		return ""
	}
	return store.SecretPath
}

func startupSummary(p plan.Plan, live state.LiveState, store *state.Store, exportPaths map[string]string, cfg config.Config) string {
	var builder strings.Builder
	builder.WriteString("development session started\n")
	fmt.Fprintf(&builder, "profile: %s\n", p.Profile)
	fmt.Fprintf(&builder, "state: %s\n", store.LivePath)
	if len(p.Ports) > 0 {
		builder.WriteString("ports:\n")
		names := make([]string, 0, len(p.Ports))
		for name := range p.Ports {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			allocation := p.Ports[name]
			if allocation.Port > 0 {
				fmt.Fprintf(&builder, "  %s: %d\n", name, allocation.Port)
			} else {
				fmt.Fprintf(&builder, "  %s: discovering\n", name)
			}
		}
	}
	builder.WriteString("services:\n")
	for _, name := range p.Order {
		service := live.Services[name]
		fmt.Fprintf(&builder, "  %s: %s", name, service.State)
		if path := live.LogPaths[name]; path != "" {
			fmt.Fprintf(&builder, " (log: %s)", path)
		}
		builder.WriteByte('\n')
	}
	for _, item := range p.Routes.Routes {
		if item.URL != "" {
			fmt.Fprintf(&builder, "route %s: %s\n", item.Service, item.URL)
		}
	}
	endpointNames := make([]string, 0, len(live.Endpoints))
	for key := range live.Endpoints {
		endpointNames = append(endpointNames, key)
	}
	sort.Strings(endpointNames)
	for _, key := range endpointNames {
		fmt.Fprintf(&builder, "endpoint %s: %s\n", key, live.Endpoints[key])
	}
	if len(exportPaths) > 0 {
		builder.WriteString("exports:\n")
		keys := make([]string, 0, len(exportPaths))
		for key := range exportPaths {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&builder, "  %s: %s\n", key, exportPaths[key])
		}
	}
	generated := generatedNames(cfg, p.Profile, p.Order)
	if len(generated) > 0 {
		fmt.Fprintf(&builder, "generated sensitive settings: %s\n", strings.Join(generated, ", "))
	}
	builder.WriteString("use `webport dev status`, `webport dev logs`, or `webport dev env` to inspect the session; press Ctrl+C to stop.\n")
	return builder.String()
}

func generatedNames(cfg config.Config, profile string, services []string) []string {
	seen := make(map[string]struct{})
	for name, value := range cfg.Env {
		if value.Generate != nil {
			seen[name] = struct{}{}
		}
	}
	for name, value := range cfg.Profiles[profile].Env {
		if value.Generate != nil {
			seen["profile."+profile+"."+name] = struct{}{}
		}
	}
	for _, serviceName := range services {
		for name, value := range cfg.Services[serviceName].Env {
			if value.Generate != nil {
				seen["service."+serviceName+"."+name] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func copyIntMap(input map[string]int) map[string]int {
	result := make(map[string]int, len(input))
	for name, value := range input {
		result[name] = value
	}
	return result
}
