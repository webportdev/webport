package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/ports"
	"github.com/webportdev/webport/internal/devsession/primitives"
	"github.com/webportdev/webport/internal/route"
)

var reservedOperations = map[string]struct{}{
	"check": {}, "config": {}, "status": {}, "logs": {},
	"env": {}, "exec": {}, "stop": {}, "clean": {},
}

type BuildOptions struct {
	Profile      string
	Service      string
	PortSpecs    map[string]config.Port
	PortValues   map[string]ports.Allocation
	Routes       Routes
	Lookup       primitives.CommandLookup
	InheritedEnv map[string]string
}

type Service struct {
	Name       string
	Command    []string
	Shell      string
	WorkingDir string
	Env        map[string]config.Value
	DependsOn  []string
	Completion string
	Ready      *config.Ready
	Endpoints  map[string]string
	Logs       *config.Logs
	Shutdown   *config.Shutdown
	Route      *config.Route
}

type Plan struct {
	SchemaVersion int
	Identity      identity.Identity
	Profile       string
	Roots         []string
	Order         []string
	Services      map[string]Service
	Ports         map[string]ports.Allocation
	Routes        Routes
	Environment   map[string]config.Value
}

func Build(cfg config.Config, id identity.Identity, options BuildOptions) (Plan, error) {
	if options.Lookup == nil {
		options.Lookup = commandLookup{}
	}
	if err := validateNames(cfg); err != nil {
		return Plan{}, err
	}
	profileName, roots, err := selectRoots(cfg, options)
	if err != nil {
		return Plan{}, err
	}
	if err := validateDependencies(cfg.Services); err != nil {
		return Plan{}, err
	}
	closure := dependencyClosure(cfg.Services, roots)
	order, err := topologicalOrder(cfg.Services, closure)
	if err != nil {
		return Plan{}, err
	}
	if err := ports.ValidatePreStartReferences(cfg.Ports, routePortReferences(cfg, closure)); err != nil {
		return Plan{}, err
	}
	if err := checkRequiredCommands(cfg.Requires.Commands, options.Lookup); err != nil {
		return Plan{}, err
	}

	services := make(map[string]Service, len(closure))
	for _, name := range order {
		item := cfg.Services[name]
		if err := validateService(name, item, id); err != nil {
			return Plan{}, err
		}
		workingDir := item.WorkingDir
		if workingDir == "" {
			workingDir = id.ConfigDirectory
		} else {
			workingDir, err = id.ResolvePath(workingDir)
			if err != nil {
				return Plan{}, fmt.Errorf("service %q working_dir: %w", name, err)
			}
		}
		if info, statErr := os.Stat(workingDir); statErr != nil || !info.IsDir() {
			if statErr != nil {
				return Plan{}, fmt.Errorf("service %q working_dir %q: %w", name, workingDir, statErr)
			}
			return Plan{}, fmt.Errorf("service %q working_dir %q is not a directory", name, workingDir)
		}
		services[name] = Service{
			Name: name, Command: append([]string(nil), item.Command...), Shell: item.Shell,
			WorkingDir: workingDir, Env: copyValues(item.Env), DependsOn: append([]string(nil), item.DependsOn...),
			Completion: completion(item), Ready: item.Ready, Endpoints: copyStrings(item.Endpoints),
			Logs: item.Logs, Shutdown: item.Shutdown, Route: item.Route,
		}
	}

	filteredRoutes := Routes{SchemaVersion: options.Routes.SchemaVersion, Daemon: options.Routes.Daemon}
	for _, item := range options.Routes.Routes {
		if _, ok := closure[item.Service]; ok {
			filteredRoutes.Routes = append(filteredRoutes.Routes, item)
		}
	}
	return Plan{
		SchemaVersion: 1, Identity: id, Profile: profileName,
		Roots: append([]string(nil), roots...), Order: order, Services: services,
		Ports: copyAllocations(options.PortValues), Routes: filteredRoutes,
		Environment: copyValues(cfg.Env),
	}, nil
}

func (p Plan) JSON() ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(p.redacted()); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func (p Plan) Human() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "development session plan\nprofile: %s\nworktree: %s\nscope: %s\n", p.Profile, p.Identity.WorktreeRoot, p.Identity.Scope)
	fmt.Fprintf(&builder, "services:\n")
	for index, name := range p.Order {
		service := p.Services[name]
		fmt.Fprintf(&builder, "  %d. %s (%s)", index+1, name, service.Completion)
		if len(service.Command) > 0 {
			fmt.Fprintf(&builder, " [%s]", strings.Join(service.Command, " "))
		} else {
			fmt.Fprintf(&builder, " [shell]")
		}
		if len(service.Env) > 0 {
			builder.WriteString(" env=")
			builder.WriteString(formatEnv(service.Env))
		}
		builder.WriteByte('\n')
	}
	return builder.String()
}

