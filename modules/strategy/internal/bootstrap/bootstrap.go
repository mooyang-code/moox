// Package bootstrap 装配策略进程：数据库、投递、事件消费、管理接口、指标、对账与保留清理。
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/replay"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger/eventconsumer"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	reconcileInterval = 30 * time.Second
	retentionInterval = 6 * time.Hour
	// startupReconcileTimeout 限制启动时对账联系 Trade 的总时长：Trade 不可达时不让启动按实例数线性变慢，
	// 剩下的由对账循环继续。
	startupReconcileTimeout = 20 * time.Second
	// outboxStallThreshold 是待投递结果最长允许的等待：投递正常时 1 秒内发出，超过它说明投递已停滞，健康检查报未就绪。
	outboxStallThreshold = 5 * time.Minute
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
	// 访问 Storage、FactorMgr 与 Trade 统一经本机主机网关（strategy 身份）；网关客户端由本进程持有，各适配器只借用。
	gateway, err := cfg.OpenGateway(func(err error) { log.Warnf("strategy 网关目录刷新失败：%v", err) })
	if err != nil {
		return nil, nil, fmt.Errorf("打开 strategy 的网关客户端：%w", err)
	}
	closers = append(closers, gateway.Close)
	eventRuntime, err := newEventBusRuntime(db, cfg)
	if err != nil {
		return nil, nil, err
	}
	closers = append(closers, eventRuntime.Close)

	// 实时与回放共用 Storage/Factor 适配；回放按 replay.page_size 分页读取较长的区间。
	live := newInputClient(cfg, gateway)
	var inputClient input.Client = live
	ranged := *live
	ranged.PageSize = uint32(cfg.Replay.PageSize)
	var replayClient input.Client = &ranged
	service := &rpc.Service{Store: db, Resolver: input.Service{Client: inputClient}}
	service.Owner = tradeowner.New(cfg.Trade, gateway)
	runner := &replay.Runner{Store: db, Client: replayClient, ChunkBars: cfg.Replay.ChunkBars, MissingPriceLiquidateBars: cfg.Replay.MissingPriceLiquidateBars, Logf: log.Infof}
	service.Replays = runner

	observer, err := newInstanceObserver(db, prometheus.DefaultRegisterer, log.Warnf)
	if err != nil {
		return nil, nil, err
	}
	service.Alerts = observer

	// 先尝试完成崩溃或网络中断留下的启停握手，再开始消费事件。Trade 暂不可达或个别实例不一致只告警：
	// 停用侧由对账循环继续重试；启用侧被 Trade 拒绝的实例自动停用，结果未知的标记 session_unverified，
	// 进程与接口照常启动以便人工修复。启动对账限时，超时未完成的实例由对账循环继续。
	reconcileCtx, cancelReconcile := context.WithTimeout(ctx, startupReconcileTimeout)
	if err := service.ReconcileDisabledInstances(reconcileCtx); err != nil {
		log.Warnf("启动对账：已停用实例的 Trade 释放未完成，稍后自动重试：%v%s", err, tradeDetail(err))
	}
	if err := service.ReconcileEnabledInstances(reconcileCtx); err != nil {
		log.Warnf("启动对账：%v%s", err, tradeDetail(err))
	}
	cancelReconcile()
	if err := eventRuntime.Start(ctx); err != nil {
		return nil, nil, err
	}
	if err := registerMetricsReporter(s, observer); err != nil {
		return nil, nil, err
	}

	var consumer *eventconsumer.Consumer
	if inputClient != nil {
		handler := &trigger.Handler{Store: db, Loader: input.Loader{Client: inputClient}, AttemptBudget: cfg.Evaluation.AttemptBudget, Observer: observer, Logf: log.Infof}
		// EventBus 暂不可用时消费者在后台重试连接，进程照常启动（未就绪），与出站投递的容错一致。
		connect := func(ctx context.Context) (*jetstream.Client, error) {
			jsConfig, err := eventBusConfig(cfg, "moox-strategy-ready")
			if err != nil {
				return nil, err
			}
			return jetstream.Connect(ctx, jsConfig)
		}
		consumer = eventconsumer.New(eventconsumer.ConsumerConfig{Connect: connect, ConsumerName: cfg.EventBus.ConsumerName, Logf: log.Warnf}, handler)
		if err := consumer.Start(ctx); err != nil {
			return nil, nil, err
		}
		closers = append(closers, consumer.Close)
	}

	// 关闭顺序：先停后台循环并等它们退出，再关消费者（等在途投递）、出站投递，最后关数据库。
	background, cancel := context.WithCancel(context.Background())
	var loops sync.WaitGroup
	closers = append(closers, func() error {
		cancel()
		loops.Wait()
		return nil
	})
	loops.Add(3)
	go func() {
		defer loops.Done()
		runner.Run(background)
	}()
	go func() {
		defer loops.Done()
		reconcileLoop(background, service)
	}()
	go func() {
		defer loops.Done()
		retentionLoop(background, db, cfg.Retention)
	}()

	// 只注册到 StrategyMgr 自己的监听；注册到整个 server 会让健康检查端口也能调用管理方法。
	strategyMgr := s.Service("trpc.moox.strategy.StrategyMgr")
	if strategyMgr == nil {
		return nil, nil, errors.New("缺少 tRPC 服务 trpc.moox.strategy.StrategyMgr")
	}
	strategypb.RegisterStrategyMgrService(strategyMgr, service)
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
			log.Warnf("已停用实例的 Trade 释放仍未完成：%v%s", err, tradeDetail(err))
		}
		if err := service.ReconcileEnabledInstances(ctx); err != nil {
			log.Warnf("启用实例的 Trade 会话复核：%v%s", err, tradeDetail(err))
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
		if deleted, err := repo.DeleteReplaysBeyond(ctx, cfg.ReplaysMax); err != nil {
			log.Warnf("清理超出个数的回放失败：%v", err)
		} else if deleted > 0 {
			log.Infof("已清理 %d 个超出保留个数的回放", deleted)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// calendarWarningWindow 是 A 股内嵌日历到期前的提醒窗口。
const calendarWarningWindow = 30 * 24 * time.Hour

// stockCalendarWarning 在有启用实例使用 A 股日历时检查内嵌日历：将到期时给出提醒，已过期时报未就绪
// （之后的周期无法换算，会被丢弃）。
func stockCalendarWarning(ctx context.Context, db *store.Store, now time.Time) (string, bool) {
	enabled := true
	instances, err := db.ListInstances(ctx, "", &enabled)
	if err != nil {
		return "", false
	}
	uses := false
	for _, instance := range instances {
		var resolved struct {
			Calendar string `json:"calendar"`
		}
		if json.Unmarshal(instance.ResolvedJSON, &resolved) == nil && strings.EqualFold(resolved.Calendar, "cn_stock") {
			uses = true
			break
		}
	}
	if !uses {
		return "", false
	}
	err = input.StockCalendarReadiness(now, calendarWarningWindow, trigger.ValidBars)
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, input.ErrCalendarExpiring):
		return err.Error(), false
	default:
		return err.Error() + "；使用 A 股日历的实例无法求值", true
	}
}

