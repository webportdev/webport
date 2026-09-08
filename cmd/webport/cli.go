package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	gitdetect "github.com/webportdev/webport/cmd/webportctl/git"
	client "github.com/webportdev/webport/cmd/webportctl/runtime"
	"github.com/webportdev/webport/internal/devsession/daemon"
	devsession "github.com/webportdev/webport/internal/devsession/session"
	"github.com/webportdev/webport/internal/discovery"
	"github.com/webportdev/webport/internal/route"
)

const defaultAPI = "http://127.0.0.1:8080"

func main() {
	err := runCLIWithProcessStreams(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	_ = writeHelp(os.Stdout)
}

func writeHelp(out io.Writer) error {
	_, err := fmt.Fprint(out, `webport - HTTPS routes for local development servers

Usage:
  webport install [options]
  webport dev [options] [SERVICE]
  webport dev check|config|status|env|logs|stop [options]
  webport dev exec SERVICE -- COMMAND [ARG...]
  webport dev clean --secrets [options]
  webport dev [options] -- COMMAND [ARG...]
  webport route --port PORT [options]
  webport list
  webport status
  webport doctor
  webport config
  webport dns status|sync [options]
  webport daemon
  webport version

The local-first install uses webport.localhost and a private local CA.
Run "webport dev -- npm run dev" from a Git checkout to publish a server.
Run "webport dev" in a checkout with .webport.yaml to use a configured
foreground session. Use "webport dev status" or "webport dev logs --follow"
from another terminal while it runs.
`)
	return err
}

type routeFlags struct {
	project string
	branch  string
	port    int
	ttl     time.Duration
	api     string
	verbose bool
}

func addRouteFlags(flags *flag.FlagSet, values *routeFlags) {
	flags.StringVar(&values.project, "project", "", "route project (default: Git repository)")
	flags.StringVar(&values.branch, "branch", "", "route branch (default: current Git branch)")
	flags.IntVar(&values.port, "port", 0, "application port")
	flags.DurationVar(&values.ttl, "ttl", 30*time.Second, "route lease lifetime")
	flags.StringVar(&values.api, "api", defaultAPI, "webport API URL")
	flags.BoolVar(&values.verbose, "verbose", false, "log successful heartbeats")
}

func (v *routeFlags) inferIdentity() error {
	var err error
	if v.project == "" {
		v.project, err = gitdetect.DetectProject()
		if err != nil {
			return fmt.Errorf("detect project: %w; use --project", err)
		}
	}
	if v.branch == "" {
		v.branch, err = gitdetect.DetectBranch()
		if err != nil {
			return fmt.Errorf("detect branch: %w; use --branch", err)
		}
	}
	if v.ttl < 3*time.Second {
		return errors.New("--ttl must be at least 3s")
	}
	return nil
}

func runRoute(args []string) error {
	flags := flag.NewFlagSet("webport route", flag.ContinueOnError)
	var values routeFlags
	addRouteFlags(flags, &values)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if values.port < 1 || values.port > 65535 {
		return errors.New("--port must be between 1 and 65535")
	}
	if err := values.inferIdentity(); err != nil {
		return err
	}
	manager, err := newClientManager(values)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return manager.Stop()
}

func runDev(args []string) error {
	return runDevWithIO(args, os.Stdin, os.Stdout, os.Stderr)
}

func runDevWithIO(args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) > 0 && args[0] == "exec" {
		return runDevExec(args[1:], in, out, errOut)
	}
	separator := -1
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}
	flags := flag.NewFlagSet("webport dev", flag.ContinueOnError)
	flags.SetOutput(errOut)
	var values routeFlags
	var startupTimeout time.Duration
	var format string
	var configPath, profile string
	var showSensitive bool
	var shell string
	var follow bool
	var cleanSecrets bool
	addRouteFlags(flags, &values)
	flags.DurationVar(&startupTimeout, "startup-timeout", 30*time.Second, "time to wait for an HTTP listener")
	flags.StringVar(&format, "format", "", "resolution output format: json or env")
	flags.StringVar(&configPath, "config", "", "development session configuration path")
	flags.StringVar(&profile, "profile", "", "development session profile")
	flags.BoolVar(&showSensitive, "show-sensitive", false, "show sensitive values in interactive inspection output")
	flags.StringVar(&shell, "shell", "bash", "environment output shell: bash, fish, or json")
	flags.BoolVar(&follow, "follow", false, "follow development session logs")
	flags.BoolVar(&cleanSecrets, "secrets", false, "remove project-lifetime generated secrets")
	flagArgs := args
	if separator >= 0 {
		flagArgs = args[:separator]
	}
	if err := flags.Parse(flagArgs); err != nil {
		return err
	}
	if separator < 0 {
		positional := flags.Args()
		if len(positional) > 0 && isDevInspectionOperation(positional[0]) {
			operationArgs, err := parseDevOperationArgs(positional[1:], &configPath, &profile, &values.api, &format, &shell, &showSensitive, &follow, &cleanSecrets)
			if err != nil {
				return err
			}
			if format != "" && format != "json" && format != "env" {
				return errors.New("--format must be json or env")
			}
			if follow {
				operationArgs = append(operationArgs, "--follow")
			}
			if cleanSecrets {
				operationArgs = append(operationArgs, "--secrets")
			}
			return runDevInspection(positional[0], operationArgs, configPath, profile, values.api, shell, format, showSensitive, in, out, errOut)
		}
		if format != "" {
			if err := values.inferIdentity(); err != nil {
				return err
			}
			resolution, err := resolveWrapperValues(values)
			if err != nil {
				return err
			}
			return writeWrapperResolution(resolution, format, out)
		}
		if len(positional) > 1 {
			return errors.New("webport dev accepts at most one service name")
		}
		service := ""
		if len(positional) == 1 {
			service = positional[0]
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer signal.Stop(signals)
		var received atomic.Int32
		received.Store(int32(syscall.SIGTERM))
		go func() {
			select {
			case sig := <-signals:
				received.Store(int32(sig.(syscall.Signal)))
				cancel()
			case <-ctx.Done():
			}
		}()
		return devsession.Run(ctx, devsession.Options{
			ConfigPath: configPath, Profile: profile, Service: service, API: values.api,
			In: in, Out: out, ErrOut: errOut,
			StopSignal: func() os.Signal { return syscall.Signal(received.Load()) },
		})
	}
	if format != "" && format != "json" && format != "env" {
		return errors.New("--format must be json or env")
	}
	if separator == len(args)-1 {
		return errors.New("usage: webport dev [options] -- COMMAND [ARG...]")
	}
	if err := values.inferIdentity(); err != nil {
		return err
	}
	if err := requireReady(values.api); err != nil {
		return fmt.Errorf("%w; run \"webport doctor\"", err)
	}
	resolution, err := resolveWrapperValues(values)
	if err != nil {
		return err
	}

	token, err := randomToken()
	if err != nil {
		return err
	}
	commandArgs := args[separator+1:]
	command := exec.Command(commandArgs[0], commandArgs[1:]...)
	command.Stdin, command.Stdout, command.Stderr = in, out, errOut
	command.Env = withEnvironment(os.Environ(), map[string]string{
		"WEBPORT_PROJECT":      resolution.Project,
		"WEBPORT_BRANCH":       resolution.Branch,
		"WEBPORT_ROUTE":        values.project + ":" + values.branch,
		"WEBPORT_HOST":         resolution.Host,
		"WEBPORT_URL":          resolution.URL,
		"WEBPORT_CLIENT_TOKEN": token,
	})
	if values.port > 0 {
		command.Env = withEnvironment(command.Env, map[string]string{"WEBPORT_APP_PORT": fmt.Sprint(values.port)})
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)

	configureChildProcess(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start development command: %w", err)
	}
	child := trackChildProcess(command)

	cleanupChild := true
	defer func() {
		if cleanupChild {
			_ = child.terminate(syscall.SIGTERM, childShutdownGracePeriod)
		}
	}()

	var shutdownSignal os.Signal
	if values.port == 0 {
		var port int
		port, shutdownSignal, err = waitForListener(
			token, values.project, values.branch, startupTimeout, child, signals,
		)
		if err != nil {
			_ = child.terminate(syscall.SIGTERM, childShutdownGracePeriod)
			cleanupChild = false
			return err
		}
		values.port = port
	} else {
		shutdownSignal, err = waitForPort(values.port, startupTimeout, child, signals)
		if err != nil {
			_ = child.terminate(syscall.SIGTERM, childShutdownGracePeriod)
			cleanupChild = false
			return err
		}
	}
	if shutdownSignal != nil {
		_ = child.terminate(shutdownSignal, childShutdownGracePeriod)
		cleanupChild = false
		return nil
	}

	manager, err := newClientManager(values)
	if err != nil {
		_ = child.terminate(syscall.SIGTERM, childShutdownGracePeriod)
		cleanupChild = false
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		_ = child.terminate(syscall.SIGTERM, childShutdownGracePeriod)
		cleanupChild = false
		return err
	}

	var commandErr error
	select {
	case sig := <-signals:
		_ = child.terminate(sig, childShutdownGracePeriod)
	case <-child.done:
		commandErr = child.terminate(syscall.SIGTERM, childShutdownGracePeriod)
	}
	cleanupChild = false
	cancel()
	releaseErr := manager.Stop()
	if commandErr != nil {
		var exitErr *exec.ExitError
		if errors.As(commandErr, &exitErr) {
			return fmt.Errorf("development command exited with status %d", exitErr.ExitCode())
		}
		return commandErr
	}
	return releaseErr
}