type redactedPlan struct {
	SchemaVersion int                         `json:"schema_version"`
	Profile       string                      `json:"profile"`
	Worktree      string                      `json:"worktree"`
	Scope         string                      `json:"scope"`
	Roots         []string                    `json:"roots"`
	Order         []string                    `json:"order"`
	Services      map[string]redactedService  `json:"services"`
	Ports         map[string]ports.Allocation `json:"ports"`
	Routes        Routes                      `json:"routes"`
	Environment   map[string]string           `json:"environment"`
}

type redactedService struct {
	Command     []string          `json:"command,omitempty"`
	Shell       string            `json:"shell,omitempty"`
	WorkingDir  string            `json:"working_dir"`
	DependsOn   []string          `json:"depends_on,omitempty"`
	Completion  string            `json:"completion"`
	Environment map[string]string `json:"environment,omitempty"`
}

func (p Plan) redacted() redactedPlan {
	services := make(map[string]redactedService, len(p.Services))
	for name, service := range p.Services {
		services[name] = redactedService{
			Command: append([]string(nil), service.Command...), Shell: service.Shell,
			WorkingDir: service.WorkingDir, DependsOn: append([]string(nil), service.DependsOn...),
			Completion: service.Completion, Environment: redactedValues(service.Env),
		}
	}
	return redactedPlan{
		SchemaVersion: p.SchemaVersion, Profile: p.Profile, Worktree: p.Identity.WorktreeRoot,
		Scope: p.Identity.Scope, Roots: p.Roots, Order: p.Order, Services: services,
		Ports: p.Ports, Routes: p.Routes, Environment: redactedValues(p.Environment),
	}
}

func validateNames(cfg config.Config) error {
	for name := range cfg.Services {
		if !route.ValidProjectName(name) {
			return fmt.Errorf("invalid service name %q", name)
		}
		if _, reserved := reservedOperations[name]; reserved {
			return fmt.Errorf("service name %q is reserved for webport dev", name)
		}
	}
	for name := range cfg.Profiles {
		if !route.ValidProjectName(name) {
			return fmt.Errorf("invalid profile name %q", name)
		}
	}
	return nil
}

func selectRoots(cfg config.Config, options BuildOptions) (string, []string, error) {
	if options.Profile != "" && options.Service != "" {
		return "", nil, errors.New("--profile and service selection are mutually exclusive")
	}
	if options.Service != "" {
		if _, ok := cfg.Services[options.Service]; !ok {
			return "", nil, fmt.Errorf("unknown service %q", options.Service)
		}
		return "service:" + options.Service, []string{options.Service}, nil
	}
	profile := options.Profile
	if profile == "" {
		profile = "default"
	}
	item, ok := cfg.Profiles[profile]
	if !ok {
		return "", nil, fmt.Errorf("profile %q is not defined", profile)
	}
	if len(item.Services) == 0 {
		return "", nil, fmt.Errorf("profile %q is empty", profile)
	}
	roots := append([]string(nil), item.Services...)
	sort.Strings(roots)
	for _, name := range roots {
		if _, ok := cfg.Services[name]; !ok {
			return "", nil, fmt.Errorf("profile %q references unknown service %q", profile, name)
		}
	}
	return profile, roots, nil
}

func validateDependencies(services map[string]config.Service) error {
	for name, service := range services {
		seen := make(map[string]struct{})
		for _, dependency := range service.DependsOn {
			if _, ok := services[dependency]; !ok {
				return fmt.Errorf("service %q depends on unknown service %q", name, dependency)
			}
			if _, duplicate := seen[dependency]; duplicate {
				return fmt.Errorf("service %q lists dependency %q more than once", name, dependency)
			}
			seen[dependency] = struct{}{}
		}
	}
	return nil
}

