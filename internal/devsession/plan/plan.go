package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/devsession/ports"
	"github.com/webportdev/webport/internal/devsession/primitives"
	"github.com/webportdev/webport/internal/route"
	"gopkg.in/yaml.v3"
)

var reservedOperations = map[string]struct{}{
	"check": {}, "config": {}, "status": {}, "logs": {},
	"env": {}, "exec": {}, "stop": {}, "clean": {},
}

// Select limits runtime preflight to the requested dependency closure. Ports
// referenced by shared environment values remain part of that closure.
func Select(cfg config.Config, options BuildOptions) (config.Config, error) {
	if err := validateNames(cfg); err != nil {
		return config.Config{}, err
	}
	profile, roots, err := selectRoots(cfg, options)
	if err != nil {
		return config.Config{}, err
	}
	if err := validateDependencies(cfg.Services); err != nil {
		return config.Config{}, err
	}
	closure := dependencyClosure(cfg.Services, roots)
	if _, err := topologicalOrder(cfg.Services, closure); err != nil {
		return config.Config{}, err
	}
	selected := cfg
	selected.Profiles = make(map[string]config.Profile)
	if item, ok := cfg.Profiles[profile]; ok {
		selected.Profiles[profile] = item
	}
	selected.Services = make(map[string]config.Service)
	selected.Ports = make(map[string]config.Port)
	for name := range closure {
		selected.Services[name] = cfg.Services[name]
	}
	data, err := yaml.Marshal(struct {
		Env      map[string]config.Value
		Profile  config.Profile
		Services map[string]config.Service
	}{cfg.Env, cfg.Profiles[profile], selected.Services})
	if err != nil {
		return config.Config{}, err
	}
	used := make(map[string]bool)
	for _, match := range regexp.MustCompile(`\$\{ports\.([^{}]+)\}`).FindAllStringSubmatch(string(data), -1) {
		used[match[1]] = true
	}
	for _, service := range selected.Services {
		if service.Route != nil {
			used[service.Route.Port] = true
		}
	}
	for name := range used {
		spec, ok := cfg.Ports[name]
		if !ok {
			return config.Config{}, fmt.Errorf("unknown port %q", name)
		}
		selected.Ports[name] = spec
	}
	return selected, nil
}

type BuildOptions struct {
	Profile      string
	Service      string
	PortSpecs    map[string]config.Port
	PortValues   map[string]ports.Allocation
	PortOwners   map[string]string
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
	PortOwners    map[string]string
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
			Logs: effectiveLogs(item.Logs), Shutdown: item.Shutdown, Route: item.Route,
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
		Ports: copyAllocations(options.PortValues), PortOwners: copyStrings(options.PortOwners), Routes: filteredRoutes,
		Environment: copyValues(cfg.Env),
	}, nil
}