func parseDevOperationArgs(args []string, configPath, profile, api, format, shell *string, showSensitive, follow, cleanSecrets *bool) ([]string, error) {
	positional := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		name, value, hasValue := strings.Cut(argument, "=")
		next := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if index+1 >= len(args) {
				return "", fmt.Errorf("option %s requires a value", name)
			}
			index++
			return args[index], nil
		}
		switch name {
		case "--config":
			resolved, err := next()
			if err != nil {
				return nil, err
			}
			*configPath = resolved
		case "--profile":
			resolved, err := next()
			if err != nil {
				return nil, err
			}
			*profile = resolved
		case "--api":
			resolved, err := next()
			if err != nil {
				return nil, err
			}
			*api = resolved
		case "--format":
			resolved, err := next()
			if err != nil {
				return nil, err
			}
			*format = resolved
		case "--shell":
			resolved, err := next()
			if err != nil {
				return nil, err
			}
			*shell = resolved
		case "--show-sensitive":
			if hasValue {
				return nil, fmt.Errorf("option %s does not take a value", name)
			}
			*showSensitive = true
		case "--follow":
			if hasValue {
				return nil, fmt.Errorf("option %s does not take a value", name)
			}
			*follow = true
		case "--secrets":
			if hasValue {
				return nil, fmt.Errorf("option %s does not take a value", name)
			}
			*cleanSecrets = true
		default:
			positional = append(positional, argument)
		}
	}
	return positional, nil
}

