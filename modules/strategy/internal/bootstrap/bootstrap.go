package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/factorio"
	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	_ "github.com/mooyang-code/moox/modules/strategy/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/strategy/internal/storageio"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategytrigger "github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	strategyeventconsumer "github.com/mooyang-code/moox/modules/strategy/internal/trigger/eventconsumer"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize opens the database and gateway client, starts the ready consumer,
// and registers StrategyMgr before the tRPC server starts listening.
func Initialize(ctx context.Context, s *server.Server, cfg Config) (*server.Server, func() error, error) {
	db, err := store.Open(cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	var gateway *gatewayclient.Client
	var eventRuntime *strategyoutbox.Runtime
	var readyConsumer *strategyeventconsumer.Consumer
	var readyClient *jetstream.Client
	var readyProcessor *strategytrigger.Processor
	var scheduler *strategytrigger.Scheduler
	var cancelReconcile context.CancelFunc
	var reconcileDone chan struct{}
	var readyErr error
	var closeOnce sync.Once
	var closeErr error
	closeFn := func() error {
		closeOnce.Do(func() {
			if cancelReconcile != nil {
				cancelReconcile()
				<-reconcileDone
			}
			if scheduler != nil {
				scheduler.Stop()
			}
			if readyConsumer != nil {
				closeErr = errors.Join(closeErr, readyConsumer.Close())
			}
			if readyClient != nil {
				closeErr = errors.Join(closeErr, readyClient.Close())
			}
			if eventRuntime != nil {
				closeErr = errors.Join(closeErr, eventRuntime.Close())
			}
			if gateway != nil {
				closeErr = errors.Join(closeErr, gateway.Close())
			}
			closeErr = errors.Join(closeErr, db.Close())
		})
		return closeErr
	}
	keepResources := false
	defer func() {
		if !keepResources {
			_ = closeFn()
		}
	}()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		return nil, nil, fmt.Errorf("apply strategy schema: %w", err)
	}
	repo := db
	gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(ctx, "strategy gateway directory refresh: %v", err) })
	if err != nil {
		return nil, nil, fmt.Errorf("open strategy gateway client: %w", err)
	}
	eventRuntime, err = newEventBusRuntime(db, cfg)
	if err != nil {
		return nil, nil, err
	}
	service := newRPCService(repo, cfg, gateway)
	// Reconcile incomplete modern disable/enable handshakes before subscribing
	// to Factor-ready events. Historical V1 owner rows are audit data and are
	// never implicitly released or rebound during startup.
	if err := service.ReconcileDisabledInstances(ctx); err != nil {
		return nil, nil, fmt.Errorf("reconcile disabled Strategy instances: %w", err)
	}
	if err := service.ReconcileEnabledInstances(ctx); err != nil {
		return nil, nil, fmt.Errorf("reconcile enabled Strategy instances: %w", err)
	}
	if err := eventRuntime.Start(ctx); err != nil {
		return nil, nil, err
	}
	moduleMetrics, err := registerMetricsReporter(s)
	if err != nil {
		return nil, nil, err
	}
	readyConsumer, readyClient, readyProcessor, readyErr = newReadyConsumer(ctx, repo, cfg, gateway, moduleMetrics)
	if readyErr != nil {
		return nil, nil, readyErr
	}
	if readyProcessor != nil {
		scheduler = &strategytrigger.Scheduler{OnError: func(err error) { log.Warnf("strategy scheduled trigger failed: %v", err) }}
		if err := startScheduleTrigger(ctx, repo, readyProcessor, scheduler); err != nil {
			return nil, nil, fmt.Errorf("start strategy scheduler: %w", err)
		}
	}
	// Disable is committed locally before the Trade release RPC. Retry modern
	// session/owner release on startup and periodically so a crash or temporary
	// network outage cannot leave an ACTIVE owner behind indefinitely.
	reconcileCtx, cancel := context.WithCancel(ctx)
	cancelReconcile = cancel
	reconcileDone = make(chan struct{})
	go func() {
		defer close(reconcileDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			if err := service.ReconcileDisabledInstances(reconcileCtx); err != nil {
				log.Warnf("strategy disabled instance reconciliation pending: %v", err)
			}
			select {
			case <-reconcileCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	strategypb.RegisterStrategyMgrService(s, service)
	healthState := health.New("strategy", "strategy", "", "")
	healthState.SnapshotFunc = strategyHealthSnapshot(db, eventRuntime, healthState, readyConsumer)
	healthState.SetReady(true)
	if err := health.Register(s.Service("trpc.moox.strategy.Health"), healthState); err != nil {
		return nil, nil, fmt.Errorf("register strategy health service: %w", err)
	}
	keepResources = true
	return s, closeFn, nil
}

func newEventBusRuntime(repo *store.Store, cfg Config) (*strategyoutbox.Runtime, error) {
	connector := func(ctx context.Context) (strategyoutbox.JetStreamClient, error) {
		jsConfig := jetstream.ConfigFromEnv(cfg.EventBus.URLs, "moox-strategy")
		if jsConfig.Credentials == "" && jsConfig.Username == "" && strings.TrimSpace(cfg.EventBus.CredentialFile) != "" {
			if err := jsConfig.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.EventBus.CredentialFile)); err != nil {
				return nil, err
			}
		}
		jsConfig.ConnectTimeout = cfg.EventBus.ConnectTimeout
		client, err := jetstream.Connect(ctx, jsConfig)
		if err != nil {
			return nil, err
		}
		return strategyoutbox.NewManagedClient(client)
	}
	return strategyoutbox.NewRuntime(strategyoutbox.RuntimeConfig{
		Connector: connector, Store: repo, InstanceID: cfg.InstanceID,
		Probe: func(ctx context.Context, client strategyoutbox.JetStreamClient) error {
			return strategyoutbox.ValidateJetStreamPublisher(ctx, client, cfg.InstanceID)
		},
		RelayInterval: cfg.EventBus.RelayInterval, ReconnectInterval: cfg.EventBus.ReconnectInterval,
		BatchSize: cfg.EventBus.RelayBatchSize,
	})
}

