package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	gitdetect "github.com/webportdev/webport/cmd/webportctl/git"
	"github.com/webportdev/webport/internal/devsession/config"
	devsession "github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
)

// liveInstances lists current-user configured sessions and command wrappers.
var liveInstances = state.ListLive

// runDevControl stops or restarts one development instance. A PROJECT:BRANCH
// target selects any instance publishing that route. Without a target, the
// current configured session is used, then the only command wrapper running
// in the current worktree.
func runDevControl(ctx context.Context, operation string, args []string, options devsession.Options, out io.Writer) error {
	if len(args) > 1 {
		return fmt.Errorf("webport dev %s accepts at most one PROJECT:BRANCH route", operation)
	}
	if len(args) == 1 {
		if options.ConfigPath != "" {
			return fmt.Errorf("webport dev %s accepts either --config or a route, not both", operation)
		}
		live, err := instanceForRoute(args[0])
		if err != nil {
			return err
		}
		return controlInstance(ctx, live, operation, args[0], out)
	}
	response, err := devsession.Control(ctx, options, operation, nil)
	if err == nil {
		if !response.OK {
			return errors.New(response.Error)
		}
		_, err = fmt.Fprintf(out, "development session %s requested\n", operation)
		return err
	}
	if options.ConfigPath != "" {
		return err
	}
	// Configured sessions need .webport.yaml in a Git worktree; wrappers do not.
	_, gitErr := gitdetect.FindWorktreeRoot(currentDirOrDot(options.CurrentDir))
	if gitErr == nil && !errors.Is(err, config.ErrNotFound) && !errors.Is(err, devsession.ErrNoLiveSession) {
		return err
	}
	live, wrapperErr := wrapperInWorktree(options.CurrentDir)
	if wrapperErr != nil {
		return wrapperErr
	}
	return controlInstance(ctx, live, operation, routeIDs(live)[0], out)
}

func controlInstance(ctx context.Context, live state.LiveState, operation, name string, out io.Writer) error {
	response, err := state.Dial(ctx, live.ControlPath, live.ControlToken, state.Request{Operation: operation})
	if err != nil {
		return fmt.Errorf("control %s: %w", name, err)
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	_, err = fmt.Fprintf(out, "development %s %s requested for %s\n", instanceKind(live), operation, name)
	return err
}

func instanceForRoute(id string) (state.LiveState, error) {
	if project, branch, ok := strings.Cut(id, ":"); !ok || project == "" || branch == "" {
		return state.LiveState{}, fmt.Errorf("route %q must use PROJECT:BRANCH", id)
	}
	instances, err := liveInstances()
	if err != nil && len(instances) == 0 {
		return state.LiveState{}, err
	}
	var matches []state.LiveState
	for _, live := range instances {
		for _, candidate := range routeIDs(live) {
			if candidate == id {
				matches = append(matches, live)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return state.LiveState{}, fmt.Errorf("no active development instance publishes %s; run webport list to see routes", id)
	case 1:
		return matches[0], nil
	default:
		return state.LiveState{}, fmt.Errorf("%d development instances publish %s (%s); stop one from its worktree or send SIGTERM to its supervisor", len(matches), id, describeInstances(matches))
	}
}

func currentDirOrDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

func wrapperInWorktree(currentDir string) (state.LiveState, error) {
	currentDir = currentDirOrDot(currentDir)
	root, err := gitdetect.FindWorktreeRoot(currentDir)
	if err != nil {
		root = currentDir
	}
	if root, err = filepath.Abs(root); err != nil {
		return state.LiveState{}, err
	}
	instances, err := liveInstances()
	if err != nil && len(instances) == 0 {
		return state.LiveState{}, err
	}
	var matches []state.LiveState
	for _, live := range instances {
		if live.Worktree == root && len(routeIDs(live)) > 0 {
			matches = append(matches, live)
		}
	}
	switch len(matches) {
	case 0:
		return state.LiveState{}, fmt.Errorf("no active development session or command wrapper in %s; pass PROJECT:BRANCH from webport list or --config PATH", root)
	case 1:
		return matches[0], nil
	}
	var names []string
	for _, live := range matches {
		names = append(names, routeIDs(live)...)
	}
	sort.Strings(names)
	return state.LiveState{}, fmt.Errorf("multiple development instances in %s (%s); choose a route: %s", root, describeInstances(matches), strings.Join(names, ", "))
}

// describeInstances names each instance by supervisor PID and worktree, so an
// ambiguous shared route can still be stopped with a signal.
func describeInstances(instances []state.LiveState) string {
	descriptions := make([]string, 0, len(instances))
	for _, live := range instances {
		pid := 0
		if len(live.PIDs) > 0 {
			pid = live.PIDs[0].PID
		}
		descriptions = append(descriptions, fmt.Sprintf("supervisor PID %d in %s", pid, live.Worktree))
	}
	sort.Strings(descriptions)
	return strings.Join(descriptions, "; ")
}

func routeIDs(live state.LiveState) []string {
	var ids []string
	for _, r := range live.Routes {
		if r.Project != "" && r.Branch != "" {
			ids = append(ids, r.Project+":"+r.Branch)
		}
	}
	sort.Strings(ids)
	return ids
}

func instanceKind(live state.LiveState) string {
	if live.Profile == wrapperProfile {
		return "command wrapper"
	}
	return "session"
}
