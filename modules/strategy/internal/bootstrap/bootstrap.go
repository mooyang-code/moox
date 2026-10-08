// Package bootstrap 装配策略进程：数据库、投递、事件消费、管理接口、指标、对账与保留清理。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/replay"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	_ "github.com/mooyang-code/moox/modules/strategy/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger/eventconsumer"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	reconcileInterval = 30 * time.Second
	retentionInterval = 6 * time.Hour
)

// Initialize 打开数据库、对账未完成的启停握手、启动投递与事件消费，并在服务监听前注册 StrategyMgr。
func Initialize(ctx context.Context, s *server.Server, cfg Config) (*server.Server, func() error, error) {
	db, err := store.Open(cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	var closers []func() error
	keep := false
	defer func() {
		if keep {
			return
		}
		for i := len(closers) - 1; i >= 0; i-- {
			_ = closers[i]()
		}
		_ = db.Close()
	}()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		return nil, nil, fmt.Errorf("应用策略 schema 失败：%w", err)
	}
	if interrupted, err := db.MarkRunningReplaysInterrupted(ctx, time.Now()); err != nil {
		return nil, nil, fmt.Errorf("标记中断的回放失败：%w", err)
	} else if interrupted > 0 {
		log.Infof("进程重启：%d 个运行中的回放已标记为 failed(interrupted)", interrupted)
	}
	eventRuntime, err := newEventBusRuntime(db, cfg)
	if err != nil {
		return nil, nil, err
	}
	closers = append(closers, eventRuntime.Close)

	// 实时与回放共用 Storage/Factor 适配；回放按 replay.page_size 分页读取较长的区间。
	var inputClient, replayClient input.Client
	if cfg.DependenciesConfigured() {
		live := newInputClient(cfg)
		inputClient = live
		ranged := *live
		ranged.PageSize = uint32(cfg.Replay.PageSize)
		replayClient = &ranged
	}
	service := &rpc.Service{Store: db, Resolver: input.Service{Client: inputClient}}
	if cfg.Trade.Configured() {
		service.Owner = tradeowner.New(cfg.Trade)
	}
	runner := &replay.Runner{Store: db, Client: replayClient, ChunkBars: cfg.Replay.ChunkBars, MissingPriceLiquidateBars: cfg.Replay.MissingPriceLiquidateBars, Logf: log.Infof}
	service.Replays = runner

	// 先完成崩溃或网络中断留下的启停握手，再开始消费事件。
	if err := service.ReconcileDisabledInstances(ctx); err != nil {
		return nil, nil, fmt.Errorf("对账已停用实例失败：%w", err)
	}
	if err := requireExecutionDependencies(ctx, db, cfg); err != nil {
		return nil, nil, err
	}
	if err := service.ReconcileEnabledInstances(ctx); err != nil {
		return nil, nil, fmt.Errorf("对账启用实例失败：%w", err)
	}
	if err := eventRuntime.Start(ctx); err != nil {
		return nil, nil, err
	}
	observer, err := newInstanceObserver(db, prometheus.DefaultRegisterer, log.Warnf)
	if err != nil {
		return nil, nil, err
	}
	if err := registerMetricsReporter(s, observer); err != nil {
		return nil, nil, err
	}

	var consumer *eventconsumer.Consumer
	if inputClient != nil {
		handler := &trigger.Handler{Store: db, Loader: input.Loader{Client: inputClient}, AttemptBudget: cfg.Evaluation.AttemptBudget, Observer: observer, Logf: log.Infof}
		eventClient, err := connectEventBus(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		closers = append(closers, eventClient.Close)
		consumer = eventconsumer.New(eventconsumer.ConsumerConfig{Client: eventClient, ConsumerName: cfg.EventBus.ConsumerName}, handler)
		if err := consumer.Start(ctx); err != nil {
			return nil, nil, err
		}
		closers = append(closers, consumer.Close)
	}

	background, cancel := context.WithCancel(context.Background())
	closers = append(closers, func() error { cancel(); return nil })
	go runner.Run(background)
	go reconcileLoop(background, service)
	go retentionLoop(background, db, cfg.Retention)

	strategypb.RegisterStrategyMgrService(s, service)
	healthState := health.New("strategy", "strategy", "", "")
	healthState.SnapshotFunc = strategyHealthSnapshot(db, eventRuntime, healthState, consumer)
	healthState.SetReady(true)
	if err := health.Register(s.Service("trpc.moox.strategy.Health"), healthState); err != nil {
		return nil, nil, fmt.Errorf("注册策略健康检查失败：%w", err)
	}
	keep = true
	closeFn := func() error {
		var closeErr error
		for i := len(closers) - 1; i >= 0; i-- {
			closeErr = errors.Join(closeErr, closers[i]())
		}
		return errors.Join(closeErr, db.Close())
	}
	return s, closeFn, nil
}

// requireExecutionDependencies 防止重启后启用实例与其 Trade 授权继续存在、却没有可用的求值路径。
func requireExecutionDependencies(ctx context.Context, repo *store.Store, cfg Config) error {
	enabled := true
	instances, err := repo.ListInstances(ctx, "", &enabled)
	if err != nil {
		return fmt.Errorf("检查启用实例失败：%w", err)
	}
	for _, instance := range instances {
		if !cfg.DependenciesConfigured() {
			return fmt.Errorf("存在启用实例 %s，但未配置 Factor 与 Storage 依赖", instance.InstanceID)
		}
		if instance.LogicalAccountID != nil && !cfg.Trade.Configured() {
			return fmt.Errorf("启用实例 %s 绑定了组合账户，但未接线 Trade", instance.InstanceID)
		}
	}
	return nil
}

func reconcileLoop(ctx context.Context, service *rpc.Service) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := service.ReconcileDisabledInstances(ctx); err != nil {
			log.Warnf("已停用实例的 Trade 释放仍未完成：%v", err)
		}
	}
}

