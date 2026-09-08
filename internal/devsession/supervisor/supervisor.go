// Package supervisor schedules the immutable plan and owns concurrent service
// startup. Cleanup policy is centralized here rather than in the command
// runner.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/webportdev/webport/internal/devsession/command"
	"github.com/webportdev/webport/internal/devsession/env"
	"github.com/webportdev/webport/internal/devsession/plan"
	"github.com/webportdev/webport/internal/devsession/readiness"
	"github.com/webportdev/webport/internal/devsession/routes"
)

type State string

const (
	StatePending  State = "pending"
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateSuccess  State = "success"
	StateFailed   State = "failed"
	StateBlocked  State = "blocked"
	StateStopped  State = "stopped"
)

type Event struct {
	Service   string
	State     State
	Error     error
	At        time.Time
	Process   command.Result
	Ports     map[string]int
	Endpoints map[string]string
}

type Result struct {
	States        map[string]State
	Events        []Event
	CleanupErrors []string
}

type Options struct {
	Environments        map[string]env.Values
	Runtime             env.Runtime
	Out                 io.Writer
	ErrOut              io.Writer
	SinkFactory         func(string) command.OutputSink
	Runner              command.Runner
	RouteManager        *routes.Manager
	RoutePlans          []plan.Route
	RouteReleaseTimeout time.Duration
	OnEvent             func(Event)
	StopSignal          func() os.Signal
	BeforeStart         func(context.Context, string) (StartPlan, error)
	Discover            func(context.Context, string, int, []string) (map[string]int, error)
	PortMu              *sync.RWMutex
	EnvironmentMu       *sync.RWMutex
}

type StartPlan struct {
	Service       plan.Service
	Values        env.Values
	Runtime       env.Runtime
	RoutePlans    []plan.Route
	DiscoverPorts []string
}

type event struct {
	service          string
	state            State
	err              error
	registerShutdown bool
	routeFailure     *routes.Failure
}

