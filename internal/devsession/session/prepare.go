package session

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/env"
	"github.com/webportdev/webport/internal/devsession/exports"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/plan"
	"github.com/webportdev/webport/internal/devsession/ports"
	"github.com/webportdev/webport/internal/devsession/readiness"
	"github.com/webportdev/webport/internal/devsession/secrets"
)

func prepare(ctx context.Context, cfg config.Config, id identity.Identity, options Options, client daemon.Client) (plan.Plan, map[string]env.Values, error) {
	var err error
	cfg, err = plan.Select(cfg, plan.BuildOptions{Profile: options.Profile, Service: options.Service})
	if err != nil {
		return plan.Plan{}, nil, err
	}
	owners := make(map[string]string)
	portOwners := make(map[string]string)
	for name, service := range cfg.Services {
		if service.Route != nil {
			owners[service.Route.Port] = name
			portOwners[service.Route.Port] = name
		}
	}
	for portName, spec := range cfg.Ports {
		if _, exists := portOwners[portName]; exists {
			continue
		}
		for name, service := range cfg.Services {
			if referencesPort(service, portName) {
				if previous, exists := portOwners[portName]; exists && previous != name {
					return plan.Plan{}, nil, fmt.Errorf("discovered port %q is referenced by services %q and %q; its owner is ambiguous", portName, previous, name)
				}
				portOwners[portName] = name
			}
		}
		if spec.Discover && portOwners[portName] == "" {
			return plan.Plan{}, nil, fmt.Errorf("discovered port %q has no owning service", portName)
		}
		if owner := portOwners[portName]; owner != "" {
			owners[portName] = owner
		}
	}
	allocations, err := ports.Resolve(ctx, cfg.Ports, owners, ports.Options{})
	if err != nil {
		return plan.Plan{}, nil, err
	}
	portValues := make(map[string]int)
	for name, allocation := range allocations {
		if !allocation.Discovered {
			portValues[name] = allocation.Port
		}
	}
	profile := options.Profile
	if profile == "" && options.Service == "" {
		profile = "default"
	}
	primary := options.Service
	if primary == "" && len(cfg.Profiles[profile].Services) > 0 {
		primary = cfg.Profiles[profile].Services[0]
	}
	routes, err := plan.ResolveRoutes(ctx, cfg, id, client, primary, portValues)
	if err != nil {
		return plan.Plan{}, nil, err
	}
	p, err := plan.Build(cfg, id, plan.BuildOptions{Profile: options.Profile, Service: options.Service, PortValues: allocations, PortOwners: portOwners, Routes: routes})
	if err != nil {
		return plan.Plan{}, nil, err
	}
	environments, err := composeEnvironments(cfg, p, false)
	if err != nil {
		return plan.Plan{}, nil, err
	}
	runtime := runtimeFor(p)
	for _, name := range p.Order {
		service := p.Services[name]
		values := environments[name]
		args, _, err := expandCommand(service, values, runtime)
		if err != nil {
			return plan.Plan{}, nil, fmt.Errorf("service %q command: %w", name, err)
		}
		if len(args) > 0 && !hasDeferredPort(args, runtime) {
			if err := checkExecutable(args[0], service.WorkingDir); err != nil {
				return plan.Plan{}, nil, fmt.Errorf("service %q command: %w", name, err)
			}
		}
		checkArgs := func(args []string) error {
			if len(args) == 0 {
				return nil
			}
			expanded, _, err := expandCommand(plan.Service{Command: args}, values, runtime)
			if err != nil {
				return err
			}
			return checkExecutable(expanded[0], service.WorkingDir)
		}
		if service.Shutdown != nil && !hasDeferredPort(service.Shutdown.Command, runtime) {
			if err := checkArgs(service.Shutdown.Command); err != nil {
				return plan.Plan{}, nil, fmt.Errorf("service %q shutdown: %w", name, err)
			}
		}
		if service.Ready != nil {
			ready := service.Ready
			if err := checkArgs(ready.Command); err != nil {
				return plan.Plan{}, nil, fmt.Errorf("service %q readiness: %w", name, err)
			}
			if ready.TCP != "" && !hasDeferredPort([]string{ready.TCP}, runtime) {
				endpoint, err := values.ExpandRuntime(ready.TCP, runtime)
				if err == nil {
					_, _, err = net.SplitHostPort(endpoint)
				}
				if err != nil {
					return plan.Plan{}, nil, fmt.Errorf("service %q: invalid TCP readiness", name)
				}
			}
			if ready.HTTP != nil && !hasDeferredPort([]string{ready.HTTP.URL}, runtime) {
				endpoint, err := values.ExpandRuntime(ready.HTTP.URL, runtime)
				if err != nil {
					return plan.Plan{}, nil, fmt.Errorf("service %q HTTP readiness: %w", name, err)
				}
				u, err := url.Parse(endpoint)
				if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
					return plan.Plan{}, nil, fmt.Errorf("service %q: invalid HTTP readiness URL", name)
				}
				if _, err := http.NewRequest(ready.HTTP.Method, endpoint, nil); err != nil {
					return plan.Plan{}, nil, fmt.Errorf("service %q: invalid HTTP readiness method", name)
				}
				for _, value := range ready.HTTP.Headers {
					if _, err := values.ExpandRuntime(value, runtime); err != nil {
						return plan.Plan{}, nil, fmt.Errorf("service %q readiness header: %w", name, err)
					}
				}
				for _, code := range ready.HTTP.Status {
					if code < 100 || code > 599 {
						return plan.Plan{}, nil, fmt.Errorf("service %q: invalid HTTP readiness status", name)
					}
				}
			}
		}
		service.Endpoints, err = readiness.ResolveEndpoints(service.Endpoints, func(s string) (string, error) { return values.ExpandRuntime(s, runtime) })
		if err != nil {
			return plan.Plan{}, nil, fmt.Errorf("service %q: %w", name, err)
		}
		p.Services[name] = service
	}
	if _, err := exports.New(cfg.Session.Exports); err != nil {
		return plan.Plan{}, nil, err
	}
	return p, environments, nil
}