func newReadyConsumer(ctx context.Context, repo *store.Store, cfg Config, gateway gatewayclient.Invoker, moduleMetrics *report.ModuleMetrics) (*strategyeventconsumer.Consumer, *jetstream.Client, *strategytrigger.Processor, error) {
	client, err := connectEventBus(ctx, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	reader := newStorageReader(cfg, gateway)
	compilerFactory := newCompilerFactory(cfg, gateway)
	logicalOwner := newLogicalAccountOwnerClient(cfg.Trade, gateway)
	poolRegistry := defaultPoolRegistry()
	processor := &strategytrigger.Processor{
		ObserveRun: func(result string, at time.Time) {
			_ = moduleMetrics.ObserveRun("evaluate", result, "strategy-targets", at)
		},
		Inbox: repo, Store: repo, Loader: storageio.Loader{Reader: reader}, PoolRegistry: poolRegistry,
		Compile: func(compileCtx context.Context, dsl config.DSL, spaceID string) (compiler.CompiledStrategy, error) {
			selected := compilerFactory(spaceID)
			if selected == nil {
				return compiler.CompiledStrategy{}, fmt.Errorf("strategy compiler is not configured")
			}
			return selected.Compile(compileCtx, dsl, spaceID)
		},
		CompileWithBindings: func(compileCtx context.Context, dsl config.DSL, spaceID string, raw json.RawMessage) (compiler.CompiledStrategy, error) {
			selected := compilerFactory(spaceID)
			if selected == nil {
				return compiler.CompiledStrategy{}, fmt.Errorf("strategy compiler is not configured")
			}
			return selected.CompileWithBindings(compileCtx, dsl, spaceID, raw)
		},
		SessionGeneration: logicalOwner.SessionGeneration,
		VerifyDependencies: func(ctx context.Context, compiled compiler.CompiledStrategy) error {
			dependencyCompiler := compilerFactory(compiled.SpaceID)
			if dependencyCompiler == nil {
				return fmt.Errorf("strategy dependency compiler is not configured")
			}
			return dependencyCompiler.VerifyDependencies(ctx, compiled)
		},
		Diagnostic: func(err error) { log.Warnf("strategy trigger evaluation skipped: %v", err) },
	}
	consumer := strategyeventconsumer.New(strategyeventconsumer.Config{Client: client, ConsumerName: cfg.EventBus.ConsumerName}, processor)
	if err := consumer.Start(ctx); err != nil {
		_ = client.Close()
		return nil, nil, nil, err
	}
	return consumer, client, processor, nil
}

func defaultPoolRegistry() *input.UDFRegistry {
	registry := input.NewUDFRegistry()
	// The built-in pool is intentionally small and deterministic: it filters
	// the frozen subject directory only, never calling Factor/Trade or an
	// external service. User code can register additional UDFs in embedded
	// deployments before constructing a Processor.
	_ = registry.RegisterValidated("spot_symbols", validateSpotSymbolsParams, func(_ context.Context, in input.PoolUDFInput) ([]string, error) {
		quote, _ := in.Params["quote_asset"].(string)
		quote = strings.ToUpper(strings.TrimSpace(quote))
		ids := make([]string, 0, len(in.Subjects))
		seen := make(map[string]struct{}, len(in.Subjects))
		for _, subject := range in.Subjects {
			if !subject.Active || (subject.Market != "" && !strings.EqualFold(subject.Market, "spot")) {
				continue
			}
			if quote != "" && !strings.EqualFold(subject.QuoteAsset, quote) {
				continue
			}
			id := strings.TrimSpace(subject.InstrumentID)
			key := strings.ToUpper(id)
			if id == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			ids = append(ids, id)
		}
		return ids, nil
	})
	_ = registry.RegisterValidated("all_symbols", validateNoPoolParams, func(_ context.Context, in input.PoolUDFInput) ([]string, error) {
		ids := make([]string, 0, len(in.Subjects))
		seen := make(map[string]struct{}, len(in.Subjects))
		for _, subject := range in.Subjects {
			if subject.Active {
				id := strings.TrimSpace(subject.InstrumentID)
				key := strings.ToUpper(id)
				if id == "" {
					continue
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				ids = append(ids, id)
			}
		}
		return ids, nil
	})
	return registry
}

func validateSpotSymbolsParams(params map[string]any) error {
	for key, value := range params {
		if key != "quote_asset" {
			return fmt.Errorf("unsupported parameter %q", key)
		}
		quote, ok := value.(string)
		if !ok || strings.TrimSpace(quote) == "" {
			return errors.New("quote_asset must be a non-empty string")
		}
	}
	return nil
}

func validateNoPoolParams(params map[string]any) error {
	if len(params) != 0 {
		return errors.New("parameters are not supported")
	}
	return nil
}

func startScheduleTrigger(ctx context.Context, repo *store.Store, processor *strategytrigger.Processor, scheduler *strategytrigger.Scheduler) error {
	return scheduler.StartDynamic(ctx, time.Minute, func(loadCtx context.Context) ([]strategytrigger.ScheduleJob, error) {
		instances, err := repo.ListAllInstances(loadCtx, boolPtr(true))
		if err != nil {
			return nil, err
		}
		jobs := make([]strategytrigger.ScheduleJob, 0, len(instances))
		for _, instance := range instances {
			definition, err := repo.GetStrategyDefinition(loadCtx, instance.StrategyID)
			if err != nil {
				continue
			}
			dsl, err := config.Parse([]byte(definition.DSLYaml))
			if err != nil || dsl.Triggers.Schedule == nil {
				continue
			}
			viewID := sourceViewID(instance.InputBindingsJSON)
			if viewID == "" {
				// Without an explicit source View the loader cannot freeze an
				// input snapshot, so leave this schedule disabled and visible in
				// the instance diagnostics rather than guessing a View.
				continue
			}
			instanceCopy, dslCopy := instance, dsl
			jobs = append(jobs, strategytrigger.ScheduleJob{
				Cron: dslCopy.Triggers.Schedule.Cron, Timezone: dslCopy.Triggers.Schedule.Timezone,
				Run: func(runCtx context.Context, at time.Time) error {
					period, err := input.ClosedPeriod(dslCopy.Data.Calendar, dslCopy.Data.Bar, at)
					if err != nil {
						return err
					}
					return processor.Handle(runCtx, strategytrigger.PeriodReady{
						MessageID: fmt.Sprintf("schedule/%s/%d", instanceCopy.InstanceID, period.BarEnd.UnixMilli()),
						EventName: "strategy.schedule", SpaceID: instanceCopy.SpaceID, ViewID: viewID,
						Frequency: dslCopy.Data.Bar, PeriodTime: period.StorageStart, StoragePeriodTime: period.StorageStart, BarEndTime: period.BarEnd, Status: "complete",
						ReadyViewIDs: []string{viewID}, TargetInstanceID: instanceCopy.InstanceID,
					})
				},
			})
		}
		return jobs, nil
	})
}

func boolPtr(value bool) *bool { return &value }

func sourceViewID(raw json.RawMessage) string {
	var binding struct {
		SourceViewID string `json:"source_view_id"`
		ViewID       string `json:"view_id"`
	}
	if json.Unmarshal(raw, &binding) != nil {
		return ""
	}
	if strings.TrimSpace(binding.SourceViewID) != "" {
		return strings.TrimSpace(binding.SourceViewID)
	}
	return strings.TrimSpace(binding.ViewID)
}

func connectEventBus(ctx context.Context, cfg Config) (*jetstream.Client, error) {
	jsConfig := jetstream.ConfigFromEnv(cfg.EventBus.URLs, "moox-strategy-ready")
	if strings.TrimSpace(cfg.EventBus.CredentialFile) != "" {
		if err := jsConfig.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.EventBus.CredentialFile)); err != nil {
			return nil, err
		}
	}
	jsConfig.ConnectTimeout = cfg.EventBus.ConnectTimeout
	return jetstream.Connect(ctx, jsConfig)
}