func Run(ctx context.Context, sessionPlan plan.Plan, options Options) (result Result, runErr error) {
	var cleanupErrors []string
	defer func() { result.CleanupErrors = cleanupErrors }()
	cleanup := func(registered map[string]struct{}) error {
		err := releaseRoutesAndShutdown(registered, sessionPlan, options)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err.Error())
		}
		return err
	}
	if options.Out == nil {
		options.Out = io.Discard
	}
	if options.ErrOut == nil {
		options.ErrOut = io.Discard
	}
	states := make(map[string]State, len(sessionPlan.Services))
	for name := range sessionPlan.Services {
		states[name] = StatePending
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan event, len(sessionPlan.Services)*4+1)
	active := make(map[string]struct{})
	var primaryErr error
	var eventLog []Event
	externalDone := ctx.Done()
	registeredShutdowns := make(map[string]struct{})
	cleanupDone := false
	var routeFailures <-chan routes.Failure
	if options.RouteManager != nil {
		routeFailures = options.RouteManager.Failures()
		go func() {
			for {
				select {
				case failure := <-routeFailures:
					select {
					case events <- event{service: failure.Service, routeFailure: &failure}:
					case <-runCtx.Done():
						return
					}
				case <-runCtx.Done():
					return
				}
			}
		}()
	}

	emit := func(service string, state State, err error) {
		events <- event{service: service, state: state, err: err}
	}
	start := func(name string) {
		states[name] = StateStarting
		active[name] = struct{}{}
		emit(name, StateStarting, nil)
		go runService(runCtx, name, sessionPlan.Services[name], options, emit, func() {
			events <- event{service: name, registerShutdown: true}
		})
	}

	for {
		if primaryErr == nil && runCtx.Err() == nil {
			for _, name := range sessionPlan.Order {
				if states[name] != StatePending {
					continue
				}
				if dependencyFailed(sessionPlan.Services[name], states) {
					states[name] = StateBlocked
					eventLog = append(eventLog, Event{Service: name, State: StateBlocked, At: time.Now()})
					continue
				}
				if dependenciesReady(sessionPlan.Services[name], states) {
					start(name)
				}
			}
		}
		if len(active) == 0 {
			if primaryErr != nil {
				if !cleanupDone {
					cleanupDone = true
					if cleanupErr := cleanup(registeredShutdowns); cleanupErr != nil {
						primaryErr = errors.Join(primaryErr, cleanupErr)
					}
				}
				for name, state := range states {
					if state == StatePending {
						states[name] = StateBlocked
						eventLog = append(eventLog, Event{Service: name, State: StateBlocked, At: time.Now()})
					}
				}
				cancel()
				return Result{States: states, Events: eventLog}, primaryErr
			}
			if ctx.Err() != nil {
				if !cleanupDone {
					cleanupDone = true
					if cleanupErr := cleanup(registeredShutdowns); cleanupErr != nil {
						return Result{States: states, Events: eventLog}, cleanupErr
					}
				}
				cancel()
				return Result{States: states, Events: eventLog}, nil
			}
			pending := false
			for _, state := range states {
				if state == StatePending || state == StateStarting || state == StateReady {
					pending = true
				}
			}
			if !pending {
				if len(registeredShutdowns) == 0 && (options.RouteManager == nil || !options.RouteManager.Active()) {
					return Result{States: states, Events: eventLog}, nil
				}
				// Continue through the common event loop so route failures are
				// handled even when only external resources remain.
			}
		}

		select {
		case <-externalDone:
			cancel()
			externalDone = nil
		case update := <-events:
			if update.routeFailure != nil {
				if update.routeFailure.Required && primaryErr == nil && runCtx.Err() == nil {
					primaryErr = fmt.Errorf("required route for %s failed: %w", update.service, update.routeFailure.Error)
					cancel()
				}
				continue
			}
			if update.registerShutdown {
				registeredShutdowns[update.service] = struct{}{}
				continue
			}
			if update.state == StateStarting {
				eventLog = append(eventLog, Event{Service: update.service, State: update.state, At: time.Now()})
				continue
			}
			if update.state == StateReady {
				states[update.service] = StateReady
				// Keep every worker active until its terminal event is consumed.
				eventLog = append(eventLog, Event{Service: update.service, State: update.state, At: time.Now()})
				continue
			}
			delete(active, update.service)
			states[update.service] = update.state
			eventLog = append(eventLog, Event{Service: update.service, State: update.state, Error: update.err, At: time.Now()})
			if update.state == StateFailed && primaryErr == nil && runCtx.Err() == nil {
				primaryErr = update.err
				cancel()
			}
		}
	}
}