// tradeDetail 把 Trade 业务错误附带的原始说明拼到日志后面。
func tradeDetail(err error) string {
	if detail := tradeowner.Detail(err); detail != "" {
		return "；Trade 的说明：" + detail
	}
	return ""
}

// eventBusConfig 是出站投递与事件消费共用的连接配置：环境变量给出的凭据优先，否则用配置的凭据文件。
func eventBusConfig(cfg Config, name string) (jetstream.Config, error) {
	jsConfig := jetstream.ConfigFromEnv(cfg.EventBus.URLs, name)
	if jsConfig.Credentials == "" && jsConfig.Username == "" && strings.TrimSpace(cfg.EventBus.CredentialFile) != "" {
		if err := jsConfig.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.EventBus.CredentialFile)); err != nil {
			return jetstream.Config{}, err
		}
	}
	jsConfig.ConnectTimeout = cfg.EventBus.ConnectTimeout
	return jsConfig, nil
}

func newEventBusRuntime(repo *store.Store, cfg Config) (*strategyoutbox.Runtime, error) {
	connector := func(ctx context.Context) (strategyoutbox.JetStreamClient, error) {
		jsConfig, err := eventBusConfig(cfg, "moox-strategy")
		if err != nil {
			return nil, err
		}
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
		PublishTimeout: cfg.EventBus.PublishTimeout, Logf: log.Warnf,
	})
}

// newInputClient 构造 Storage（Metadata、DataView）与 Factor 的窄适配，调用经 gatewayclient 发往本机主机网关。
func newInputClient(cfg Config, gateway gatewayclient.Invoker) *input.RPCClient {
	client := input.NewGatewayClient(gateway, cfg.Storage.Timeout, cfg.Factor.Timeout)
	client.Auth = &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.AppKey, Operator: "strategy"}
	client.ViewAuth = &commonpb.AuthInfo{AppId: cfg.Storage.AppID, AppKey: cfg.Storage.ViewAppKey, Operator: "strategy"}
	return client
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
		// 待投递结果在投递正常时 1 秒内发出：最老一条等待超过阈值，或发布持续失败超过阈值（新结果会替代、过期取消旧的
		// 待投递结果，短 bar 的实例最老一条的年龄不会超过一根 bar），都说明投递停滞，报未就绪以触发告警。
		publishFailing := eventRuntime.PublishFailingFor(time.Now())
		outboxStalled := oldestAge > outboxStallThreshold.Seconds() || publishFailing > outboxStallThreshold
		calendarWarning, calendarExpired := stockCalendarWarning(ctx, db, time.Now())
		ready := databaseReady && state.Ready() && eventBusConnected && consumerReady && statsErr == nil && !outboxStalled && !calendarExpired
		rsp := healthz.Base("strategy", "strategy", "", "", state.StartedAt, ready)
		rsp.Details = map[string]any{
			"database_ready": databaseReady, "eventbus_connected": eventBusConnected,
			"ready_consumer_connected": consumerReady, "outbox_pending_count": stats.PendingCount,
			"oldest_outbox_age_seconds": oldestAge, "outbox_stalled": outboxStalled,
			"outbox_publish_failing_seconds": publishFailing.Seconds(),
		}
		if calendarWarning != "" {
			rsp.Details["calendar_warning"] = calendarWarning
		}
		return rsp
	}
}