func newStorageReader(cfg Config, gateway gatewayclient.Invoker) *storageio.RPCClient {
	reader := storageio.NewGatewayClient(gateway)
	reader.Auth = &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.AppKey, Operator: "strategy"}
	reader.ViewAuth = &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.ViewAppKey, Operator: "strategy"}
	return reader
}

func newRPCService(repo *store.Store, cfg Config, gateway gatewayclient.Invoker) *rpc.Service {
	compilerFactory := newCompilerFactory(cfg, gateway)
	return &rpc.Service{
		Repo: repo, Registry: &registry.Service{Repo: repo}, CompilerFactory: compilerFactory, PoolRegistry: defaultPoolRegistry(),
		LogicalAccounts: newLogicalAccountOwnerClient(cfg.Trade, gateway),
	}
}

func newCompilerFactory(cfg Config, gateway gatewayclient.Invoker) func(string) *compiler.Compiler {
	if gateway == nil {
		return nil
	}
	factorClient := factorio.NewGatewayClient(gateway)
	return func(spaceID string) *compiler.Compiler {
		storageClient := newStorageReader(cfg, gateway)
		storageClient.SpaceID = spaceID
		return &compiler.Compiler{Factors: factorClient, Storage: storageClient}
	}
}

func strategyHealthSnapshot(db *store.Store, eventRuntime *strategyoutbox.Runtime, state *health.State, consumers ...*strategyeventconsumer.Consumer) func(context.Context) healthz.Response {
	return func(ctx context.Context) healthz.Response {
		databaseReady := db != nil && db.Ping(ctx) == nil
		eventBusConnected := eventRuntime != nil && eventRuntime.Connected()
		readyConsumerReady := true
		if len(consumers) > 0 && consumers[0] != nil {
			readyConsumerReady = consumers[0].Ready()
		}
		stats, statsErr := db.PendingOutboxStats(ctx)
		oldestAge := 0.0
		if !stats.OldestPending.IsZero() {
			oldestAge = max(0, time.Since(stats.OldestPending).Seconds())
		}
		ready := databaseReady && state.Ready() && eventBusConnected && readyConsumerReady && statsErr == nil
		rsp := healthz.Base("strategy", "strategy", "", "", state.StartedAt, ready)
		rsp.Details = map[string]any{
			"database_ready":     databaseReady,
			"eventbus_connected": eventBusConnected, "ready_consumer_connected": readyConsumerReady, "outbox_pending_count": stats.PendingCount,
			"oldest_outbox_age_seconds": oldestAge,
		}
		return rsp
	}
}