func retentionLoop(ctx context.Context, repo *store.Store, cfg RetentionConfig) {
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		now := time.Now().UTC()
		if deleted, err := repo.DeleteResultItemsBefore(ctx, now.AddDate(0, 0, -cfg.ResultItemsDays)); err != nil {
			log.Warnf("清理过期解释明细失败：%v", err)
		} else if deleted > 0 {
			log.Infof("已清理 %d 条过期解释明细", deleted)
		}
		if deleted, err := repo.DeleteReplaysBefore(ctx, now.AddDate(0, 0, -cfg.ReplaysDays)); err != nil {
			log.Warnf("清理过期回放失败：%v", err)
		} else if deleted > 0 {
			log.Infof("已清理 %d 个过期回放", deleted)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
		RelayInterval: cfg.EventBus.RelayInterval, ReconnectInterval: cfg.EventBus.ReconnectInterval, BatchSize: cfg.EventBus.RelayBatchSize,
	})
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

// newInputClient 构造 Storage（Metadata、DataView）与 Factor 的窄适配。
func newInputClient(cfg Config) *input.RPCClient {
	credentials := gatewayauth.CredentialsFromEnv()
	storageTarget, storageNode := storageGatewayEndpoint(cfg)
	storageOptions := appendTimeout(gatewayauth.NewTRPCClientOptions(storageTarget, storageNode, credentials), cfg.Storage.Timeout)
	factorTarget := gatewayauth.ServiceGatewayTarget(cfg.Factor.Target)
	factorNode := cfg.Factor.TargetNode
	if envNode := gatewayauth.ServiceGatewayNodeID(); envNode != "" {
		factorNode = envNode
	}
	factorOptions := appendTimeout(gatewayauth.NewTRPCClientOptions(factorTarget, factorNode, credentials), cfg.Factor.Timeout)
	return &input.RPCClient{
		Metadata: storagepb.NewMetadataClientProxy(storageOptions...),
		DataView: storagepb.NewDataViewClientProxy(storageOptions...),
		Factor:   factorpb.NewFactorMgrClientProxy(factorOptions...),
		Auth:     &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.AppKey, Operator: "strategy"},
		ViewAuth: &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.ViewAppKey, Operator: "strategy"},
	}
}

// storageGatewayEndpoint 优先使用 Storage 节点的本机网关：控制面注入的 MOOX_SERVICE_GATEWAY_TARGET 指向本机，
// 不能把 Metadata/DataView 钉到控制面一侧的旧副本上。
func storageGatewayEndpoint(cfg Config) (string, string) {
	target := strings.TrimSpace(cfg.Storage.Target)
	node := strings.TrimSpace(cfg.Storage.TargetNode)
	if value := strings.TrimSpace(os.Getenv("MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET")); value != "" {
		target = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID")); value != "" {
		node = value
	}
	return target, node
}

func appendTimeout(options []client.Option, timeout time.Duration) []client.Option {
	if timeout > 0 {
		return append(options, client.WithTimeout(timeout))
	}
	return options
}

func strategyHealthSnapshot(db *store.Store, eventRuntime *strategyoutbox.Runtime, state *health.State, consumer *eventconsumer.Consumer) func(context.Context) healthz.Response {
	return func(ctx context.Context) healthz.Response {
		databaseReady := db != nil && db.Ping(ctx) == nil
		eventBusConnected := eventRuntime != nil && eventRuntime.Connected()
		consumerReady := consumer == nil || consumer.Ready()
		stats, statsErr := db.PendingOutboxStats(ctx)
		oldestAge := 0.0
		if !stats.OldestPending.IsZero() {
			oldestAge = max(0, time.Since(stats.OldestPending).Seconds())
		}
		ready := databaseReady && state.Ready() && eventBusConnected && consumerReady && statsErr == nil
		rsp := healthz.Base("strategy", "strategy", "", "", state.StartedAt, ready)
		rsp.Details = map[string]any{
			"database_ready": databaseReady, "eventbus_connected": eventBusConnected,
			"ready_consumer_connected": consumerReady, "outbox_pending_count": stats.PendingCount,
			"oldest_outbox_age_seconds": oldestAge,
		}
		return rsp
	}
}
