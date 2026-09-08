// Package plan contains immutable, side-effect-free session planning stages.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/webportdev/webport/internal/devsession/config"
	"github.com/webportdev/webport/internal/devsession/daemon"
	"github.com/webportdev/webport/internal/devsession/identity"
	"github.com/webportdev/webport/internal/route"
)

type DaemonInfo struct {
	Available  bool   `json:"available"`
	BaseDomain string `json:"base_domain,omitempty"`
	DefaultTTL int    `json:"default_ttl_seconds,omitempty"`
	TLSMode    string `json:"tls_mode,omitempty"`
	CACertPath string `json:"ca_cert_path,omitempty"`
	Error      string `json:"error,omitempty"`
}

type Route struct {
	Service         string            `json:"service"`
	Project         string            `json:"project"`
	Branch          string            `json:"branch"`
	RawID           string            `json:"raw_id"`
	Host            string            `json:"host,omitempty"`
	URL             string            `json:"url,omitempty"`
	PortName        string            `json:"port_name"`
	Port            int               `json:"port,omitempty"`
	Optional        bool              `json:"optional"`
	Available       bool              `json:"available"`
	Export          map[string]string `json:"export,omitempty"`
	ExportReceivers []string          `json:"export_receivers,omitempty"`
}

type Routes struct {
	SchemaVersion int        `json:"schema_version"`
	Daemon        DaemonInfo `json:"daemon"`
	Routes        []Route    `json:"routes"`
}

// ResolveRoutes computes all route identities before a child starts. An empty
// primaryService uses the first route's service as the primary route owner;
// callers launching a selected service should pass that service explicitly.
func ResolveRoutes(ctx context.Context, cfg config.Config, id identity.Identity, client daemon.Client, primaryService string, ports map[string]int) (Routes, error) {
	serviceNames := make([]string, 0)
	for name, service := range cfg.Services {
		if service.Route != nil {
			serviceNames = append(serviceNames, name)
		}
	}
	sort.Strings(serviceNames)
	result := Routes{SchemaVersion: 1, Routes: make([]Route, 0, len(serviceNames))}
	if len(serviceNames) == 0 {
		return result, nil
	}
	if primaryService == "" {
		primaryService = serviceNames[0]
	}

	var daemonCfg daemon.Config
	if client == nil {
		for _, name := range serviceNames {
			if !cfg.Services[name].Route.Optional {
				return Routes{}, fmt.Errorf("required route %q cannot resolve daemon configuration: daemon client is unavailable", name)
			}
		}
		result.Daemon = DaemonInfo{Error: "daemon client is unavailable"}
	} else {
		var err error
		daemonCfg, err = client.Config(ctx)
		if err != nil {
			for _, name := range serviceNames {
				if !cfg.Services[name].Route.Optional {
					return Routes{}, fmt.Errorf("required route %q cannot resolve daemon configuration: %w", name, err)
				}
			}
			result.Daemon = DaemonInfo{Error: err.Error()}
		} else {
			result.Daemon = DaemonInfo{
				Available: true, BaseDomain: daemonCfg.BaseDomain,
				DefaultTTL: daemonCfg.DefaultTTL, TLSMode: daemonCfg.TLSMode,
				CACertPath: daemonCfg.CACertPath,
			}
		}
	}

	identities := make(map[string]string)
	hosts := make(map[string]string)
	exports := make(map[string]string)
	configuredNames := configuredEnvironmentNames(cfg)
	for _, name := range serviceNames {
		serviceRoute := cfg.Services[name].Route
		project, err := expandIdentityReference(serviceRoute.Project, id.Project, id.Branch)
		if err != nil {
			return Routes{}, fmt.Errorf("service %q route project: %w", name, err)
		}
		if project == "" {
			if name == primaryService {
				project = id.Project
			} else {
				project = id.Project + "-" + name
			}
		}
		branch, err := expandIdentityReference(serviceRoute.Branch, id.Project, id.Branch)
		if err != nil {
			return Routes{}, fmt.Errorf("service %q route branch: %w", name, err)
		}
		if branch == "" {
			branch = id.Branch
		}
		if !route.ValidProjectName(project) || !route.ValidBranchName(branch) {
			return Routes{}, fmt.Errorf("service %q route identity %q:%q is invalid", name, project, branch)
		}
		if serviceRoute.Port == "" {
			return Routes{}, fmt.Errorf("service %q route port is required", name)
		}
		port := ports[serviceRoute.Port]
		resolved := Route{
			Service: name, Project: project, Branch: branch,
			RawID:    route.RouteID{Project: project, Branch: branch}.String(),
			PortName: serviceRoute.Port, Port: port, Optional: serviceRoute.Optional,
			Available:       result.Daemon.Available,
			Export:          copyStringMap(serviceRoute.Export),
			ExportReceivers: exportReceivers(cfg, name),
		}
		if previous, exists := identities[resolved.RawID]; exists {
			return Routes{}, fmt.Errorf("route identity %q is claimed by services %q and %q", resolved.RawID, previous, name)
		}
		identities[resolved.RawID] = name
		if result.Daemon.Available {
			resolved.Host, err = route.BuildDomainChecked(result.Daemon.BaseDomain, project, branch)
			if err != nil {
				return Routes{}, fmt.Errorf("service %q route hostname: %w", name, err)
			}
			if previous, exists := hosts[resolved.Host]; exists {
				return Routes{}, fmt.Errorf("generated route hostname %q is claimed by services %q and %q", resolved.Host, previous, name)
			}
			hosts[resolved.Host] = name
			resolved.URL = "https://" + resolved.Host
		}
		for kind, alias := range serviceRoute.Export {
			if !validEnvName(alias) {
				return Routes{}, fmt.Errorf("service %q route export %q has invalid environment name %q", name, kind, alias)
			}
			if _, exists := configuredNames[alias]; exists {
				return Routes{}, fmt.Errorf("route export %q for service %q conflicts with configured environment", alias, name)
			}
			if previous, exists := exports[alias]; exists {
				return Routes{}, fmt.Errorf("route export %q for service %q conflicts with service %q", alias, name, previous)
			}
			exports[alias] = name
		}
		result.Routes = append(result.Routes, resolved)
	}
	sort.Slice(result.Routes, func(i, j int) bool { return result.Routes[i].Service < result.Routes[j].Service })
	return result, nil
}