func (p Plan) JSON(showSensitive ...bool) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	view := p.redacted()
	if len(showSensitive) > 0 && showSensitive[0] {
		view.Environment = visibleValues(p.Environment)
		for name, service := range p.Services {
			value := view.Services[name]
			value.Environment = visibleValues(service.Env)
			view.Services[name] = value
		}
	}
	if err := encoder.Encode(view); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func (p Plan) Human(showSensitive ...bool) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "development session plan\nprofile: %s\nworktree: %s\nscope: %s\n", p.Profile, p.Identity.WorktreeRoot, p.Identity.Scope)
	if len(p.Ports) > 0 {
		builder.WriteString("ports:\n")
		names := make([]string, 0, len(p.Ports))
		for name := range p.Ports {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			allocation := p.Ports[name]
			if allocation.Discovered {
				fmt.Fprintf(&builder, "  %s: discover (owner=%s)\n", name, allocation.Owner)
			} else {
				fmt.Fprintf(&builder, "  %s: %d (owner=%s)\n", name, allocation.Port, allocation.Owner)
			}
		}
	}
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
			values := service.Env
			if len(showSensitive) > 0 && showSensitive[0] {
				values = copyValues(values)
				for name, v := range values {
					v.Sensitive = false
					values[name] = v
				}
			}
			builder.WriteString(formatEnv(values))
		}
		if len(service.DependsOn) > 0 {
			fmt.Fprintf(&builder, " depends_on=%s", strings.Join(service.DependsOn, ","))
		}
		if service.Ready != nil {
			builder.WriteString(" ready=")
			switch {
			case service.Ready.TCP != "":
				builder.WriteString("tcp:" + service.Ready.TCP)
			case service.Ready.HTTP != nil:
				fmt.Fprintf(&builder, "http:%s method=%s status=%v interval=%s timeout=%s overall_timeout=%s", service.Ready.HTTP.URL, service.Ready.HTTP.Method, service.Ready.HTTP.Status, service.Ready.HTTP.Interval.Duration(), service.Ready.HTTP.Timeout.Duration(), service.Ready.HTTP.OverallTimeout.Duration())
			default:
				builder.WriteString("command:" + strings.Join(service.Ready.Command, " "))
			}
		}
		if len(service.Endpoints) > 0 {
			builder.WriteString(" endpoints=")
			builder.WriteString(formatStrings(service.Endpoints))
		}
		if service.Logs != nil {
			fmt.Fprintf(&builder, " logs=%s:%s:%s", service.Logs.Destination, service.Logs.Path, service.Logs.Mode)
		}
		if service.Shutdown != nil {
			fmt.Fprintf(&builder, " shutdown=signal:%s timeout:%s command:%s", service.Shutdown.Signal, service.Shutdown.Timeout.Duration(), strings.Join(service.Shutdown.Command, " "))
		}
		if service.Route != nil {
			fmt.Fprintf(&builder, " route=project:%s branch:%s port:%s optional:%t export:%s", service.Route.Project, service.Route.Branch, service.Route.Port, service.Route.Optional, formatStrings(service.Route.Export))
		}
		builder.WriteByte('\n')
	}
	if len(p.Routes.Routes) > 0 {
		builder.WriteString("routes:\n")
		for _, item := range p.Routes.Routes {
			fmt.Fprintf(&builder, "  %s: %s:%s port=%d available=%t optional=%t", item.Service, item.Project, item.Branch, item.Port, item.Available, item.Optional)
			if item.URL != "" {
				fmt.Fprintf(&builder, " url=%s", item.URL)
			}
			builder.WriteByte('\n')
		}
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
	PortOwners    map[string]string           `json:"port_owners,omitempty"`
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
	Ready       *redactedReady    `json:"ready,omitempty"`
	Endpoints   map[string]string `json:"endpoints,omitempty"`
	Logs        *redactedLogs     `json:"logs,omitempty"`
	Shutdown    *redactedShutdown `json:"shutdown,omitempty"`
	Route       *redactedRoute    `json:"route,omitempty"`
}

type redactedReady struct {
	TCP     string        `json:"tcp,omitempty"`
	HTTP    *redactedHTTP `json:"http,omitempty"`
	Command []string      `json:"command,omitempty"`
}

type redactedHTTP struct {
	URL                string            `json:"url"`
	Method             string            `json:"method,omitempty"`
	Status             []int             `json:"status,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	Interval           string            `json:"interval,omitempty"`
	Timeout            string            `json:"timeout,omitempty"`
	OverallTimeout     string            `json:"overall_timeout,omitempty"`
	InsecureSkipVerify bool              `json:"insecure_skip_verify,omitempty"`
}

type redactedLogs struct {
	Destination string `json:"destination,omitempty"`
	Path        string `json:"path,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Streams     string `json:"streams,omitempty"`
	MaxBytes    int64  `json:"max_bytes,omitempty"`
	Backups     int    `json:"backups,omitempty"`
}

