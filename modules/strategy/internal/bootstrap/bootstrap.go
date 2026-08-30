package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/factorio"
	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	"github.com/mooyang-code/moox/modules/strategy/internal/storageio"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategytrigger "github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	strategyeventconsumer "github.com/mooyang-code/moox/modules/strategy/internal/trigger/eventconsumer"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize opens the control-plane database, starts the declarative ready
// consumer when Factor/Storage dependencies are configured, and registers the
// StrategyMgr service before the tRPC server starts listening.
func Initialize(ctx context.Context, s *server.Server, cfg Config) (*server.Server, func() error, error) {
	db, err := store.Open(cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	keepResources := false
	var eventRuntime *strategyoutbox.Runtime
	var readyConsumer *strategyeventconsumer.Consumer
	var readyClient *jetstream.Client
	var cancelReconcile context.CancelFunc
	var readyErr error
	defer func() {
		if keepResources {
			return
		}
		if eventRuntime != nil {
			_ = eventRuntime.Close()
		}
		if readyConsumer != nil {
			_ = readyConsumer.Close()
		}
		if readyClient != nil {
			_ = readyClient.Close()
		}
		if cancelReconcile != nil {
			cancelReconcile()
		}
		_ = db.Close()
	}()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		return nil, nil, fmt.Errorf("apply strategy schema: %w", err)
	}
	repo := db
	eventRuntime, err = newEventBusRuntime(db, cfg)
	if err != nil {
		return nil, nil, err
	}
	service := newRPCService(repo, cfg)
	// Reconcile archived and disabled owners before subscribing to Factor-ready
	// events. This closes the upgrade window in which a new V2 target could be
	// accepted and then cleared by a late owner handoff.
	if err := service.ReconcileLegacyOwners(ctx); err != nil {
		return nil, nil, fmt.Errorf("reconcile legacy Strategy owners: %w", err)
	}
	if err := service.ReconcileDisabledOwners(ctx); err != nil {
		return nil, nil, fmt.Errorf("reconcile disabled Strategy owners: %w", err)
	}
	if err := eventRuntime.Start(ctx); err != nil {
		return nil, nil, err
	}
	readyConsumer, readyClient, readyErr = newReadyConsumer(ctx, repo, cfg)
	if readyErr != nil {
		return nil, nil, readyErr
	}
	if _, err := registerMetricsReporter(s); err != nil {
		return nil, nil, err
	}
	// Disable is committed locally before the Trade release RPC. Retry the
	// release on startup and periodically so a crash or temporary network
	// outage cannot leave an ACTIVE Trade owner behind indefinitely.
	reconcileCtx, cancel := context.WithCancel(context.Background())
	cancelReconcile = cancel
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			if err := service.ReconcileLegacyOwners(reconcileCtx); err != nil {
				log.Warnf("strategy legacy owner reconciliation pending: %v", err)
			}
			if err := service.ReconcileDisabledOwners(reconcileCtx); err != nil {
				log.Warnf("strategy disabled owner reconciliation pending: %v", err)
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
	closeFn := func() error {
		cancelReconcile()
		var eventBusErr error
		if eventRuntime != nil {
			eventBusErr = eventRuntime.Close()
		}
		if readyConsumer != nil {
			_ = readyConsumer.Close()
		}
		if readyClient != nil {
			_ = readyClient.Close()
		}
		dbErr := db.Close()
		if eventBusErr != nil {
			return eventBusErr
		}
		return dbErr
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

func newReadyConsumer(ctx context.Context, repo *store.Store, cfg Config) (*strategyeventconsumer.Consumer, *jetstream.Client, error) {
	if strings.TrimSpace(cfg.Factor.Target) == "" || strings.TrimSpace(cfg.Storage.Target) == "" {
		return nil, nil, nil
	}
	client, err := connectEventBus(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	reader := newStorageReader(cfg)
	compilerFactory := newCompilerFactory(cfg)
	logicalOwner := newLogicalAccountOwnerClient(cfg.LogicalAccountTarget, cfg.LogicalAccountTimeout)
	processor := &strategytrigger.Processor{
		Inbox: repo, Store: repo, Loader: storageio.Loader{Reader: reader},
		OwnerGeneration: logicalOwner.Generation,
		VerifyDependencies: func(ctx context.Context, compiled compiler.CompiledStrategy) error {
			dependencyCompiler := compilerFactory(compiled.SpaceID)
			if dependencyCompiler == nil {
				return fmt.Errorf("strategy dependency compiler is not configured")
			}
			return dependencyCompiler.VerifyDependencies(ctx, compiled)
		},
	}
	consumer := strategyeventconsumer.New(strategyeventconsumer.Config{Client: client, ConsumerName: cfg.EventBus.ConsumerName}, processor)
	if err := consumer.Start(ctx); err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return consumer, client, nil
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

func newStorageReader(cfg Config) *storageio.RPCClient {
	credentials := gatewayauth.CredentialsFromEnv()
	options := rpcOptions(cfg.Storage.Target, cfg.Storage.TargetNode, credentials, cfg.Storage.Timeout)
	return &storageio.RPCClient{
		Metadata: storagepb.NewMetadataClientProxy(options...),
		DataView: storagepb.NewDataViewClientProxy(options...),
		Auth:     &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.AppKey, Operator: "strategy"},
		ViewAuth: &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.ViewAppKey, Operator: "strategy"},
	}
}

func newRPCService(repo *store.Store, cfg Config) *rpc.Service {
	compilerFactory := newCompilerFactory(cfg)
	return &rpc.Service{
		Repo: repo, Registry: &registry.Service{Repo: repo}, CompilerFactory: compilerFactory,
		LogicalAccounts: newLogicalAccountOwnerClient(
			cfg.LogicalAccountTarget,
			cfg.LogicalAccountTimeout,
		),
	}
}

func newCompilerFactory(cfg Config) func(string) *compiler.Compiler {
	if strings.TrimSpace(cfg.Factor.Target) == "" || strings.TrimSpace(cfg.Storage.Target) == "" {
		return nil
	}
	return func(spaceID string) *compiler.Compiler {
		credentials := gatewayauth.CredentialsFromEnv()
		factorOptions := rpcOptions(cfg.Factor.Target, cfg.Factor.TargetNode, credentials, cfg.Factor.Timeout)
		storageOptions := rpcOptions(cfg.Storage.Target, cfg.Storage.TargetNode, credentials, cfg.Storage.Timeout)
		factorClient := &factorio.RPCClient{Proxy: factorpb.NewFactorMgrClientProxy(factorOptions...)}
		storageClient := &storageio.RPCClient{
			SpaceID:  spaceID,
			Metadata: storagepb.NewMetadataClientProxy(storageOptions...),
			DataView: storagepb.NewDataViewClientProxy(storageOptions...),
			Auth:     &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.AppKey, Operator: "strategy"},
			ViewAuth: &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.ViewAppKey, Operator: "strategy"},
		}
		return &compiler.Compiler{Factors: factorClient, Storage: storageClient}
	}
}

func rpcOptions(target, targetNode string, credentials gatewayauth.Credentials, timeout time.Duration) []client.Option {
	target = gatewayauth.ServiceGatewayTarget(target)
	if envNode := gatewayauth.ServiceGatewayNodeID(); envNode != "" {
		targetNode = envNode
	}
	options := gatewayauth.NewTRPCClientOptions(target, targetNode, credentials)
	if timeout > 0 {
		options = append(options, client.WithTimeout(timeout))
	}
	return options
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
