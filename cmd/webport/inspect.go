package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	gitdetect "github.com/webportdev/webport/cmd/webportctl/git"
	"github.com/webportdev/webport/internal/devsession/config"
	devsession "github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/route"
)

type inspectOptions struct {
	configPath       string
	api              string
	format           string
	includeInherited bool
	currentDir       string
}

type projectInspection struct {
	SchemaVersion     int                          `json:"schema_version"`
	Project           string                       `json:"project"`
	Branch            string                       `json:"branch"`
	Source            string                       `json:"source"`
	EnvironmentSource string                       `json:"environment_source"`
	Worktree          string                       `json:"worktree,omitempty"`
	Profile           string                       `json:"profile,omitempty"`
	Routes            []devsession.InspectionRoute `json:"routes"`
	Environment       map[string]map[string]string `json:"environment"`
}

func runInspect(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("webport inspect", flag.ContinueOnError)
	flags.SetOutput(errOut)
	options := inspectOptions{}
	flags.StringVar(&options.configPath, "config", "", "configuration path for another active project")
	flags.StringVar(&options.api, "api", defaultAPI, "webport API URL for route lookup")
	flags.StringVar(&options.format, "format", "", "output format: json")
	flags.Bool("show-sensitive", false, "accepted for compatibility; configured values are always shown")
	flags.BoolVar(&options.includeInherited, "include-inherited", false, "include inherited process environment for configured sessions")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("webport inspect accepts no positional arguments")
	}
	if options.format != "" && options.format != "json" {
		return errors.New("--format must be json")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := inspectProject(ctx, options)
	if err != nil {
		return err
	}
	if options.format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	return writeProjectInspection(out, result)
}

func inspectProject(ctx context.Context, options inspectOptions) (projectInspection, error) {
	live, err := devsession.InspectLive(ctx, devsession.Options{
		CurrentDir: options.currentDir, ConfigPath: options.configPath,
		ShowSensitive: true, IncludeInherited: options.includeInherited,
	})
	if err == nil {
		return projectInspection{
			SchemaVersion: 1, Project: live.Project, Branch: live.Branch,
			Source: "configured", EnvironmentSource: "live", Worktree: live.Worktree,
			Profile: live.Profile, Routes: live.Routes, Environment: live.Environment,
		}, nil
	}
	if options.configPath != "" {
		if errors.Is(err, devsession.ErrNoLiveSession) {
			return projectInspection{}, fmt.Errorf("no active session for %s; use webport dev config --config %s to preview configuration", options.configPath, options.configPath)
		}
		return projectInspection{}, err
	}
	missingConfig := errors.Is(err, config.ErrNotFound)
	if !missingConfig && !errors.Is(err, devsession.ErrNoLiveSession) {
		return projectInspection{}, err
	}
	worktree, gitErr := gitdetect.DetectWorktreeFrom(options.currentDirOrDot())
	if gitErr != nil {
		return projectInspection{}, fmt.Errorf("no active configured session and cannot identify a current Git route: %w", gitErr)
	}
	if options.includeInherited {
		return projectInspection{}, errors.New("--include-inherited requires a live configured session")
	}
	routes, err := listActiveRoutes(ctx, options.api)
	if err != nil {
		return projectInspection{}, err
	}
	for _, item := range routes {
		if item.Project == worktree.Project && item.Branch == worktree.Branch {
			url := "https://" + item.Domain
			if item.Domain == "" {
				return projectInspection{}, errors.New("active route has no domain")
			}
			return projectInspection{
				SchemaVersion: 1, Project: item.Project, Branch: item.Branch,
				Source: "route", EnvironmentSource: "derived", Worktree: worktree.Root,
				Routes: []devsession.InspectionRoute{{Project: item.Project, Branch: item.Branch, URL: url}},
				Environment: map[string]map[string]string{"route": {
					"WEBPORT_PROJECT": item.Project, "WEBPORT_BRANCH": item.Branch,
					"WEBPORT_ROUTE": item.Project + ":" + item.Branch,
					"WEBPORT_HOST":  item.Domain, "WEBPORT_URL": url,
				}},
			}, nil
		}
	}
	if missingConfig {
		return projectInspection{}, fmt.Errorf("no active route for %s:%s; run webport dev -- COMMAND to publish one", worktree.Project, worktree.Branch)
	}
	return projectInspection{}, fmt.Errorf("no active session or route for %s:%s; use webport dev config to preview configuration", worktree.Project, worktree.Branch)
}

func (o inspectOptions) currentDirOrDot() string {
	if o.currentDir != "" {
		return o.currentDir
	}
	return "."
}

func listActiveRoutes(ctx context.Context, api string) ([]route.Route, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api, "/")+"/routes", nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list active routes: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list active routes: HTTP %d", response.StatusCode)
	}
	var result route.RoutesList
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode active routes: %w", err)
	}
	return result.Routes, nil
}

func writeProjectInspection(out io.Writer, value projectInspection) error {
	var builder strings.Builder
	fmt.Fprintf(&builder, "project: %s\nbranch: %s\nsource: %s\n", value.Project, value.Branch, value.Source)
	if value.Profile != "" {
		fmt.Fprintf(&builder, "profile: %s\n", value.Profile)
	}
	builder.WriteString("active URLs:\n")
	if len(value.Routes) == 0 {
		builder.WriteString("  (none)\n")
	}
	for _, item := range value.Routes {
		if item.Service == "" {
			fmt.Fprintf(&builder, "  %s\n", item.URL)
		} else {
			fmt.Fprintf(&builder, "  %s: %s\n", item.Service, item.URL)
		}
	}
	if value.EnvironmentSource == "derived" {
		builder.WriteString("route context (derived from active route):\n")
	} else {
		builder.WriteString("environment by service:\n")
	}
	names := make([]string, 0, len(value.Environment))
	for name := range value.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if value.EnvironmentSource != "derived" {
			fmt.Fprintf(&builder, "  %s:\n", name)
		}
		keys := make([]string, 0, len(value.Environment[name]))
		for key := range value.Environment[name] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			indent := "    "
			if value.EnvironmentSource == "derived" {
				indent = "  "
			}
			fmt.Fprintf(&builder, "%s%s=%s\n", indent, key, strconv.Quote(value.Environment[name][key]))
		}
		if len(keys) == 0 {
			builder.WriteString("    (none)\n")
		}
	}
	_, err := io.WriteString(out, builder.String())
	return err
}
