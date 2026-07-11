package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/consumer"
	"github.com/mooyang-code/moox/modules/archive/internal/cosstore"
	"github.com/mooyang-code/moox/modules/archive/internal/health"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/registry"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

type App struct {
	Config    *config.Config
	State     *health.State
	Version   string
	GitCommit string
}

func (a *App) Run(ctx context.Context) error {
	if a == nil || a.Config == nil {
		return fmt.Errorf("archive config is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	state := a.State
	if state == nil {
		state = health.New("archive", "archive", a.Version, a.GitCommit)
	}
	a.State = state
	state.CosEnabled = a.Config.Archive.COS.Enabled
	healthCtx, healthCancel := context.WithCancel(ctx)
	defer healthCancel()
	healthServer, err := health.Start(healthCtx, a.Config.Health.Addr, state)
	if err != nil {
		return err
	}
	_ = healthServer
	store, err := journal.Open(a.Config.Archive.StateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	state.JournalReady.Store(true)
	w := writer.New(store, a.Config.Archive.RootDir, a.Config.Archive.Materialize.RowGroupRows)
	w.SetWorkers(a.Config.Archive.Materialize.Workers)
	metadataRegistry := registry.NewClient(a.Config.Archive.StorageRPC.MetadataTarget)
	w.SetRegistry(registry.PartitionRegistry{Client: metadataRegistry, DeviceID: a.Config.Archive.DeviceID})
	if err := w.Recover(ctx); err != nil {
		return err
	}
	jsCfg := jetstream.ConfigFromEnv(a.Config.Archive.EventBus.URLs, "moox-archive")
	natsClient, err := jetstream.Connect(ctx, jsCfg)
	if err != nil {
		return err
	}
	defer natsClient.Close()
	state.NATSReady.Store(true)
	ref := jetstream.ConsumerRef{Stream: a.Config.Archive.EventBus.Stream, Durable: a.Config.Archive.EventBus.Durable, FilterSubject: a.Config.Archive.EventBus.Subject, AckWait: a.Config.Archive.EventBus.AckWait, MaxDeliver: -1, MaxAckPending: a.Config.Archive.EventBus.MaxAckPending, FetchMaxWait: a.Config.Archive.EventBus.FetchMaxWait, DeliverPolicy: nats.DeliverAllPolicy, DeliverDecodeErrors: true}
	pull, err := natsClient.BindPullConsumer(ctx, ref)
	if err != nil {
		return err
	}
	defer pull.Close()
	decoder := consumer.NewDecoder(sourceLists(a.Config))
	handler := consumer.NewHandler(decoder, store, nil)
	runner := consumer.NewRunner(pull, handler, a.Config.Archive.EventBus.FetchBatch)
	runnerErr := make(chan error, 1)
	go func() { runnerErr <- runner.Run(ctx) }()
	state.ReadyFlag.Store(true)
	schedulerErr := make(chan error, 1)
	go func() {
		schedulerErr <- (writer.Scheduler{Writer: w, Interval: a.Config.Archive.Materialize.Interval, PendingRows: a.Config.Archive.Materialize.PendingRows, ShutdownTimeout: a.Config.Archive.Materialize.ShutdownTimeout, DedupeRetention: a.Config.Archive.EventBus.DedupeRetention}).Run(ctx)
	}()
	cosErr := make(chan error, 1)
	if a.Config.Archive.COS.Enabled {
		cosClient, err := cosstore.New(a.Config.Archive.COS.Region, a.Config.Archive.COS.Bucket, a.Config.Archive.RootDir, a.Config.Archive.COS.Prefix, "", "")
		if err != nil {
			return err
		}
		go func() {
			ticker := time.NewTicker(a.Config.Archive.COS.SyncInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := (cosstore.Syncer{Client: cosClient, Root: a.Config.Archive.RootDir, Prefix: a.Config.Archive.COS.Prefix, Workers: a.Config.Archive.COS.Workers, SyncOpenPartitions: a.Config.Archive.COS.SyncOpenPartitions}).Sync(ctx); err != nil {
						select {
						case cosErr <- err:
						default:
						}
					}
				}
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			state.ReadyFlag.Store(false)
			return nil
		case err := <-runnerErr:
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		case err := <-schedulerErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			if status, err := store.Status(ctx); err == nil {
				state.PendingRows.Store(status.PendingRows)
				state.DirtyPartitions.Store(status.DirtyPartitions)
			}
		case err := <-cosErr:
			if err != nil {
				return err
			}
		}
	}
}
func sourceLists(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	for space, source := range cfg.Archive.Sources {
		out[space] = append([]string(nil), source.Datasets...)
	}
	return out
}
func RunFromConfig(ctx context.Context, path, version, commit string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	return (&App{Config: cfg, Version: version, GitCommit: commit}).Run(ctx)
}

func mainContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)
	return ctx
}
