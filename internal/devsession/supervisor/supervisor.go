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
	Service string
	State   State
	Error   error
	At      time.Time
	Process command.Result
}

type Result struct {
	States        map[string]State
	Events        []Event
	CleanupErrors []string
}

type Options struct {
	Environments map[string]env.Values
	Runtime      env.Runtime
	Out          io.Writer
	ErrOut       io.Writer
	SinkFactory  func(string) command.OutputSink
	Runner       command.Runner
	RouteManager *routes.Manager
	RoutePlans   []plan.Route
	OnEvent      func(Event)
	StopSignal   func() os.Signal
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
			options.OnEvent(update)
		}
		send(name, state, err)
	}
	values := options.Environments[name]
	commandValues, shellValue, err := expandCommand(service, values, options.Runtime)
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
		options.OnEvent(Event{Service: name, State: StateStarting, At: time.Now(), Process: process.Snapshot()})
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
			Expand:     func(value string) (string, error) { return values.ExpandRuntime(value, options.Runtime) },
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
		if err := activateRoute(ctx, name, options); err != nil {
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
		Expand:     func(value string) (string, error) { return values.ExpandRuntime(value, options.Runtime) },
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
	if err := activateRoute(ctx, name, options); err != nil {
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
		values := options.Environments[name]
		commandValues, shellValue, err := expandCommand(plan.Service{Command: service.Shutdown.Command, Shell: ""}, values, options.Runtime)
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
		combined = errors.Join(combined, options.RouteManager.ReleaseAll(context.Background()))
	}
	return errors.Join(combined, runShutdownCommands(registered, sessionPlan, options))
}

func activateRoute(ctx context.Context, service string, options Options) error {
	if options.RouteManager == nil {
		return nil
	}
	for _, item := range options.RoutePlans {
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
