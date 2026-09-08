// Package env composes and resolves session environment values.
package env

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/plan"
)

type Entry struct {
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type Values struct {
	entries map[string]Entry
}

type Runtime struct {
	Project string
	Branch  string
	Scope   string
	Ports   map[string]int
	Routes  plan.Routes
}

type Input struct {
	Inherited   map[string]string
	DotenvFiles []string
	Top         map[string]config.Value
	Profile     map[string]config.Value
	Service     map[string]config.Value
	Project     string
	Branch      string
	Scope       string
	Ports       map[string]int
	Routes      plan.Routes
	ServiceName string
}

func Resolve(input Input) (Values, error) {
	entries := make(map[string]Entry)
	for name, value := range input.Inherited {
		if err := validateName(name); err != nil {
			return Values{}, fmt.Errorf("inherited environment: %w", err)
		}
		entries[name] = Entry{Value: value}
	}
	for _, path := range input.DotenvFiles {
		values, err := parseDotenv(path)
		if err != nil {
			return Values{}, err
		}
		for name, value := range values {
			entries[name] = Entry{Value: value}
		}
	}
	for name, value := range input.Top {
		entry, err := literalEntry(name, value)
		if err != nil {
			return Values{}, fmt.Errorf("env.%s: %w", name, err)
		}
		entries[name] = entry
	}
	for name, value := range input.Profile {
		entry, err := literalEntry(name, value)
		if err != nil {
			return Values{}, fmt.Errorf("profile env.%s: %w", name, err)
		}
		entries[name] = entry
	}
	for name, value := range input.Service {
		entry, err := literalEntry(name, value)
		if err != nil {
			return Values{}, fmt.Errorf("service env.%s: %w", name, err)
		}
		entries[name] = entry
	}
	for _, route := range input.Routes.Routes {
		if !route.Available || !receives(route, input.ServiceName) {
			continue
		}
		for kind, alias := range route.Export {
			value := ""
			switch kind {
			case "host":
				value = route.Host
			case "url":
				value = route.URL
			default:
				return Values{}, fmt.Errorf("route %s has unsupported export %q", route.Service, kind)
			}
			entries[alias] = Entry{Value: value}
		}
	}

	resolver := resolver{
		entries: entries, project: input.Project, branch: input.Branch,
		scope: input.Scope, ports: input.Ports, routes: input.Routes,
		resolved: make(map[string]Entry), resolving: make(map[string]bool), stack: nil,
	}
	for name := range entries {
		if _, err := resolver.resolveEnv(name); err != nil {
			return Values{}, err
		}
	}
	return Values{entries: resolver.resolved}, nil
}

func (v Values) Get(name string) (Entry, bool) {
	entry, ok := v.entries[name]
	return entry, ok
}

func (v Values) Map(showSensitive bool) map[string]string {
	result := make(map[string]string, len(v.entries))
	for name, entry := range v.entries {
		if entry.Sensitive && !showSensitive {
			result[name] = "<redacted>"
		} else {
			result[name] = entry.Value
		}
	}
	return result
}

func (v Values) JSON(showSensitive bool) ([]byte, error) {
	return json.MarshalIndent(v.Map(showSensitive), "", "  ")
}

func (v Values) Render(shell string, showSensitive bool) (string, error) {
	if shell == "json" {
		return stringMustJSON(v.Map(showSensitive)), nil
	}
	keys := make([]string, 0, len(v.entries))
	for name := range v.entries {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, name := range keys {
		entry := v.entries[name]
		value := entry.Value
		if entry.Sensitive && !showSensitive {
			value = "<redacted>"
		}
		switch shell {
		case "bash", "posix", "zsh":
			fmt.Fprintf(&builder, "export %s=%s\n", name, shellQuote(value))
		case "fish":
			fmt.Fprintf(&builder, "set -gx %s %s\n", name, shellQuote(value))
		case "human":
			fmt.Fprintf(&builder, "%s=%s\n", name, value)
		default:
			return "", fmt.Errorf("unsupported environment shell %q", shell)
		}
	}
	return builder.String(), nil
}

// ExpandArgument expands allowed environment references while refusing to put
// a sensitive value in a process argument vector.
func (v Values) ExpandArgument(value string) (string, error) {
	return v.expand(value, true)
}

func (v Values) ExpandShell(value string) (string, error) {
	return v.expand(value, true)
}

// ExpandRuntime resolves non-environment runtime references used by readiness
// URLs, endpoints, and command arguments after ports and routes are planned.
func (v Values) ExpandRuntime(value string, runtime Runtime) (string, error) {
	refs := interpolation.FindAllStringSubmatch(value, -1)
	if strings.Contains(value, "${") && len(refs) == 0 {
		return "", errors.New("malformed interpolation")
	}
	for _, ref := range refs {
		replacement, sensitive, err := v.runtimeReference(ref[1], runtime)
		if err != nil {
			return "", err
		}
		if sensitive {
			return "", fmt.Errorf("sensitive environment value cannot be interpolated into runtime text")
		}
		value = strings.Replace(value, ref[0], replacement, 1)
	}
	return value, nil
}

func (v Values) runtimeReference(reference string, runtime Runtime) (string, bool, error) {
	switch {
	case reference == "project":
		return runtime.Project, false, nil
	case reference == "branch":
		return runtime.Branch, false, nil
	case reference == "session.scope":
		return runtime.Scope, false, nil
	case strings.HasPrefix(reference, "ports."):
		name := strings.TrimPrefix(reference, "ports.")
		port, ok := runtime.Ports[name]
		if !ok {
			return "", false, fmt.Errorf("unknown port %q", name)
		}
		return strconv.Itoa(port), false, nil
	case strings.HasPrefix(reference, "env."):
		entry, ok := v.entries[strings.TrimPrefix(reference, "env.")]
		if !ok {
			return "", false, fmt.Errorf("unknown environment variable %q", strings.TrimPrefix(reference, "env."))
		}
		return entry.Value, entry.Sensitive, nil
	case strings.HasPrefix(reference, "routes."):
		parts := strings.Split(strings.TrimPrefix(reference, "routes."), ".")
		if len(parts) != 2 || (parts[1] != "host" && parts[1] != "url") {
			return "", false, fmt.Errorf("invalid route reference %q", reference)
		}
		for _, item := range runtime.Routes.Routes {
			if item.Service == parts[0] {
				if !item.Available {
					return "", false, fmt.Errorf("route %q is unavailable", parts[0])
				}
				if parts[1] == "host" {
					return item.Host, false, nil
				}
				return item.URL, false, nil
			}
		}
		return "", false, fmt.Errorf("unknown route %q", parts[0])
	default:
		return "", false, fmt.Errorf("unknown interpolation %q", reference)
	}
}

func (v Values) expand(value string, rejectSensitive bool) (string, error) {
	refs := interpolation.FindAllStringSubmatch(value, -1)
	if strings.Contains(value, "${") && len(refs) == 0 {
		return "", errors.New("malformed interpolation")
	}
	for _, ref := range refs {
		entry, ok := v.entries[ref[1]]
		if !ok {
			return "", fmt.Errorf("unknown environment reference %q", ref[1])
		}
		if rejectSensitive && entry.Sensitive {
			return "", fmt.Errorf("sensitive environment value %q cannot be interpolated into command text", ref[1])
		}
		value = strings.Replace(value, ref[0], entry.Value, 1)
	}
	return value, nil
}

type resolver struct {
	entries   map[string]Entry
	resolved  map[string]Entry
	resolving map[string]bool
	stack     []string
	project   string
	branch    string
	scope     string
	ports     map[string]int
	routes    plan.Routes
}

func (r *resolver) resolveEnv(name string) (Entry, error) {
	if result, ok := r.resolved[name]; ok {
		return result, nil
	}
	if r.resolving[name] {
		cycle := append(append([]string{}, r.stack...), name)
		return Entry{}, fmt.Errorf("environment interpolation cycle: %s", strings.Join(cycle, " -> "))
	}
	entry, ok := r.entries[name]
	if !ok {
		return Entry{}, fmt.Errorf("environment variable %q is not defined", name)
	}
	r.resolving[name] = true
	r.stack = append(r.stack, name)
	value, sensitive, err := r.resolveText(entry.Value)
	r.stack = r.stack[:len(r.stack)-1]
	delete(r.resolving, name)
	if err != nil {
		return Entry{}, fmt.Errorf("env.%s: %w", name, err)
	}
	result := Entry{Value: value, Sensitive: entry.Sensitive || sensitive}
	r.resolved[name] = result
	return result, nil
}

func (r *resolver) resolveText(value string) (string, bool, error) {
	refs := interpolation.FindAllStringSubmatch(value, -1)
	if strings.Contains(value, "${") && len(refs) == 0 {
		return "", false, errors.New("malformed interpolation")
	}
	var sensitive bool
	for _, ref := range refs {
		replacement, replacementSensitive, err := r.resolveReference(ref[1])
		if err != nil {
			return "", false, err
		}
		value = strings.Replace(value, ref[0], replacement, 1)
		sensitive = sensitive || replacementSensitive
	}
	return value, sensitive, nil
}

func (r *resolver) resolveReference(reference string) (string, bool, error) {
	switch {
	case reference == "project":
		return r.project, false, nil
	case reference == "branch":
		return r.branch, false, nil
	case reference == "session.scope":
		return r.scope, false, nil
	case strings.HasPrefix(reference, "ports."):
		name := strings.TrimPrefix(reference, "ports.")
		port, ok := r.ports[name]
		if !ok {
			return "", false, fmt.Errorf("unknown port %q", name)
		}
		return strconv.Itoa(port), false, nil
	case strings.HasPrefix(reference, "env."):
		returnEntry, err := r.resolveEnv(strings.TrimPrefix(reference, "env."))
		if err != nil {
			return "", false, err
		}
		return returnEntry.Value, returnEntry.Sensitive, nil
	case strings.HasPrefix(reference, "routes."):
		parts := strings.Split(strings.TrimPrefix(reference, "routes."), ".")
		if len(parts) != 2 || (parts[1] != "host" && parts[1] != "url") {
			return "", false, fmt.Errorf("invalid route reference %q", reference)
		}
		for _, route := range r.routes.Routes {
			if route.Service == parts[0] {
				if !route.Available {
					return "", false, fmt.Errorf("route %q is unavailable", parts[0])
				}
				if parts[1] == "host" {
					return route.Host, false, nil
				}
				return route.URL, false, nil
			}
		}
		return "", false, fmt.Errorf("unknown route %q", parts[0])
	default:
		return "", false, fmt.Errorf("unknown interpolation %q", reference)
	}
}

var interpolation = regexp.MustCompile(`\$\{([^{}]+)\}`)
var validNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func literalEntry(name string, value config.Value) (Entry, error) {
	if err := validateName(name); err != nil {
		return Entry{}, err
	}
	if value.Generate != nil {
		return Entry{}, errors.New("generated values must be resolved before environment composition")
	}
	if value.Literal == nil {
		return Entry{}, errors.New("value is missing")
	}
	return Entry{Value: *value.Literal, Sensitive: value.Sensitive}, nil
}

func validateName(name string) error {
	if !validNamePattern.MatchString(name) {
		return fmt.Errorf("invalid environment name %q", name)
	}
	return nil
}

func parseDotenv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dotenv %s: %w", path, err)
	}
	result := make(map[string]string)
	for lineNumber, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator <= 0 {
			return nil, fmt.Errorf("dotenv %s:%d: expected NAME=value", path, lineNumber+1)
		}
		name := strings.TrimSpace(line[:separator])
		if err := validateName(name); err != nil {
			return nil, fmt.Errorf("dotenv %s:%d: %w", path, lineNumber+1, err)
		}
		value, err := parseDotenvValue(strings.TrimSpace(line[separator+1:]))
		if err != nil {
			return nil, fmt.Errorf("dotenv %s:%d: %w", path, lineNumber+1, err)
		}
		result[name] = value
	}
	return result, nil
}

func parseDotenvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' {
		end := strings.LastIndexByte(value[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single-quoted value")
		}
		end++
		if trailing := strings.TrimSpace(value[end+1:]); trailing != "" && !strings.HasPrefix(trailing, "#") {
			return "", errors.New("unexpected text after quoted value")
		}
		return value[1:end], nil
	}
	if value[0] == '"' {
		if len(value) < 2 {
			return "", errors.New("unterminated double-quoted value")
		}
		end := strings.LastIndexByte(value, '"')
		if end == 0 {
			return "", errors.New("unterminated double-quoted value")
		}
		parsed, err := strconv.Unquote(value[:end+1])
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted value: %w", err)
		}
		if trailing := strings.TrimSpace(value[end+1:]); trailing != "" && !strings.HasPrefix(trailing, "#") {
			return "", errors.New("unexpected text after quoted value")
		}
		return parsed, nil
	}
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = value[:comment]
	}
	return strings.TrimSpace(value), nil
}

func receives(route plan.Route, service string) bool {
	for _, receiver := range route.ExportReceivers {
		if receiver == service {
			return true
		}
	}
	return service == route.Service
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func stringMustJSON(value map[string]string) string {
	encoded, _ := json.MarshalIndent(value, "", "  ")
	return string(encoded) + "\n"
}