func isDevInspectionOperation(operation string) bool {
	switch operation {
	case "check", "config", "status", "env", "stop", "logs", "clean":
		return true
	default:
		return false
	}
}

func runDevInspection(operation string, operationArgs []string, configPath, profile, api, shell, format string, showSensitive bool, in io.Reader, out, errOut io.Writer) error {
	options := devsession.Options{ConfigPath: configPath, Profile: profile, API: api, In: in, Out: out}
	switch operation {
	case "check", "config":
		options.PreferLive, options.ShowSensitive = operation == "config", showSensitive
		resolved, err := devsession.InspectConfig(context.Background(), options)
		if err != nil {
			return err
		}
		if operation == "check" {
			_, err = fmt.Fprintln(out, "PASS development session configuration is valid")
			return err
		}
		if format == "json" {
			encoded, encodeErr := resolved.JSON(showSensitive)
			if encodeErr != nil {
				return encodeErr
			}
			_, err = fmt.Fprintln(out, string(encoded))
			return err
		}
		_, err = io.WriteString(out, resolved.Human(showSensitive))
		return err
	case "status":
		value, err := devsession.Status(options)
		if err != nil {
			return err
		}
		if format == "json" {
			encoded, encodeErr := json.MarshalIndent(value, "", "  ")
			if encodeErr != nil {
				return encodeErr
			}
			_, err = fmt.Fprintln(out, string(encoded))
			return err
		}
		_, err = fmt.Fprintf(out, "development session status\n%v\n", value)
		return err
	case "env":
		response, err := devsession.Control(context.Background(), options, "env", map[string]any{"show_sensitive": showSensitive})
		if err != nil {
			return err
		}
		if !response.OK {
			return errors.New(response.Error)
		}
		values := map[string]string{}
		if raw, ok := response.Payload["values"].(map[string]any); ok {
			for name, value := range raw {
				if stringValue, ok := value.(string); ok {
					values[name] = stringValue
				}
			}
		}
		if format == "json" {
			shell = "json"
		}
		rendered, err := devsession.RenderEnvironment(values, shell)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, rendered)
		return err
	case "stop":
		if len(operationArgs) > 0 {
			return errors.New("webport dev stop accepts no arguments")
		}
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := devsession.Control(stopContext, options, "stop", nil)
		if err != nil {
			return err
		}
		if !response.OK {
			return errors.New(response.Error)
		}
		_, err = fmt.Fprintln(out, "development session stop requested")
		return err
	case "logs":
		service := ""
		follow := false
		for _, argument := range operationArgs {
			if argument == "--follow" {
				follow = true
				continue
			}
			if strings.HasPrefix(argument, "-") {
				return fmt.Errorf("unknown logs option %q", argument)
			}
			if service != "" {
				return errors.New("webport dev logs accepts at most one service name")
			}
			service = argument
		}
		logContext, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return devsession.StreamLogs(logContext, options, service, follow, out)
	case "clean":
		secrets := false
		for _, argument := range operationArgs {
			if argument == "--secrets" {
				secrets = true
				continue
			}
			return fmt.Errorf("unknown clean option %q", argument)
		}
		if !secrets {
			return errors.New("webport dev clean currently requires --secrets")
		}
		if err := devsession.CleanSecrets(options); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "development session secrets cleaned")
		return err
	default:
		return fmt.Errorf("unsupported development session operation %q", operation)
	}
}

