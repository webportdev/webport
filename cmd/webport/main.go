package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/webportdev/webport/internal/api"
	"github.com/webportdev/webport/internal/config"
	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/discovery"
	"github.com/webportdev/webport/internal/hub"
	"github.com/webportdev/webport/internal/localca"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/shutdown"
	"github.com/webportdev/webport/internal/socket"
	"github.com/webportdev/webport/internal/traefik"
	webuiserver "github.com/webportdev/webport/internal/webui"
	webuiassets "github.com/webportdev/webport/webui"

	"github.com/coreos/go-systemd/v22/daemon"
)

func runDaemon() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}
	store := route.NewStore()
	writer := traefik.NewWriter(cfg.TraefikDynamicConfigPath)
	proxyCfg := traefik.Config{
		EntryPoint:   cfg.TraefikEntryPoint,
		CertResolver: cfg.TraefikCertResolver,
	}
	var localCAEnabled bool
	if cfg.TLSMode == config.TLSModeLocalCA {
		paths, err := localca.Ensure(cfg.LocalCADir, cfg.BaseDomain)
		if err != nil {
			log.Fatalf("Failed to initialize local CA: %v", err)
		}
		proxyCfg.CertResolver = ""
		proxyCfg.CertFile = paths.Cert
		proxyCfg.KeyFile = paths.Key
		localCAEnabled = true
		if paths.CreatedCA {
			log.Printf("Created local CA; trust its public certificate at %s", paths.CACert)
		}
		log.Printf("Local-CA TLS enabled for *.%s", cfg.BaseDomain)
	}

	var publishMu sync.Mutex
	publishTraefikConfig := func() error {
		publishMu.Lock()
		defer publishMu.Unlock()

		content, err := traefik.GenerateDynamicConfig(traefik.RoutesToRouteInfos(store.List()), proxyCfg, cfg.BaseDomain)
		if err != nil {
			return err
		}
		if err := writer.Write(content); err != nil {
			return err
		}
		return nil
	}

	controller := route.NewController(store, publishTraefikConfig)
	if err := controller.Initialize(); err != nil {
		log.Fatalf("Failed to publish initial Traefik configuration: %v", err)
	}
	controller.Start(cfg.TTLCheckInterval, cfg.BaseDomain)

	var stopLocalCARenewal func()
	if localCAEnabled {
		renewCtx, cancelRenewal := context.WithCancel(context.Background())
		renewalDone := make(chan struct{})
		stopLocalCARenewal = func() {
			cancelRenewal()
			<-renewalDone
		}
		go func() {
			defer close(renewalDone)
			ticker := time.NewTicker(12 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-renewCtx.Done():
					return
				case <-ticker.C:
					paths, err := localca.Ensure(cfg.LocalCADir, cfg.BaseDomain)
					if err != nil {
						log.Printf("WARN: failed to renew local CA certificate: %v", err)
						continue
					}
					if paths.CreatedCA {
						log.Printf("WARN: local CA was replaced; clients must trust %s again", paths.CACert)
					}
					if paths.Changed {
						if err := publishTraefikConfig(); err != nil {
							log.Printf("WARN: failed to publish renewed local certificate: %v", err)
						}
					}
				}
			}
		}()
	}

	var discoveryManager *discovery.Manager
	if cfg.DiscoveryEnabled && discovery.Supported() {
		discoveryManager = discovery.NewManager(discovery.Scanner{
			BaseDomain: cfg.BaseDomain,
		}, store, cfg.DiscoveryInterval, publishTraefikConfig)
		discoveryManager.Start(context.Background())
		log.Printf("Process discovery enabled (interval %s)", cfg.DiscoveryInterval)
	} else if cfg.DiscoveryEnabled {
		log.Printf("WARN: process discovery is not supported on this platform")
	}

	mux := http.NewServeMux()
	handlers := api.NewHandlers(store, writer, cfg).WithController(controller)
	handlers.RegisterRoutes(mux)

	sessions := hub.New()
	webUI, socketServer := startSessionChannel(cfg, controller, store, handlers, sessions)
	server := &http.Server{
		Addr:              cfg.GetListenAddr(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	watchdogStop := startWatchdog()
	shutdownManager := shutdown.NewManager(store, writer, server, nil, cfg.ShutdownTimeout, proxyCfg, cfg.BaseDomain)
	shutdownManager.OnStopping(controller.Stop)
	if stopLocalCARenewal != nil {
		shutdownManager.OnStopping(stopLocalCARenewal)
	}
	if discoveryManager != nil {
		shutdownManager.OnStopping(discoveryManager.Stop)
	}
	if webUI != nil {
		shutdownManager.OnStopping(webUI.Stop)
	}
	if socketServer != nil {
		shutdownManager.OnStopping(func() { _ = socketServer.Close() })
	}
	shutdownManager.OnExit(func() {
		close(watchdogStop)
		notify(daemon.SdNotifyStopping)
	})

	go func() {
		log.Printf("Listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	notify(daemon.SdNotifyReady)
	shutdownManager.Wait()
}

func startWatchdog() chan struct{} {
	stop := make(chan struct{})
	interval, err := daemon.SdWatchdogEnabled(false)
	if err != nil {
		log.Printf("WARN: failed to determine systemd watchdog interval: %v", err)
		return stop
	}
	if interval == 0 {
		return stop
	}

	go func() {
		ticker := time.NewTicker(interval / 2)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				notify(daemon.SdNotifyWatchdog)
			case <-stop:
				return
			}
		}
	}()

	return stop
}

func notify(state string) {
	if _, err := daemon.SdNotify(false, state); err != nil {
		log.Printf("WARN: failed to notify systemd: %v", err)
	}
}

// startSessionChannel opens the authenticated Unix socket that development
// sessions and the webui commands use, and starts the web UI. Failures are
// logged and leave routing unaffected.
func startSessionChannel(cfg *config.Config, controller *route.Controller, store *route.Store, handlers *api.Handlers, sessions *hub.Hub) (*webuiserver.Manager, *socket.Server) {
	webUI, err := webuiserver.NewManager(webuiserver.ManagerOptions{
		SettingsPath: filepath.Join(cfg.StateDir, "webui.json"),
		Listen:       cfg.WebUIListen,
		BaseDomain:   cfg.BaseDomain,
		Routes:       controller,
		Backend: webuiserver.DaemonBackend{
			Hub:    sessions,
			Routes: store.List,
			Status: func(ctx context.Context) map[string]any { return daemonStatus(ctx, cfg, handlers, sessions) },
		},
		ListLive: func() ([]state.LiveState, error) { return sessions.List(), nil },
		Assets:   webuiassets.Assets(),
	})
	handlersByName := map[string]socket.Handler{
		"daemon.status": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return daemonStatus(ctx, cfg, handlers, sessions), nil
		},
	}
	if err != nil {
		log.Printf("WARN: web UI unavailable: %v", err)
		webUI = nil
		message := err.Error()
		unavailable := func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("web UI unavailable: " + message)
		}
		handlersByName["webui.status"], handlersByName["webui.update"] = unavailable, unavailable
	} else {
		if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
			log.Printf("WARN: create state directory %s: %v", cfg.StateDir, err)
		}
		for name, handler := range webUI.SocketHandlers() {
			handlersByName[name] = handler
		}
		webUI.Start()
	}
	daemonUID := os.Getuid()
	allowed := func(uid int) bool {
		return uid == 0 || uid == daemonUID || (cfg.OwnerUID >= 0 && uid == cfg.OwnerUID)
	}
	if cfg.OwnerUID < 0 && daemonUID != 0 {
		log.Printf("WARN: WEBPORT_OWNER_UID is not set; only root and uid %d may use %s", daemonUID, cfg.SocketPath)
	}
	server, err := socket.Listen(cfg.SocketPath, allowed, handlersByName, sessions.Serve)
	if err != nil {
		log.Printf("WARN: session channel unavailable: %v", err)
		return webUI, nil
	}
	log.Printf("Session channel listening on %s", cfg.SocketPath)
	return webUI, server
}

// daemonStatus is the dashboard's Daemon tab: the /status document plus the
// API address, session channel, and Traefik's HTTPS listener.
func daemonStatus(ctx context.Context, cfg *config.Config, handlers *api.Handlers, sessions *hub.Hub) map[string]any {
	result := map[string]any{}
	if data, err := json.Marshal(handlers.Status()); err == nil {
		_ = json.Unmarshal(data, &result)
	}
	result["webport_api"] = "http://" + cfg.GetListenAddr()
	result["session_socket"] = cfg.SocketPath
	result["connected_sessions"] = len(sessions.List())
	dialer := net.Dialer{Timeout: 250 * time.Millisecond}
	status := "not listening at 127.0.0.1:443"
	if conn, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:443"); err == nil {
		status = "listening at 127.0.0.1:443"
		_ = conn.Close()
	}
	result["traefik_https_listener"] = status
	return result
}