func (r Routes) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func (r Routes) Env() string {
	values := map[string]string{
		"WEBPORT_DAEMON_AVAILABLE":   fmt.Sprintf("%t", r.Daemon.Available),
		"WEBPORT_DAEMON_BASE_DOMAIN": r.Daemon.BaseDomain,
		"WEBPORT_DAEMON_TLS_MODE":    r.Daemon.TLSMode,
	}
	if r.Daemon.CACertPath != "" {
		values["WEBPORT_DAEMON_CA_CERT_PATH"] = r.Daemon.CACertPath
	}
	for _, item := range r.Routes {
		prefix := "WEBPORT_ROUTE_" + envPart(item.Service)
		values[prefix+"_PROJECT"] = item.Project
		values[prefix+"_BRANCH"] = item.Branch
		values[prefix+"_ID"] = item.RawID
		values[prefix+"_PORT_NAME"] = item.PortName
		if item.Port > 0 {
			values[prefix+"_PORT"] = fmt.Sprint(item.Port)
		}
		if item.Host != "" {
			values[prefix+"_HOST"] = item.Host
			values[prefix+"_URL"] = item.URL
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(shellQuote(values[key]))
		builder.WriteByte('\n')
	}
	return builder.String()
}

func configuredEnvironmentNames(cfg config.Config) map[string]struct{} {
	result := make(map[string]struct{})
	for name := range cfg.Env {
		result[name] = struct{}{}
	}
	for _, service := range cfg.Services {
		for name := range service.Env {
			result[name] = struct{}{}
		}
	}
	for _, profile := range cfg.Profiles {
		for name := range profile.Env {
			result[name] = struct{}{}
		}
	}
	return result
}

func exportReceivers(cfg config.Config, owner string) []string {
	result := []string{owner}
	for name := range cfg.Services {
		if name != owner && dependsTransitively(cfg.Services, name, owner, map[string]bool{}) {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func dependsTransitively(services map[string]config.Service, candidate, target string, visiting map[string]bool) bool {
	if visiting[candidate] {
		return false
	}
	visiting[candidate] = true
	for _, dependency := range services[candidate].DependsOn {
		if dependency == target || dependsTransitively(services, dependency, target, visiting) {
			return true
		}
	}
	return false
}

func expandIdentityReference(value, project, branch string) (string, error) {
	value = strings.ReplaceAll(value, "${project}", project)
	value = strings.ReplaceAll(value, "${branch}", branch)
	if strings.Contains(value, "${") {
		return "", fmt.Errorf("unsupported identity reference in %q", value)
	}
	return value, nil
}

func validEnvName(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if !(char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func envPart(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToUpper(value) {
		if (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func copyStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
