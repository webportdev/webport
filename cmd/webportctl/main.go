// Package main provides the webportctl binary.
//
// webportctl is a companion CLI for webport that:
// 1. Registers a route on startup
// 2. Sends periodic heartbeats to keep the route alive
// 3. Unregisters the route on exit (graceful shutdown)
//
// Usage:
//
//	webportctl -port 3000
//	webportctl -project myapp -branch main -port 3000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/webportdev/webport/cmd/webportctl/git"
	"github.com/webportdev/webport/cmd/webportctl/runtime"
)

var (
	// Flags
	flagProject   string
	flagBranch    string
	flagPort      int
	flagTTL       int
	flagAPIAddr   string
	flagInterval  int
	flagVerbose   bool
	flagQueryConf bool
)

func init() {
	flag.StringVar(&flagProject, "project", getEnv("WEBPORT_PROJECT", ""), "Project name (auto-detected from git repo if not set)")
	flag.StringVar(&flagBranch, "branch", getEnv("WEBPORT_BRANCH", ""), "Branch name (auto-detected from git if not set)")
	flag.IntVar(&flagPort, "port", getEnvInt("APP_PORT", 0), "Local app port to proxy to (required)")
	flag.IntVar(&flagTTL, "ttl", getEnvInt("WEBPORT_TTL", 300), "Route TTL in seconds")
	flag.StringVar(&flagAPIAddr, "api", getEnv("WEBPORT_API_ADDR", "localhost:8080"), "Webport API address (host:port)")
	flag.IntVar(&flagInterval, "interval", getEnvInt("WEBPORT_INTERVAL", 60), "Heartbeat interval in seconds")
	flag.BoolVar(&flagVerbose, "v", getEnvBool("WEBPORT_VERBOSE", false), "Verbose mode (log heartbeat messages)")
	flag.BoolVar(&flagQueryConf, "query-config", false, "Query server configuration and exit")
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		var i int
		if _, err := fmt.Sscanf(val, "%d", &i); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		return val == "1" || val == "true" || val == "yes"
	}
	return defaultVal
}

func main() {
	flag.Usage = usage
	flag.Parse()

	// Handle query-config subcommand
	if flagQueryConf {
		if err := queryConfig(); err != nil {
			logFatal("Error querying config: %v", err)
		}
		return
	}

	// Validate required flags
	if flagPort <= 0 || flagPort > 65535 {
		logFatal("Error: -port is required and must be between 1-65535")
	}

	if flagInterval <= 0 {
		logFatal("Error: -interval must be positive")
	}

	if flagTTL <= 0 {
		logFatal("Error: -ttl must be positive")
	}

	// Auto-detect project and branch from git if not set
	project := flagProject
	branch := flagBranch

	if project == "" {
		detected, err := git.DetectProject()
		if err != nil {
			logFatal("Error detecting project: %v\n\nSet -project explicitly or run from within a git repository.", err)
		}
		project = detected
		log.Printf("Auto-detected project: %s", project)
	}

	if branch == "" {
		detected, err := git.DetectBranch()
		if err != nil {
			log.Printf("Warning: could not detect branch: %v", err)
			detected = "unknown"
		}
		branch = detected
		log.Printf("Auto-detected branch: %s", branch)
	}

	// Build configuration
	cfg := runtime.Config{
		Project:  project,
		Branch:   branch,
		Port:     flagPort,
		TTL:      time.Duration(flagTTL) * time.Second,
		APIAddr:  flagAPIAddr,
		Interval: time.Duration(flagInterval) * time.Second,
		Verbose:  flagVerbose,
	}

	// Create runtime manager
	manager, err := runtime.NewManager(cfg)
	if err != nil {
		logFatal("Error creating manager: %v", err)
	}

	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start the manager (registers route and starts heartbeat)
	if err := manager.Start(ctx); err != nil {
		logFatal("Error starting manager: %v", err)
	}

	// Wait for shutdown signal
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down...", sig)

	// Cleanup (unregister route)
	if err := manager.Stop(); err != nil {
		log.Printf("Warning: cleanup failed: %v", err)
		os.Exit(1)
	}

	log.Println("Graceful shutdown complete")
}

// queryConfig queries and prints the server configuration
func queryConfig() error {
	cfg := runtime.Config{
		APIAddr: flagAPIAddr,
	}
	manager, err := runtime.NewManager(cfg)
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}

	config, err := manager.GetConfig()
	if err != nil {
		return err
	}

	fmt.Printf("Base Domain: %s\n", config.BaseDomain)
	fmt.Printf("Default TTL: %d seconds\n", config.DefaultTTLSecs)
	return nil
}

func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), "webportctl - Register and maintain a route with webport\n\n")
	fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  webportctl -port <port> [options]\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  webportctl -query-config [options]\n\n")
	fmt.Fprintf(flag.CommandLine.Output(), "Options:\n")
	flag.PrintDefaults()
	fmt.Fprintf(flag.CommandLine.Output(), "\nExamples:\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  # Query server configuration\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  webportctl -query-config\n\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  # Auto-detect project/branch from git\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  webportctl -port 3000\n\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  # Explicit values\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  webportctl -project myapp -branch main -port 3000\n\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  # Custom TTL and heartbeat interval\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  webportctl -port 3000 -ttl 600 -interval 30\n\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  # Verbose mode (shows heartbeat logs)\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  webportctl -port 3000 -v\n\n")
	fmt.Fprintf(flag.CommandLine.Output(), "Environment Variables:\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  WEBPORT_PROJECT   Same as -project\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  WEBPORT_BRANCH    Same as -branch\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  WEBPORT_TTL       Same as -ttl\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  WEBPORT_API_ADDR  Same as -api\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  WEBPORT_INTERVAL  Same as -interval\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  WEBPORT_VERBOSE   Same as -v\n")
	fmt.Fprintf(flag.CommandLine.Output(), "  APP_PORT          Same as -port\n")
}

func logFatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
