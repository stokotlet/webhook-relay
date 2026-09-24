package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stokotlet/webhook-relay/internal/api"
	"github.com/stokotlet/webhook-relay/internal/config"
	"github.com/stokotlet/webhook-relay/internal/delivery"
	"github.com/stokotlet/webhook-relay/internal/metrics"
	"github.com/stokotlet/webhook-relay/internal/store"
	"github.com/stokotlet/webhook-relay/internal/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}
func run(log *slog.Logger) error {
	mode := flag.String("mode", "all", "all, api, worker, or migrate")
	flag.Parse()
	if *mode != "all" && *mode != "api" && *mode != "worker" && *mode != "migrate" {
		return fmt.Errorf("unknown mode %q", *mode)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if *mode == "migrate" {
		return store.Migrate(startup, cfg.DatabaseURL)
	}
	queue, err := store.Open(startup, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer queue.Pool.Close()
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	stats := metrics.New(reg)
	admin := http.NewServeMux()
	admin.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	admin.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	admin.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, c := context.WithTimeout(r.Context(), time.Second)
		defer c()
		var exists bool
		err := queue.Pool.QueryRow(pingCtx, "SELECT to_regclass('deliveries') IS NOT NULL").Scan(&exists)
		if err != nil || !exists {
			http.Error(w, "not ready", 503)
			return
		}
		w.WriteHeader(200)
	})
	servers := []*http.Server{server(cfg.AdminAddr, admin)}
	if *mode != "worker" {
		a := &api.API{Store: queue, Key: cfg.APIKey, AllowPrivate: cfg.AllowPrivate, Log: log}
		servers = append(servers, server(cfg.HTTPAddr, a.Handler()))
	}
	errorsCh := make(chan error, len(servers))
	for _, srv := range servers {
		go func() { log.Info("listening", "address", srv.Addr, "mode", *mode); errorsCh <- srv.ListenAndServe() }()
	}
	workerDone := make(chan struct{})
	sender := delivery.NewSender(cfg.AllowPrivate)
	defer sender.Close()
	go func() {
		defer close(workerDone)
		if *mode != "api" {
			w := worker.Worker{Queue: queue, Sender: sender, Metrics: stats, Log: log}
			w.Run(ctx, cfg.Workers)
		}
	}()
	select {
	case <-ctx.Done():
	case err = <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	stop()
	shutdown, c := context.WithTimeout(context.Background(), 15*time.Second)
	defer c()
	for _, srv := range servers {
		if e := srv.Shutdown(shutdown); e != nil {
			_ = srv.Close()
			log.Error("forced HTTP shutdown", "error", e)
		}
	}
	<-workerDone
	return err
}
func server(address string, handler http.Handler) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
}
