package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate"
	"github.com/mooyang-code/moox/modules/streamcalc/internal/config"
	"github.com/mooyang-code/moox/modules/streamcalc/internal/service"
	"github.com/mooyang-code/moox/modules/streamcalc/internal/state"
	"github.com/mooyang-code/moox/modules/streamcalc/internal/storage"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
)

func main() {
	path := os.Getenv("MOOX_STREAMCALC_CONFIG")
	if path == "" {
		path = "modules/streamcalc/config/app.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		fatal(err)
	}
	client, err := jetstream.Connect(context.Background(), jetstream.ConfigFromEnv(cfg.EventBus.URLs, "streamcalc"))
	if err != nil {
		fatal(err)
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		fatal(err)
	}
	consumer, err := events.NewConsumer(client, jetstream.ConsumerBindRef{Stream: cfg.EventBus.Stream, Durable: cfg.EventBus.Durable, FetchMaxWait: cfg.EventBus.FetchMaxWait}, registry)
	if err != nil {
		fatal(err)
	}
	defer consumer.Close()
	aggregator, err := aggregate.New(cfg.Aggregation.InputFrequency, cfg.Aggregation.TargetFrequency, cfg.Aggregation.AllowedLateness)
	if err != nil {
		fatal(err)
	}
	writer, err := storage.NewRPCWriter(cfg.Storage.Target, cfg.Storage.SpaceID, cfg.Storage.DatasetID, nil)
	if err != nil {
		fatal(err)
	}
	processor, err := service.NewProcessor(aggregator, writer)
	if err != nil {
		fatal(err)
	}
	runner, err := service.NewRunner(consumer, processor, cfg.EventBus.FetchBatch)
	if err != nil {
		fatal(err)
	}
	runner.SetCheckpoint(state.FileStore{Path: cfg.State.CheckpointPath})
	if err := runner.Restore(context.Background()); err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