func composeEnvironments(cfg config.Config, p plan.Plan, generate bool) (map[string]env.Values, error) {
	store, err := secrets.NewStore(filepathForSecretStore(p.Identity))
	if err != nil {
		return nil, err
	}
	resolve := func(input map[string]config.Value, prefix string) (map[string]config.Value, error) {
		if !generate {
			return secrets.PreviewValues(input)
		}
		named := make(map[string]config.Value)
		for name, value := range input {
			named[prefix+name] = value
		}
		resolved, err := secrets.ResolveValues(named, store, nil)
		if err != nil {
			return nil, err
		}
		result := make(map[string]config.Value)
		for name, value := range resolved {
			result[strings.TrimPrefix(name, prefix)] = value
		}
		return result, nil
	}
	top, err := resolve(cfg.Env, "")
	if err != nil {
		return nil, err
	}
	profile, err := resolve(cfg.Profiles[p.Profile].Env, "profile."+p.Profile+".")
	if err != nil {
		return nil, err
	}
	var dotenv []string
	for _, path := range cfg.Session.EnvFiles {
		resolved, err := p.Identity.ResolvePath(path)
		if err != nil {
			return nil, err
		}
		dotenv = append(dotenv, resolved)
	}
	runtime := runtimeFor(p)
	result := make(map[string]env.Values)
	for _, name := range p.Order {
		service, err := resolve(cfg.Services[name].Env, "service."+name+".")
		if err != nil {
			return nil, err
		}
		values, err := env.Resolve(env.Input{Inherited: environmentFromProcess(), InheritedSensitive: true, DotenvFiles: dotenv, Top: top, Profile: profile, Service: service, Project: p.Identity.Project, Branch: p.Identity.Branch, Scope: p.Identity.Scope, Ports: runtime.Ports, DeferredPorts: runtime.DeferredPorts, DeferRuntime: true, Routes: p.Routes, ServiceName: name})
		if err != nil {
			return nil, fmt.Errorf("service %q environment: %w", name, err)
		}
		// The same values reach startup, readiness, shutdown, exports and exec.
		result[name] = values.With(sessionValues(p.Identity, p.Routes, name))
	}
	return result, nil
}

func runtimeFor(p plan.Plan) env.Runtime {
	values := make(map[string]int)
	deferred := make(map[string]struct{})
	for name, port := range p.Ports {
		if !port.Discovered || port.Port > 0 {
			values[name] = port.Port
		}
		if port.Discovered && port.Port == 0 {
			deferred[name] = struct{}{}
		}
	}
	routes := p.Routes
	routes.Routes = append([]plan.Route(nil), p.Routes.Routes...)
	return env.Runtime{Project: p.Identity.Project, Branch: p.Identity.Branch, Scope: p.Identity.Scope, Ports: values, DeferredPorts: deferred, Routes: routes}
}

func inspectEnvironments(p plan.Plan, environments map[string]env.Values, includeInherited bool) plan.Plan {
	p.Services = cloneServices(p.Services)
	runtime := runtimeFor(p)
	for name, values := range environments {
		if resolved, err := values.ExpandRuntimeValues(runtime); err == nil {
			values = resolved
		}
		if !includeInherited {
			values = values.ManagedOnly()
		}
		service := p.Services[name]
		service.Env = values.ConfigValues()
		p.Services[name] = service
	}
	allValues := sessionEnvironment(p, environments)
	if !includeInherited {
		allValues = allValues.ManagedOnly()
	}
	p.Environment = allValues.ConfigValues()
	return p
}

func sessionEnvironment(p plan.Plan, environments map[string]env.Values) env.Values {
	var result env.Values
	for _, name := range p.Order {
		result = result.Merge(environments[name])
	}
	return result
}

func referencesPort(service config.Service, name string) bool {
	token := "${ports." + name + "}"
	values := append([]string{}, service.Command...)
	values = append(values, service.Shell)
	values = append(values, service.DependsOn...)
	if service.Ready != nil {
		values = append(values, service.Ready.TCP)
		values = append(values, service.Ready.Command...)
		if service.Ready.HTTP != nil {
			values = append(values, service.Ready.HTTP.URL)
			for key, value := range service.Ready.HTTP.Headers {
				values = append(values, key, value)
			}
		}
	}
	for key, value := range service.Env {
		values = append(values, key)
		if value.Literal != nil {
			values = append(values, *value.Literal)
		}
	}
	for key, value := range service.Endpoints {
		values = append(values, key, value)
	}
	if service.Shutdown != nil {
		values = append(values, service.Shutdown.Command...)
	}
	if service.Route != nil {
		values = append(values, service.Route.Project, service.Route.Branch, service.Route.Port)
	}
	for _, value := range values {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func hasDeferredPort(values []string, runtime env.Runtime) bool {
	for _, value := range values {
		for name := range runtime.DeferredPorts {
			if strings.Contains(value, "${ports."+name+"}") {
				return true
			}
		}
	}
	return false
}

func cloneServices(input map[string]plan.Service) map[string]plan.Service {
	result := make(map[string]plan.Service, len(input))
	for name, service := range input {
		result[name] = service
	}
	return result
}

func checkExecutable(name, directory string) error {
	if strings.ContainsRune(name, os.PathSeparator) {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("executable %q is unavailable", name)
		}
		return nil
	}
	_, err := exec.LookPath(name)
	return err
}
