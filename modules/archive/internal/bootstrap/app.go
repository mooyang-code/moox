package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/cosstore"
	eventconsumer "github.com/mooyang-code/moox/modules/archive/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/archive/internal/health"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/registry"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/server"
)

type App struct {
	Config    *config.Config
	State     *health.State
	Version   string
	GitCommit string
	Server    *server.Server
}

func (a *App) Run(ctx context.Context) error {
	if a == nil || a.Config == nil {
		return fmt.Errorf("archive config is required")
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	state := a.State
	if state == nil {
		state = health.New("archive", "archive", a.Version, a.GitCommit)
	}
	a.State = state
	state.CosEnabled = a.Config.Archive.COS.Enabled
	store, err := journal.Open(a.Config.Archive.StateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	state.JournalReady.Store(true)
	w := writer.New(store, a.Config.Archive.RootDir, a.Config.Archive.Materialize.RowGroupRows)
	w.SetWorkers(a.Config.Archive.Materialize.Workers)
	storageCredentials, err := gatewayauth.ResolveCredentials(a.Config.Archive.StorageRPC.KeyID, a.Config.Archive.StorageRPC.HMACKeyFile)
	if err != nil {
		return err
	}
	metadataRegistry := registry.NewClientWithCredentials(a.Config.Archive.StorageRPC.GatewayTarget, a.Config.Archive.StorageRPC.GatewayNodeID, storageCredentials)
	w.SetRegistry(registry.PartitionRegistry{Client: metadataRegistry, DeviceID: a.Config.Archive.DeviceID})
	if err := w.Recover(ctx); err != nil {
		return err
	}
	var moduleMetrics *report.ModuleMetrics
	if a.Server != nil {
		moduleMetrics, err = registerMetricsReporter(a.Server)
		if err != nil {
			return err
		}
	}
	materializer := writer.Scheduler{
		Writer: w, PendingRows: a.Config.Archive.Materialize.PendingRows,
		DedupeRetention: a.Config.Archive.EventBus.DedupeRetention,
		ModuleMetrics:   moduleMetrics,
	}
	var cosSyncer archiveCOSSyncer
	if a.Config.Archive.COS.Enabled {
		cosClient, err := cosstore.New(a.Config.Archive.COS.Region, a.Config.Archive.COS.Bucket, a.Config.Archive.RootDir, a.Config.Archive.COS.Prefix, "", "")
		if err != nil {
			return err
		}
		cosSyncer = &cosstore.Syncer{
			Client: cosClient, Root: a.Config.Archive.RootDir, Prefix: a.Config.Archive.COS.Prefix,
			Workers: a.Config.Archive.COS.Workers, SyncOpenPartitions: a.Config.Archive.COS.SyncOpenPartitions,
		}
	}
	if a.Server != nil {
		if err := registerArchiveTimers(a.Server, materializer, cosSyncer); err != nil {
			return err
		}
	}
	jsCfg, err := eventBusConfig(a.Config)
	if err != nil {
		return err
	}
	natsClient, err := jetstream.Connect(ctx, jsCfg)
	if err != nil {
		return err
	}
	defer natsClient.Close()
	state.NATSReady.Store(true)
	eventRegistry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	pull, err := events.NewConsumer(ctx, natsClient, eventRegistry, events.ConsumerConfig{
		Name: a.Config.Archive.EventBus.Consumer, Event: events.DatasetRowsUpserted,
		AckWait: 5 * time.Minute, MaxDeliver: -1, MaxAckPending: 256,
		FetchMaxWait: a.Config.Archive.EventBus.FetchMaxWait, DeliverPolicy: nats.DeliverAllPolicy,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		return err
	}
	defer pull.Close()
	decoder := eventconsumer.NewDecoder(sourceLists(a.Config))
	handler := eventconsumer.NewHandler(decoder, store, nil)
	runner := eventconsumer.NewRunner(pull, handler, a.Config.Archive.EventBus.FetchBatch)
	runnerErr := make(chan error, 1)
	go func() { runnerErr <- runner.Run(ctx) }()
	var serveErr <-chan error
	if a.Server != nil {
		ch := make(chan error, 1)
		serveErr = ch
		go func() { ch <- a.Server.Serve() }()
	}
	state.ReadyFlag.Store(true)
	runnerDone := false
	var firstErr error
	select {
	case <-ctx.Done():
	case err := <-runnerErr:
		runnerDone = true
		if err != nil && !errors.Is(err, context.Canceled) {
			firstErr = err
		}
	case err := <-serveErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			firstErr = err
		}
	}
	state.ReadyFlag.Store(false)
	cancel()
	if a.Server != nil {
		_ = a.Server.Close(nil)
	}
	wait := a.Config.Archive.Materialize.ShutdownTimeout
	if wait <= 0 {
		wait = 2 * time.Minute
	}
	var drainErr error
	if !runnerDone {
		timer := time.NewTimer(wait + time.Second)
		select {
		case err := <-runnerErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				drainErr = err
			}
		case <-timer.C:
			drainErr = fmt.Errorf("archive shutdown drain exceeded %s", wait+time.Second)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	flushCtx, flushCancel := context.WithTimeout(trpc.CloneContext(ctx), wait)
	flushErr := materializer.FlushOnShutdown(flushCtx)
	flushCancel()
	statusCtx, statusCancel := context.WithTimeout(trpc.BackgroundContext(), time.Second)
	defer statusCancel()
	if status, err := store.Status(statusCtx); err == nil {
		state.PendingRows.Store(status.PendingRows)
		state.DirtyPartitions.Store(status.DirtyPartitions)
	}
	return errors.Join(firstErr, drainErr, flushErr)
}

func eventBusConfig(cfg *config.Config) (jetstream.Config, error) {
	if cfg == nil {
		return jetstream.Config{}, errors.New("archive config is required")
	}
	jsCfg := jetstream.ConfigFromEnv(cfg.Archive.EventBus.URLs, "moox-archive")
	if path := strings.TrimSpace(cfg.Archive.EventBus.CredentialFile); path != "" {
		if err := jsCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(path)); err != nil {
			return jetstream.Config{}, fmt.Errorf("archive eventbus credential: %w", err)
		}
	}
	return jsCfg, nil
}

// RegisterHealth registers the monitor-facing endpoints on the tRPC server.
func (a *App) RegisterHealth(s *server.Server) error {
	if a == nil || a.Config == nil {
		return fmt.Errorf("archive config is required")
	}
	if a.State == nil {
		a.State = health.New("archive", "archive", a.Version, a.GitCommit)
	}
	return health.Register(s.Service("trpc.moox.archive.Health"), a.State)
}
func sourceLists(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	for space, source := range cfg.Archive.Sources {
		out[space] = append([]string(nil), source.Datasets...)
	}
	return out
}
func RunFromConfig(ctx context.Context, path, version, commit string) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	s := trpc.NewServer()
	trpclog.InstallServiceName("archive")
	app := &App{Config: cfg, Version: version, GitCommit: commit, Server: s}
	if err := app.RegisterHealth(s); err != nil {
		return err
	}
	return app.Run(ctx)
}
