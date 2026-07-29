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
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	gitdetect "github.com/webportdev/webport/cmd/webportctl/git"
	client "github.com/webportdev/webport/cmd/webportctl/runtime"
	"github.com/webportdev/webport/internal/discovery"
)

const defaultAPI = "http://127.0.0.1:8080"

func main() {
	if len(os.Args) == 1 {
		// Compatibility with existing service definitions.
		runDaemon()
		return
	}

	var err error
	switch os.Args[1] {
	case "daemon":
		runDaemon()
	case "version", "--version":
		fmt.Println(versionString())
	case "help", "-h", "--help":
		printHelp()
	case "dev":
		err = runDev(os.Args[2:])
	case "route":
		err = runRoute(os.Args[2:])
	case "list":
		err = printEndpoint("/routes")
	case "status":
		err = printEndpoint("/status")
	case "config":
		err = printEndpoint("/config")
	case "doctor":
		err = runDoctor()
	case "dns":
		err = runCompatibilityCommand("webport-dns", os.Args[2:])
	case "install":
		err = runInstaller(os.Args[2:])
	default:
		printHelp()
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`webport - HTTPS routes for local development servers

Usage:
  webport install [options]
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
`)
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
	separator := -1
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator == len(args)-1 {
		return errors.New("usage: webport dev [options] -- COMMAND [ARG...]")
	}
	flags := flag.NewFlagSet("webport dev", flag.ContinueOnError)
	var values routeFlags
	var startupTimeout time.Duration
	addRouteFlags(flags, &values)
	flags.DurationVar(&startupTimeout, "startup-timeout", 30*time.Second, "time to wait for an HTTP listener")
	if err := flags.Parse(args[:separator]); err != nil {
		return err
	}
	if err := values.inferIdentity(); err != nil {
		return err
	}
	if err := requireReady(values.api); err != nil {
		return fmt.Errorf("%w; run \"webport doctor\"", err)
	}

	token, err := randomToken()
	if err != nil {
		return err
	}
	commandArgs := args[separator+1:]
	command := exec.Command(commandArgs[0], commandArgs[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Env = append(os.Environ(),
		"WEBPORT_ROUTE="+values.project+":"+values.branch,
		"WEBPORT_CLIENT_TOKEN="+token,
	)
	if values.port > 0 {
		command.Env = append(command.Env, fmt.Sprintf("WEBPORT_APP_PORT=%d", values.port))
	}
	configureChildProcess(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start development command: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	cleanupChild := true
	defer func() {
		if cleanupChild && command.Process != nil {
			_ = signalChildProcess(command, syscall.SIGTERM)
		}
	}()

	if values.port == 0 {
		port, err := waitForListener(token, values.project, values.branch, startupTimeout, done)
		if err != nil {
			_ = signalChildProcess(command, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
			return err
		}
		values.port = port
	} else if err := waitForPort(values.port, startupTimeout, done); err != nil {
		_ = signalChildProcess(command, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return err
	}

	manager, err := newClientManager(values)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		_ = signalChildProcess(command, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case sig := <-signals:
		_ = signalChildProcess(command, sig)
		err = <-done
	case err = <-done:
	}
	cleanupChild = false
	cancel()
	releaseErr := manager.Stop()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("development command exited with status %d", exitErr.ExitCode())
		}
		return err
	}
	return releaseErr
}

func newClientManager(values routeFlags) (*client.Manager, error) {
	return client.NewManager(client.Config{
		Project: values.project, Branch: values.branch, Port: values.port,
		TTL: values.ttl, Interval: values.ttl / 3,
		APIAddr: values.api, Verbose: values.verbose,
	})
}

func waitForListener(token, project, branch string, timeout time.Duration, done <-chan error) (int, error) {
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
				return port, nil
			}
		}
		if len(ports) > 1 {
			return 0, errors.New("multiple HTTP listeners found; select one with --port")
		}
		select {
		case err := <-done:
			if err != nil {
				return 0, fmt.Errorf("development command exited before opening an HTTP listener: %w", err)
			}
			return 0, errors.New("development command exited before opening an HTTP listener")
		case <-ctx.Done():
			if len(lastErrors) > 0 {
				return 0, fmt.Errorf("no HTTP listener found before timeout: %v", lastErrors[len(lastErrors)-1])
			}
			return 0, errors.New("no HTTP listener found before timeout; use --port to select it")
		case <-ticker.C:
		}
	}
}

func waitForPort(port int, timeout time.Duration, done <-chan error) error {
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("development command exited before port %d opened: %w", port, err)
			}
			return fmt.Errorf("development command exited before port %d opened", port)
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("port %d did not start listening before timeout", port)
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
	resp, err := http.Get(defaultAPI + path)
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
	output, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(output))
	return nil
}

func runDoctor() error {
	fmt.Printf("webport %s\n", versionString())
	if err := requireReady(defaultAPI); err != nil {
		return err
	}
	fmt.Println("PASS daemon API is ready")

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
	fmt.Printf("PASS TLS certificate %s, expires %s\n", leaf.Subject.CommonName, leaf.NotAfter.Format(time.RFC3339))
	if _, err := net.LookupHost(serverName); err != nil {
		return fmt.Errorf("wildcard name resolution for %s: %w", serverName, err)
	}
	fmt.Printf("PASS wildcard name resolution for %s\n", serverName)
	return nil
}

func runCompatibilityCommand(name string, args []string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is not installed", name)
	}
	command := exec.Command(path, args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}

func runInstaller(args []string) error {
	candidates := []string{
		filepath.Join("scripts", platformInstaller()),
		filepath.Join("/usr/local/libexec/webport/installer/scripts", platformInstaller()),
		filepath.Join(filepath.Dir(os.Args[0]), "scripts", platformInstaller()),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			command := exec.Command(candidate, args...)
			command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
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