func dependencyClosure(services map[string]config.Service, roots []string) map[string]struct{} {
	closure := make(map[string]struct{})
	var visit func(string)
	visit = func(name string) {
		if _, ok := closure[name]; ok {
			return
		}
		closure[name] = struct{}{}
		for _, dependency := range services[name].DependsOn {
			visit(dependency)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	return closure
}

func topologicalOrder(services map[string]config.Service, closure map[string]struct{}) ([]string, error) {
	state := make(map[string]int)
	stack := make([]string, 0)
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			start := 0
			for i, item := range stack {
				if item == name {
					start = i
					break
				}
			}
			return fmt.Errorf("dependency cycle: %s", strings.Join(append(stack[start:], name), " -> "))
		case 2:
			return nil
		}
		state[name] = 1
		stack = append(stack, name)
		dependencies := append([]string(nil), services[name].DependsOn...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			if _, selected := closure[dependency]; selected {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = 2
		return nil
	}
	selected := make([]string, 0, len(closure))
	for name := range closure {
		selected = append(selected, name)
	}
	sort.Strings(selected)
	for _, name := range selected {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	// Kahn's algorithm chooses the lexicographically smallest ready service at
	// each point, making independent branches stable while guaranteeing that
	// every dependency precedes its dependents.
	indegree := make(map[string]int, len(closure))
	dependents := make(map[string][]string, len(closure))
	ready := make([]string, 0)
	for name := range closure {
		for _, dependency := range services[name].DependsOn {
			if _, selected := closure[dependency]; selected {
				indegree[name]++
				dependents[dependency] = append(dependents[dependency], name)
			}
		}
		if indegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	order := make([]string, 0, len(closure))
	for len(ready) > 0 {
		sort.Strings(ready)
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)
		for _, dependent := range dependents[name] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if len(order) != len(closure) {
		return nil, errors.New("dependency cycle detected")
	}
	return order, nil
}

func validateService(name string, service config.Service, id identity.Identity) error {
	if len(service.Command) == 0 && service.Shell == "" {
		return fmt.Errorf("service %q must define command or shell", name)
	}
	if len(service.Command) > 0 && service.Shell != "" {
		return fmt.Errorf("service %q must define only one of command or shell", name)
	}
	completionValue := completion(service)
	if completionValue != "process" && completionValue != "exit" {
		return fmt.Errorf("service %q has invalid completion %q", name, completionValue)
	}
	if completionValue == "process" && service.Shutdown != nil && len(service.Shutdown.Command) > 0 {
		return fmt.Errorf("service %q process completion cannot define a shutdown command", name)
	}
	if service.Ready != nil {
		checks := 0
		if service.Ready.TCP != "" {
			checks++
		}
		if service.Ready.HTTP != nil {
			checks++
			if service.Ready.HTTP.URL == "" {
				return fmt.Errorf("service %q HTTP readiness URL is required", name)
			}
		}
		if len(service.Ready.Command) > 0 {
			checks++
		}
		if checks != 1 {
			return fmt.Errorf("service %q readiness must define exactly one check", name)
		}
	}
	if service.Route != nil && service.Route.Port == "" {
		return fmt.Errorf("service %q route port is required", name)
	}
	if service.Logs != nil {
		if service.Logs.Destination != "" && service.Logs.Destination != "none" && service.Logs.Destination != "file" && service.Logs.Destination != "directory" {
			return fmt.Errorf("service %q has invalid log destination %q", name, service.Logs.Destination)
		}
	}
	_ = id
	return nil
}

func completion(service config.Service) string {
	if service.Completion == "" {
		return "process"
	}
	return service.Completion
}

func checkRequiredCommands(commands []string, lookup primitives.CommandLookup) error {
	missing := make([]string, 0)
	for _, name := range commands {
		if strings.TrimSpace(name) == "" {
			missing = append(missing, "<empty>")
			continue
		}
		if _, err := lookup.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("required commands not found: %s", strings.Join(missing, ", "))
	}
	return nil
}

func routePortReferences(cfg config.Config, closure map[string]struct{}) map[string]string {
	result := make(map[string]string)
	for name := range closure {
		if service := cfg.Services[name]; service.Route != nil {
			result["service "+name+" route"] = service.Route.Port
		}
	}
	return result
}

type commandLookup struct{}

func (commandLookup) LookPath(file string) (string, error) { return exec.LookPath(file) }

func copyValues(input map[string]config.Value) map[string]config.Value {
	result := make(map[string]config.Value, len(input))
	for name, value := range input {
		result[name] = value
	}
	return result
}

func copyStrings(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for name, value := range input {
		result[name] = value
	}
	return result
}

func copyAllocations(input map[string]ports.Allocation) map[string]ports.Allocation {
	result := make(map[string]ports.Allocation, len(input))
	for name, value := range input {
		result[name] = value
	}
	return result
}

func redactedValues(values map[string]config.Value) map[string]string {
	result := make(map[string]string, len(values))
	for name, value := range values {
		if value.Sensitive || value.Generate != nil {
			result[name] = "<redacted>"
		} else if value.Literal != nil {
			result[name] = *value.Literal
		} else {
			result[name] = "<unset>"
		}
	}
	return result
}

func formatEnv(values map[string]config.Value) string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+redactedValues(map[string]config.Value{name: values[name]})[name])
	}
	return strings.Join(parts, ",")
}

var _ primitives.CommandLookup = commandLookup{}