func runDevExec(args []string, in io.Reader, out, errOut io.Writer) error {
	separator := -1
	for index, argument := range args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 1 || separator == len(args)-1 {
		return errors.New("usage: webport dev exec SERVICE -- COMMAND [ARG...]")
	}
	flags := flag.NewFlagSet("webport dev exec", flag.ContinueOnError)
	flags.SetOutput(errOut)
	var configPath, profile, api string
	flags.StringVar(&configPath, "config", "", "development session configuration path")
	flags.StringVar(&profile, "profile", "", "development session profile")
	flags.StringVar(&api, "api", defaultAPI, "webport API URL")
	if err := flags.Parse(args[:separator]); err != nil {
		return err
	}
	if len(flags.Args()) != 1 {
		return errors.New("webport dev exec requires one service name")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return devsession.Exec(ctx, devsession.Options{ConfigPath: configPath, Profile: profile, API: api, In: in, Out: out, ErrOut: errOut}, flags.Args()[0], args[separator+1:], in, out, errOut)
}

type wrapperResolution struct {
	SchemaVersion int    `json:"schema_version"`
	Project       string `json:"project"`
	Branch        string `json:"branch"`
	Route         string `json:"route"`
	Host          string `json:"host"`
	URL           string `json:"url"`
	Port          int    `json:"port,omitempty"`
}

func resolveWrapperValues(values routeFlags) (wrapperResolution, error) {
	httpClient, err := daemon.NewHTTPClient(values.api, nil, nil)
	if err != nil {
		return wrapperResolution{}, err
	}
	serverConfig, err := httpClient.Config(context.Background())
	if err != nil {
		return wrapperResolution{}, err
	}
	host, err := route.BuildDomainChecked(serverConfig.BaseDomain, values.project, values.branch)
	if err != nil {
		return wrapperResolution{}, fmt.Errorf("resolve route hostname: %w", err)
	}
	return wrapperResolution{
		SchemaVersion: 1,
		Project:       values.project,
		Branch:        values.branch,
		Route:         values.project + ":" + values.branch,
		Host:          host,
		URL:           "https://" + host,
		Port:          values.port,
	}, nil
}

func writeWrapperResolution(resolution wrapperResolution, format string, out io.Writer) error {
	switch format {
	case "json":
		encoded, err := json.MarshalIndent(resolution, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	case "env":
		values := map[string]string{
			"WEBPORT_PROJECT": resolution.Project,
			"WEBPORT_BRANCH":  resolution.Branch,
			"WEBPORT_ROUTE":   resolution.Route,
			"WEBPORT_HOST":    resolution.Host,
			"WEBPORT_URL":     resolution.URL,
		}
		if resolution.Port > 0 {
			values["WEBPORT_APP_PORT"] = fmt.Sprint(resolution.Port)
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, err := fmt.Fprintf(out, "%s=%s\n", key, shellQuote(values[key])); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func withEnvironment(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	for _, item := range base {
		name, _, found := strings.Cut(item, "=")
		if found {
			if _, replace := values[name]; replace {
				continue
			}
		}
		result = append(result, item)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func newClientManager(values routeFlags) (*client.Manager, error) {
	return client.NewManager(client.Config{
		Project: values.project, Branch: values.branch, Port: values.port,
		TTL: values.ttl, Interval: values.ttl / 3,
		APIAddr: values.api, Verbose: values.verbose,
	})
}

func waitForListener(
	token, project, branch string,
	timeout time.Duration,
	child *childProcess,
	signals <-chan os.Signal,
) (int, os.Signal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	scanner := discovery.Scanner{BaseDomain: "webport.invalid", ClientToken: token}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastErrors []error
	for {
		routes, scanErrors := scanner.Scan(ctx)
		lastErrors = scanErrors
		ports := make(map[int]struct{})
		for _, item := range routes {
			if item.Project == project && item.Branch == branch {
				ports[item.Port] = struct{}{}
			}
		}
		if len(ports) == 1 {
			for port := range ports {
				return port, nil, nil
			}
		}
		if len(ports) > 1 {
			return 0, nil, errors.New("multiple HTTP listeners found; select one with --port")
		}
		select {
		case sig := <-signals:
			return 0, sig, nil
		case <-child.done:
			err := child.wait()
			if err != nil {
				return 0, nil, fmt.Errorf("development command exited before opening an HTTP listener: %w", err)
			}
			return 0, nil, errors.New("development command exited before opening an HTTP listener")
		case <-ctx.Done():
			if len(lastErrors) > 0 {
				return 0, nil, fmt.Errorf("no HTTP listener found before timeout: %v", lastErrors[len(lastErrors)-1])
			}
			return 0, nil, errors.New("no HTTP listener found before timeout; use --port to select it")
		case <-ticker.C:
		}
	}
}

func waitForPort(
	port int,
	timeout time.Duration,
	child *childProcess,
	signals <-chan os.Signal,
) (os.Signal, error) {
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil, nil
		}
		select {
		case sig := <-signals:
			return sig, nil
		case <-child.done:
			err := child.wait()
			if err != nil {
				return nil, fmt.Errorf("development command exited before port %d opened: %w", port, err)
			}
			return nil, fmt.Errorf("development command exited before port %d opened", port)
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("port %d did not start listening before timeout", port)
}

func requireReady(api string) error {
	resp, err := http.Get(strings.TrimRight(api, "/") + "/ready")
	if err != nil {
		return fmt.Errorf("webport daemon is unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webport daemon is not ready (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func printEndpoint(path string) error {
	return printEndpointTo(defaultAPI, path, os.Stdout)
}

func printEndpointTo(api, path string, out io.Writer) error {
	resp, err := http.Get(strings.TrimRight(api, "/") + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", path, resp.StatusCode)
	}
	var value any
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		return err
	}
	output, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(output))
	return err
}

func runDoctor() error {
	return runDoctorTo(os.Stdout)
}

func runDoctorTo(out io.Writer) error {
	fmt.Fprintf(out, "webport %s\n", versionString())
	if err := requireReady(defaultAPI); err != nil {
		return err
	}
	fmt.Fprintln(out, "PASS daemon API is ready")

	var cfg struct {
		BaseDomain string `json:"base_domain"`
		TLSMode    string `json:"tls_mode"`
	}
	resp, err := http.Get(defaultAPI + "/config")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return err
	}
	serverName := "doctor." + cfg.BaseDomain
	raw, err := net.DialTimeout("tcp", "127.0.0.1:443", 3*time.Second)
	if err != nil {
		return fmt.Errorf("Traefik HTTPS listener: %w", err)
	}
	conn := tls.Client(raw, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12})
	if err := conn.Handshake(); err != nil {
		_ = raw.Close()
		return fmt.Errorf("TLS trust/hostname verification for %s: %w", serverName, err)
	}
	state := conn.ConnectionState()
	_ = conn.Close()
	if len(state.PeerCertificates) == 0 {
		return errors.New("Traefik returned no certificate")
	}
	leaf := state.PeerCertificates[0]
	fmt.Fprintf(out, "PASS TLS certificate %s, expires %s\n", leaf.Subject.CommonName, leaf.NotAfter.Format(time.RFC3339))
	if _, err := net.LookupHost(serverName); err != nil {
		return fmt.Errorf("wildcard name resolution for %s: %w", serverName, err)
	}
	fmt.Fprintf(out, "PASS wildcard name resolution for %s\n", serverName)
	return nil
}

func runCompatibilityCommand(name string, args []string) error {
	return runCompatibilityCommandTo(name, args, os.Stdin, os.Stdout, os.Stderr)
}

func runCompatibilityCommandTo(name string, args []string, in io.Reader, out, errOut io.Writer) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is not installed", name)
	}
	command := exec.Command(path, args...)
	command.Stdin, command.Stdout, command.Stderr = in, out, errOut
	return command.Run()
}

func runInstaller(args []string) error {
	return runInstallerTo(args, os.Stdin, os.Stdout, os.Stderr)
}

func runInstallerTo(args []string, in io.Reader, out, errOut io.Writer) error {
	candidates := []string{
		filepath.Join("scripts", platformInstaller()),
		filepath.Join("/usr/local/libexec/webport/installer/scripts", platformInstaller()),
		filepath.Join(filepath.Dir(os.Args[0]), "scripts", platformInstaller()),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			command := exec.Command(candidate, args...)
			command.Stdin, command.Stdout, command.Stderr = in, out, errOut
			return command.Run()
		}
	}
	return errors.New("installer assets are unavailable; use the documented bootstrap command")
}

func platformInstaller() string {
	if runtime.GOOS == "darwin" {
		return "install-macos.sh"
	}
	return "install.sh"
}

func versionString() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func randomToken() (string, error) {
	var data [18]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}