func runService(ctx context.Context, name string, service plan.Service, options Options, emit func(string, State, error), registerShutdown func()) {
	var process *command.Process
	stopSignal := func() os.Signal {
		setting := ""
		if service.Shutdown != nil {
			setting = service.Shutdown.Signal
		}
		switch setting {
		case "SIGINT":
			return syscall.SIGINT
		case "SIGHUP":
			return syscall.SIGHUP
		case "SIGKILL":
			return syscall.SIGKILL
		case "SIGTERM":
			return syscall.SIGTERM
		default:
			if options.StopSignal != nil {
				return options.StopSignal()
			}
			return syscall.SIGTERM
		}
	}
	send := emit
	var readyEndpoints map[string]string
	var startPorts map[string]int
	emit = func(name string, state State, err error) {
		if process != nil && (state == StateStopped || state == StateFailed || state == StateSuccess) {
			_ = process.Terminate(stopSignal(), shutdownGrace(service))
		}
		if err != nil {
			err = fmt.Errorf("service %q: %w", name, err)
		}
		if options.OnEvent != nil {
			update := Event{Service: name, State: state, Error: err, At: time.Now()}
			if process != nil {
				update.Process = process.Snapshot()
			}
			update.Ports = copyPorts(startPorts)
			update.Endpoints = copyStrings(readyEndpoints)
			options.OnEvent(update)
		}
		send(name, state, err)
	}
	start := StartPlan{Service: service, Runtime: snapshotRuntime(options.Runtime, options.PortMu), RoutePlans: copyRoutePlans(options.RoutePlans)}
	if options.EnvironmentMu != nil {
		options.EnvironmentMu.RLock()
		start.Values = options.Environments[name]
		options.EnvironmentMu.RUnlock()
	} else {
		start.Values = options.Environments[name]
	}
	if options.BeforeStart != nil {
		planned, planErr := options.BeforeStart(ctx, name)
		if planErr != nil {
			emit(name, StateFailed, planErr)
			return
		}
		if planned.Service.Name != "" {
			start.Service = planned.Service
		}
		if planned.Values.Map(true) != nil {
			start.Values = planned.Values
		}
		if planned.Runtime.Project != "" {
			start.Runtime = planned.Runtime
		}
		if planned.RoutePlans != nil {
			start.RoutePlans = planned.RoutePlans
		}
		start.DiscoverPorts = append([]string(nil), planned.DiscoverPorts...)
	}
	service, values, runtime := start.Service, start.Values, start.Runtime
	var err error
	values, err = values.ExpandRuntimeValues(runtime)
	if err != nil {
		emit(name, StateFailed, err)
		return
	}
	commandValues, shellValue, err := expandCommand(service, values, runtime)
	if err != nil {
		emit(name, StateFailed, err)
		return
	}
	sink := command.OutputSink(nil)
	if options.SinkFactory != nil {
		sink = options.SinkFactory(name)
	}
	process, err = options.Runner.Start(ctx, command.Spec{
		Command: commandValues, Shell: shellValue, Dir: service.WorkingDir,
		Env: environmentList(values), Sink: sink,
		StopSignal: stopSignal, GracePeriod: shutdownGrace(service),
	})
	if err != nil {
		if ctx.Err() != nil {
			emit(name, StateStopped, nil)
			return
		}
		emit(name, StateFailed, fmt.Errorf("start service: %w", err))
		return
	}
	if options.OnEvent != nil {
		startPorts = runtimePorts(runtime)
		options.OnEvent(Event{Service: name, State: StateStarting, At: time.Now(), Process: process.Snapshot(), Ports: copyPorts(startPorts)})
	}
	if options.Discover != nil && len(start.DiscoverPorts) > 0 {
		discovered, discoverErr := options.Discover(ctx, name, process.Snapshot().PID, start.DiscoverPorts)
		if discoverErr != nil {
			_ = process.Terminate(stopSignal(), shutdownGrace(service))
			emit(name, StateFailed, fmt.Errorf("discover listener: %w", discoverErr))
			return
		}
		if len(discovered) != len(start.DiscoverPorts) {
			_ = process.Terminate(stopSignal(), shutdownGrace(service))
			emit(name, StateFailed, errors.New("listener discovery returned an incomplete port allocation"))
			return
		}
		if options.PortMu != nil {
			options.PortMu.Lock()
			for portName, port := range discovered {
				runtime.Ports[portName] = port
				delete(runtime.DeferredPorts, portName)
				options.Runtime.Ports[portName] = port
				delete(options.Runtime.DeferredPorts, portName)
			}
			options.PortMu.Unlock()
		} else {
			for portName, port := range discovered {
				runtime.Ports[portName] = port
				delete(runtime.DeferredPorts, portName)
				options.Runtime.Ports[portName] = port
				delete(options.Runtime.DeferredPorts, portName)
			}
		}
		if len(discovered) == 1 {
			for _, port := range discovered {
				values = values.With(map[string]string{"WEBPORT_APP_PORT": fmt.Sprint(port)})
			}
		}
		if options.EnvironmentMu != nil {
			options.EnvironmentMu.Lock()
			options.Environments[name] = values
			options.EnvironmentMu.Unlock()
		} else {
			options.Environments[name] = values
		}
		for portName, port := range discovered {
			for index := range start.RoutePlans {
				if start.RoutePlans[index].PortName == portName {
					start.RoutePlans[index].Port = port
				}
			}
		}
		startPorts = runtimePorts(runtime)
	}
	values, err = values.ExpandRuntimeValues(runtime)
	if err != nil {
		_ = process.Terminate(stopSignal(), shutdownGrace(service))
		emit(name, StateFailed, err)
		return
	}
	readyEndpoints = make(map[string]string, len(service.Endpoints))
	for label, endpoint := range service.Endpoints {
		resolved, endpointErr := values.ExpandRuntime(endpoint, runtime)
		if endpointErr != nil {
			_ = process.Terminate(stopSignal(), shutdownGrace(service))
			emit(name, StateFailed, fmt.Errorf("endpoint %s: %w", label, endpointErr))
			return
		}
		readyEndpoints[label] = resolved
	}
	if service.Completion == "exit" {
		processErr := process.Wait()
		if processErr == nil && service.Shutdown != nil && len(service.Shutdown.Command) > 0 {
			registerShutdown()
		}
		if ctx.Err() != nil {
			emit(name, StateStopped, nil)
			return
		}
		if processErr != nil {
			emit(name, StateFailed, fmt.Errorf("exit-completing service failed: %w", processErr))
			return
		}
		check, readyErr := readiness.Check(ctx, service.Ready, readiness.Options{
			Expand:     func(value string) (string, error) { return values.ExpandRuntime(value, runtime) },
			WorkingDir: service.WorkingDir, Environment: environmentList(values), CommandRunner: options.Runner,
		})
		if readyErr != nil {
			if ctx.Err() != nil {
				emit(name, StateStopped, nil)
				return
			}
			emit(name, StateFailed, fmt.Errorf("readiness failed after %d attempts (%s): %w", check.Attempts, check.LastProbe, readyErr))
			return
		}
		if err := activateRoute(ctx, name, options, start.RoutePlans); err != nil {
			if ctx.Err() != nil {
				emit(name, StateStopped, nil)
				return
			}
			emit(name, StateFailed, err)
			return
		}
		emit(name, StateReady, nil)
		emit(name, StateSuccess, nil)
		return
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
	check, readyErr := readiness.Check(readyCtx, service.Ready, readiness.Options{
		Expand:     func(value string) (string, error) { return values.ExpandRuntime(value, runtime) },
		WorkingDir: service.WorkingDir, Environment: environmentList(values), CommandRunner: options.Runner,
	})
	if readyErr != nil {
		if ctx.Err() != nil {
			emit(name, StateStopped, nil)
			return
		}
		_ = process.Terminate(stopSignal(), shutdownGrace(service))
		emit(name, StateFailed, fmt.Errorf("readiness failed after %d attempts (%s): %w", check.Attempts, check.LastProbe, readyErr))
		return
	}
	if err := activateRoute(ctx, name, options, start.RoutePlans); err != nil {
		_ = process.Terminate(stopSignal(), shutdownGrace(service))
		if ctx.Err() != nil {
			emit(name, StateStopped, nil)
			return
		}
		emit(name, StateFailed, err)
		return
	}
	emit(name, StateReady, nil)
	processErr := process.Wait()
	if ctx.Err() != nil {
		emit(name, StateStopped, nil)
		return
	}
	if processErr == nil {
		processErr = errors.New("process exited unexpectedly with status 0")
	}
	emit(name, StateFailed, processErr)
}

func runShutdownCommands(registered map[string]struct{}, sessionPlan plan.Plan, options Options) error {
	var cleanupErr error
	for index := len(sessionPlan.Order) - 1; index >= 0; index-- {
		name := sessionPlan.Order[index]
		if _, ok := registered[name]; !ok {
			continue
		}
		service := sessionPlan.Services[name]
		if service.Shutdown == nil || len(service.Shutdown.Command) == 0 {
			continue
		}
		var values env.Values
		if options.EnvironmentMu != nil {
			options.EnvironmentMu.RLock()
			values = options.Environments[name]
			options.EnvironmentMu.RUnlock()
		} else {
			values = options.Environments[name]
		}
		runtime := snapshotRuntime(options.Runtime, options.PortMu)
		var err error
		values, err = values.ExpandRuntimeValues(runtime)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("shutdown %s: %w", name, err))
			continue
		}
		commandValues, shellValue, err := expandCommand(plan.Service{Command: service.Shutdown.Command, Shell: ""}, values, runtime)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("shutdown %s: %w", name, err))
			continue
		}
		timeout := service.Shutdown.Timeout.Duration()
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), timeout)
		process, startErr := options.Runner.Start(cleanupCtx, command.Spec{
			Command: commandValues, Shell: shellValue, Dir: service.WorkingDir,
			Env: environmentList(values), Sink: sinkFor(options, name),
			StopSignal: func() os.Signal { return os.Kill }, GracePeriod: time.Millisecond,
		})
		if startErr == nil {
			startErr = process.Wait()
			_ = process.Terminate(os.Kill, time.Millisecond)
		}
		if cleanupCtx.Err() != nil {
			startErr = errors.Join(startErr, cleanupCtx.Err())
		}
		cancel()
		if startErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("shutdown %s: %w", name, startErr))
		}
	}
	return cleanupErr
}

