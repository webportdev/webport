package main

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/webportdev/webport/internal/api"
	"github.com/webportdev/webport/internal/config"
	"github.com/webportdev/webport/internal/route"
	"github.com/webportdev/webport/internal/shutdown"
	"github.com/webportdev/webport/internal/traefik"

	"github.com/coreos/go-systemd/v22/daemon"
)

func main() {
	cfg := config.Load()
	store := route.NewStore()
	writer := traefik.NewWriter(cfg.TraefikDynamicConfigPath)
	proxyCfg := traefik.Config{
		EntryPoint:   cfg.TraefikEntryPoint,
		CertResolver: cfg.TraefikCertResolver,
	}

	publishTraefikConfig := func() error {
		content, err := traefik.GenerateDynamicConfig(traefik.RoutesToRouteInfos(store.List()), proxyCfg, cfg.BaseDomain)
		if err != nil {
			return err
		}
		if err := writer.Write(content); err != nil {
			return err
		}
		return nil
	}

	ttlChecker := route.NewTTLChecker(store, cfg.TTLCheckInterval, func(_ []route.Route) {
		if err := publishTraefikConfig(); err != nil {
			log.Printf("WARN: failed to publish Traefik configuration after route expiry: %v", err)
		}
	})
	if err := publishTraefikConfig(); err != nil {
		log.Fatalf("Failed to publish initial Traefik configuration: %v", err)
	}
	ttlChecker.Start()

	mux := http.NewServeMux()
	api.NewHandlers(store, writer, cfg).RegisterRoutes(mux)
	server := &http.Server{
		Addr:              cfg.GetListenAddr(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	watchdogStop := startWatchdog()
	shutdownManager := shutdown.NewManager(store, writer, server, ttlChecker, cfg.ShutdownTimeout, proxyCfg, cfg.BaseDomain)
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
