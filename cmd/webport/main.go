package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/webportdev/webport/internal/api"
	"github.com/webportdev/webport/internal/config"
	"github.com/webportdev/webport/internal/discovery"
	"github.com/webportdev/webport/internal/localca"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/shutdown"
	"github.com/webportdev/webport/internal/traefik"

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
	api.NewHandlers(store, writer, cfg).
		WithController(controller).
		RegisterRoutes(mux)
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