func releaseRoutesAndShutdown(registered map[string]struct{}, sessionPlan plan.Plan, options Options) error {
	var combined error
	if options.RouteManager != nil {
		timeout := options.RouteReleaseTimeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), timeout)
		combined = errors.Join(combined, options.RouteManager.ReleaseAll(releaseCtx))
		cancel()
	}
	return errors.Join(combined, runShutdownCommands(registered, sessionPlan, options))
}

func activateRoute(ctx context.Context, service string, options Options, routePlans []plan.Route) error {
	if options.RouteManager == nil {
		return nil
	}
	for _, item := range routePlans {
		if item.Service == service && item.Available {
			if err := options.RouteManager.Activate(ctx, item); err != nil {
				return fmt.Errorf("activate route for %s: %w", service, err)
			}
			return nil
		}
	}
	return nil
}

func sinkFor(options Options, service string) command.OutputSink {
	if options.SinkFactory == nil {
		return nil
	}
	return options.SinkFactory(service)
}

func dependenciesReady(service plan.Service, states map[string]State) bool {
	for _, dependency := range service.DependsOn {
		if states[dependency] != StateReady && states[dependency] != StateSuccess {
			return false
		}
	}
	return true
}

func dependencyFailed(service plan.Service, states map[string]State) bool {
	for _, dependency := range service.DependsOn {
		if states[dependency] == StateFailed || states[dependency] == StateBlocked || states[dependency] == StateStopped {
			return true
		}
	}
	return false
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

func shutdownGrace(service plan.Service) time.Duration {
	if service.Shutdown != nil && service.Shutdown.GracePeriod.Duration() > 0 {
		return service.Shutdown.GracePeriod.Duration()
	}
	return 5 * time.Second
}

func snapshotRuntime(value env.Runtime, mutex *sync.RWMutex) env.Runtime {
	if mutex != nil {
		mutex.RLock()
		defer mutex.RUnlock()
	}
	ports := make(map[string]int, len(value.Ports))
	for name, port := range value.Ports {
		ports[name] = port
	}
	deferred := make(map[string]struct{}, len(value.DeferredPorts))
	for name := range value.DeferredPorts {
		deferred[name] = struct{}{}
	}
	routes := value.Routes
	routes.Routes = append([]plan.Route(nil), value.Routes.Routes...)
	return env.Runtime{Project: value.Project, Branch: value.Branch, Scope: value.Scope, Ports: ports, DeferredPorts: deferred, Routes: routes}
}

func runtimePorts(runtime env.Runtime) map[string]int {
	ports := make(map[string]int, len(runtime.Ports))
	for name, port := range runtime.Ports {
		if port > 0 {
			ports[name] = port
		}
	}
	return ports
}

func copyPorts(input map[string]int) map[string]int {
	if input == nil {
		return nil
	}
	result := make(map[string]int, len(input))
	for name, port := range input {
		result[name] = port
	}
	return result
}

func copyStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for name, value := range input {
		result[name] = value
	}
	return result
}

func copyRoutePlans(input []plan.Route) []plan.Route {
	return append([]plan.Route(nil), input...)
}
