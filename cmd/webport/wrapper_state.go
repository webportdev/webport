package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	gitdetect "github.com/webportdev/webport/cmd/webportctl/git"
	"github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/devsession/state"
)

func wrapperStore() (*state.Store, error) {
	root, err := gitdetect.FindWorktreeRoot(".")
	if err != nil {
		root, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	store, err := state.NewStore(root, "")
	if err != nil {
		return nil, err
	}
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	prefix := filepath.Join(store.Directory, "w-"+id)
	store.LivePath, store.LockPath = prefix+".live.json", prefix+".lock"
	return &store, nil
}

func startWrapperControl(store *state.Store, resolution wrapperResolution, values routeFlags, command *exec.Cmd, signals chan os.Signal, restart *atomic.Bool) (*state.ControlServer, error) {
	sessionID, err := randomToken()
	if err != nil {
		return nil, err
	}
	live := state.LiveState{
		SessionID: sessionID, Worktree: store.WorktreeRoot, Profile: "command wrapper", StartedAt: time.Now(),
		PIDs:     []state.ProcessIdentity{state.IdentifyProcess(os.Getpid()), state.IdentifyProcess(command.Process.Pid)},
		Services: map[string]state.ServiceState{"command": {State: "ready", PID: command.Process.Pid, Ready: true, StartedAt: time.Now()}},
		Routes:   map[string]state.RouteState{"command": {State: "active", Project: values.project, Branch: values.branch, Host: resolution.Host, URL: resolution.URL}},
		Ports:    map[string]int{"command": values.port},
	}
	managed := map[string]string{"WEBPORT_PROJECT": values.project, "WEBPORT_BRANCH": values.branch, "WEBPORT_ROUTE": values.project + ":" + values.branch, "WEBPORT_HOST": resolution.Host, "WEBPORT_URL": resolution.URL, "WEBPORT_APP_PORT": fmt.Sprint(values.port)}
	control, err := state.StartControl(strings.TrimSuffix(store.LivePath, ".live.json")+".sock", func(_ context.Context, request state.Request) state.Response {
		switch request.Operation {
		case "stop", "restart":
			if request.Operation == "restart" {
				restart.Store(true)
			}
			select {
			case signals <- syscall.SIGTERM:
			default:
			}
			return state.Response{OK: true}
		case "status":
			public, err := store.ReadLive()
			if err != nil {
				return state.Response{Error: err.Error()}
			}
			public.ControlToken = ""
			return state.Response{OK: true, Payload: map[string]any{"state": public}}
		case "inspect":
			environment := make(map[string]string)
			if inherited, _ := request.Payload["include_inherited"].(bool); inherited {
				for _, item := range command.Env {
					name, value, ok := strings.Cut(item, "=")
					if ok && name != "WEBPORT_CLIENT_TOKEN" && name != detachedReadyEnv {
						showSensitive, _ := request.Payload["show_sensitive"].(bool)
						if _, known := managed[name]; !known && !showSensitive {
							value = "<redacted>"
						}
						environment[name] = value
					}
				}
			} else {
				for name, value := range managed {
					environment[name] = value
				}
			}
			result := session.LiveInspection{Project: values.project, Branch: values.branch, Worktree: live.Worktree, Profile: live.Profile, Routes: []session.InspectionRoute{{Service: "command", Project: values.project, Branch: values.branch, URL: resolution.URL}}, Environment: map[string]map[string]string{"command": environment}}
			return state.Response{OK: true, Payload: map[string]any{"inspection": result}}
		default:
			return state.Response{Status: 404, Error: "unknown control operation"}
		}
	})
	if err != nil {
		return nil, err
	}
	live.ControlPath, live.ControlToken = control.Path(), control.Token()
	if err := store.WriteLive(live); err != nil {
		_ = control.Close()
		return nil, err
	}
	return control, nil
}
