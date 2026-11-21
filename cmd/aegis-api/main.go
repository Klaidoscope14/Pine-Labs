// Command aegis-api serves the AegisSDLC HTTP API.
//
// Phase 0 scope: load and guard the config, then serve health endpoints.
// It refuses to start if the config is invalid.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aegis/internal/config"
)

func main() {
	path := flag.String("config", os.Getenv("AEGIS_CONFIG"), "path to config YAML (or AEGIS_CONFIG)")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if *path == "" {
		log.Error("no config given; set -config or AEGIS_CONFIG")
		os.Exit(2)
	}
	cfg, err := config.LoadFile(*path)
	if err != nil {
		log.Error("refusing to start: config rejected", "error", err.Error())
		os.Exit(1)
	}
	log.Info("config accepted", "environment", cfg.Deployment.Environment, "config_hash", cfg.Hash)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"config_hash": cfg.Hash, "environment": cfg.Deployment.Environment})
	})

	srv := &http.Server{
		Addr:              cfg.API.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", cfg.API.Listen)
	if err != nil {
		log.Error("cannot listen", "addr", cfg.API.Listen, "error", err.Error())
		os.Exit(1)
	}
	log.Info("listening", "addr", ln.Addr().String())
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err.Error())
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	log.Info("stopped")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(v)
}
