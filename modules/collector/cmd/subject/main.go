package main

import (
	"context"
	"flag"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	configPath := flag.String("conf", "config/subject.yaml", "runtime config path")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := subjectsync.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("moox-collector-subject config error: %v", err)
	}
	listers, err := marketwiring.NewSubjectListers()
	if err != nil {
		log.Fatalf("moox-collector-subject source initialization failed: %v", err)
	}
	storage, err := subjectsync.NewStorageClient(cfg.Storage.Target)
	if err != nil {
		log.Fatalf("moox-collector-subject storage initialization failed: %v", err)
	}
	if err := storage.RegisterSubjectListing(ctx, listers.Supported()); err != nil {
		log.Fatalf("moox-collector-subject source registration failed: %v", err)
	}
	metrics := subjectsync.NewMetrics(nil)
	state := healthz.NewState("collector-subject", "", "", "")
	healthHandler, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		log.Fatalf("moox-collector-subject health initialization failed: %v", err)
	}
	go func() {
		if err := http.ListenAndServe(cfg.HealthAddr, healthHandler); err != nil {
			log.Errorf("collector-subject health server: %v", err)
		}
	}()
	state.SetReady(true)
	reporter, err := report.NewHandler(report.DefaultConfig("collector", "moox_collector_subject"))
	if err != nil {
		log.Fatalf("moox-collector-subject metrics reporter initialization failed: %v", err)
	}
	go reportMetrics(ctx, reporter)
	log.Info("starting moox-collector-subject")
	(&subjectsync.Service{
		Tags:       &subjectsync.TagRunner{Store: storage, Listers: listers, FetchTimeout: cfg.FetchTimeout, Metrics: metrics},
		Attributes: &subjectsync.AttributeRunner{Store: storage, Listers: listers, Jobs: cfg.Attributes, FetchTimeout: cfg.FetchTimeout, Metrics: metrics},
		Poll:       cfg.PollInterval,
	}).Run(ctx)
}

// reportMetrics publishes the process metrics snapshot every 30s, like the
// tRPC services' metrics timers, so Monitor sees collector-subject as a live
// reporter. A failed publish is logged and retried on the next tick.
func reportMetrics(ctx context.Context, reporter *report.Handler) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := reporter.Handle(ctx); err != nil && ctx.Err() == nil {
			log.Warnf("moox-collector-subject metrics report failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
