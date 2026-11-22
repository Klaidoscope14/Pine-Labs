// Command aegis-api serves the AegisSDLC web app and JSON API (PLAN.md §10.7).
// It refuses to start if the config is invalid. It listens on the configured
// address (loopback by default; reach it through the SSM tunnel on AWS).
package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aegis/internal/app"
	"aegis/internal/config"
	"aegis/internal/web"
)

func main() {
	path := flag.String("config", os.Getenv("AEGIS_CONFIG"), "path to config YAML (or AEGIS_CONFIG)")
	flag.Parse()
	log := app.Logger()
	if *path == "" {
		log.Error("no config given; set -config or AEGIS_CONFIG")
		os.Exit(2)
	}
	cfg, err := config.LoadFile(*path)
	if err != nil {
		log.Error("refusing to start: config rejected", "error", err.Error())
		os.Exit(1)
	}
	key, err := config.ResolveSecret(cfg.API.SessionKeyRef)
	if err != nil || len(key) < 32 {
		log.Error("refusing to start: api.session_key_ref must resolve to at least 32 bytes (run make secrets)")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := app.Ledger(ctx, cfg)
	if err != nil {
		log.Error("ledger", "error", err.Error())
		os.Exit(1)
	}
	defer store.Close()
	tc, err := app.Temporal(cfg, log)
	if err != nil {
		log.Error("temporal", "error", err.Error())
		os.Exit(1)
	}
	defer tc.Close()
	adminDSN, err := cfg.DSN(cfg.Ledger.AdminEndpoint)
	if err != nil {
		log.Error("ledger admin endpoint", "error", err.Error())
		os.Exit(1)
	}
	srv, err := web.New(cfg, store, tc, adminDSN, []byte(key), log)
	if err != nil {
		log.Error("web", "error", err.Error())
		os.Exit(1)
	}
	if cfg.API.BFFTokenRef != "" {
		tok, err := config.ResolveSecret(cfg.API.BFFTokenRef)
		if err != nil || len(tok) < 32 {
			log.Error("refusing to start: api.bff_token_ref must resolve to at least 32 bytes")
			os.Exit(1)
		}
		srv.BFF = []byte(tok)
	}
	if cfg.API.OriginSecretRef != "" {
		sec, err := config.ResolveSecret(cfg.API.OriginSecretRef)
		if err != nil || len(sec) < 32 {
			log.Error("refusing to start: api.origin_secret_ref must resolve to at least 32 bytes")
			os.Exit(1)
		}
		srv.OriginSecret = []byte(sec)
	} else if host, _, _ := net.SplitHostPort(cfg.API.Listen); host != "127.0.0.1" && host != "localhost" && host != "::1" {
		log.Error("refusing to start: listening beyond loopback requires api.origin_secret_ref (CloudFront only)")
		os.Exit(1)
	}

	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No write timeout: live updates stream (each stream ends after 10 minutes).
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 16,
	}
	ln, err := net.Listen("tcp", cfg.API.Listen)
	if err != nil {
		log.Error("cannot listen", "addr", cfg.API.Listen, "error", err.Error())
		os.Exit(1)
	}
	log.Info("web app listening", "addr", "http://"+ln.Addr().String(), "environment", cfg.Deployment.Environment, "config_hash", cfg.Hash)
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err.Error())
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdown)
	log.Info("stopped")
}