type redactedShutdown struct {
	Signal      string   `json:"signal,omitempty"`
	GracePeriod string   `json:"grace_period,omitempty"`
	Command     []string `json:"command,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
}

type redactedRoute struct {
	Project  string            `json:"project,omitempty"`
	Branch   string            `json:"branch,omitempty"`
	Port     string            `json:"port"`
	Optional bool              `json:"optional,omitempty"`
	Export   map[string]string `json:"export,omitempty"`
}

func readyView(value *config.Ready) *redactedReady {
	if value == nil {
		return nil
	}
	result := &redactedReady{TCP: value.TCP, Command: append([]string(nil), value.Command...)}
	if value.HTTP != nil {
		result.HTTP = &redactedHTTP{
			URL: value.HTTP.URL, Method: value.HTTP.Method, Status: append([]int(nil), value.HTTP.Status...),
			Headers: copyStrings(value.HTTP.Headers), Interval: value.HTTP.Interval.Duration().String(),
			Timeout: value.HTTP.Timeout.Duration().String(), OverallTimeout: value.HTTP.OverallTimeout.Duration().String(),
			InsecureSkipVerify: value.HTTP.InsecureSkipVerify,
		}
	}
	return result
}

func logsView(value *config.Logs) *redactedLogs {
	if value == nil {
		return nil
	}
	return &redactedLogs{Destination: value.Destination, Path: value.Path, Mode: value.Mode, Streams: value.Streams, MaxBytes: value.MaxBytes, Backups: value.Backups}
}

func shutdownView(value *config.Shutdown) *redactedShutdown {
	if value == nil {
		return nil
	}
	return &redactedShutdown{Signal: value.Signal, GracePeriod: value.GracePeriod.Duration().String(), Command: append([]string(nil), value.Command...), Timeout: value.Timeout.Duration().String()}
}

func routeView(value *config.Route) *redactedRoute {
	if value == nil {
		return nil
	}
	return &redactedRoute{Project: value.Project, Branch: value.Branch, Port: value.Port, Optional: value.Optional, Export: copyStrings(value.Export)}
}

func (p Plan) redacted() redactedPlan {
	services := make(map[string]redactedService, len(p.Services))
	for name, service := range p.Services {
		services[name] = redactedService{
			Command: append([]string(nil), service.Command...), Shell: service.Shell,
			WorkingDir: service.WorkingDir, DependsOn: append([]string(nil), service.DependsOn...),
			Completion: service.Completion, Environment: redactedValues(service.Env),
			Ready: readyView(service.Ready), Endpoints: copyStrings(service.Endpoints), Logs: logsView(service.Logs),
			Shutdown: shutdownView(service.Shutdown), Route: routeView(service.Route),
		}
	}
	return redactedPlan{
		SchemaVersion: p.SchemaVersion, Profile: p.Profile, Worktree: p.Identity.WorktreeRoot,
		Scope: p.Identity.Scope, Roots: p.Roots, Order: p.Order, Services: services,
		Ports: p.Ports, PortOwners: p.PortOwners, Routes: p.Routes, Environment: redactedValues(p.Environment),
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
	if len(service.Platform) > 0 {
		allowed := false
		for _, platform := range service.Platform {
			if platform != "linux" && platform != "darwin" {
				return fmt.Errorf("service %q: invalid platform %q", name, platform)
			}
			allowed = allowed || platform == runtime.GOOS
		}
		if !allowed {
			return fmt.Errorf("service %q is unavailable on %s", name, runtime.GOOS)
		}
	}
	if service.Shutdown != nil {
		switch service.Shutdown.Signal {
		case "", "inherit", "SIGINT", "SIGTERM", "SIGHUP", "SIGKILL":
		default:
			return fmt.Errorf("service %q: invalid shutdown signal", name)
		}
		if service.Shutdown.GracePeriod < 0 || service.Shutdown.Timeout < 0 {
			return fmt.Errorf("service %q: shutdown durations must not be negative", name)
		}
	}
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
			if service.Ready.HTTP.Interval < 0 || service.Ready.HTTP.Timeout < 0 || service.Ready.HTTP.OverallTimeout < 0 {
				return fmt.Errorf("service %q: readiness durations must not be negative", name)
			}
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
		if service.Logs.Mode != "" && service.Logs.Mode != "append" && service.Logs.Mode != "truncate" {
			return fmt.Errorf("service %q: invalid log mode", name)
		}
		if service.Logs.Streams != "" && service.Logs.Streams != "combined" && service.Logs.Streams != "separate" {
			return fmt.Errorf("service %q: invalid log streams", name)
		}
		if service.Logs.MaxBytes < 0 || service.Logs.Backups < 0 {
			return fmt.Errorf("service %q: invalid log rotation bounds", name)
		}
		if service.Logs.Destination != "" && service.Logs.Destination != "session" && service.Logs.Destination != "none" && service.Logs.Destination != "file" && service.Logs.Destination != "directory" {
			return fmt.Errorf("service %q has invalid log destination %q", name, service.Logs.Destination)
		}
		if (service.Logs.Destination == "" || service.Logs.Destination == "session") && service.Logs.Path != "" {
			return fmt.Errorf("service %q: session log destination cannot define a path", name)
		}
	}
	_ = id
	return nil
}

func effectiveLogs(value *config.Logs) *config.Logs {
	result := config.Logs{}
	if value != nil {
		result = *value
	}
	if result.Destination == "" {
		result.Destination = "session"
	}
	if result.Mode == "" {
		result.Mode = "truncate"
	}
	if result.Streams == "" {
		result.Streams = "combined"
	}
	if result.Destination == "session" {
		if result.MaxBytes == 0 {
			result.MaxBytes = 10 * 1024 * 1024
		}
		if result.Backups == 0 {
			result.Backups = 2
		}
	}
	return &result
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

func visibleValues(values map[string]config.Value) map[string]string {
	result := redactedValues(values)
	for name, value := range values {
		if value.Literal != nil {
			result[name] = *value.Literal
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

func formatStrings(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

var _ primitives.CommandLookup = commandLookup{}
