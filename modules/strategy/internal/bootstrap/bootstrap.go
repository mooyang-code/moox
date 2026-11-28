// Package bootstrap 装配策略进程：数据库、投递、事件消费、管理接口、指标与保留清理。
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/paramsubmit"
	"github.com/mooyang-code/moox/modules/strategy/internal/replay"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	"github.com/mooyang-code/moox/modules/strategy/internal/seeding"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/modules/strategy/internal/warmup"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"

	"github.com/mooyang-code/moox/modules/strategy/internal/period"
	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

const (
	retentionInterval = 6 * time.Hour
	// outboxStallThreshold 是待投递结果最长允许的等待：投递正常时 1 秒内发出，超过它说明投递已停滞，健康检查报未就绪。
	outboxStallThreshold = 5 * time.Minute
)

// Initialize 打开数据库、启动投递与事件消费，并在服务监听前注册 StrategyMgr。
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
	// 访问 Storage、FactorMgr 与 Trade 统一经本机主机网关（strategy 身份）；网关客户端由本进程持有，各适配器只借用。
	gateway, err := cfg.OpenGateway(func(err error) { log.Warnf("strategy 网关目录刷新失败：%v", err) })
	if err != nil {
		return nil, nil, fmt.Errorf("打开 strategy 的网关客户端：%w", err)
	}
	closers = append(closers, gateway.Close)
	// “因子现算”来源经同一个网关客户端调因子引擎的 EngineControl：不套 factor.timeout，超时按服务目录的路由超时。
	engineClient := upstream.NewEngineClient(gateway)
	// 先关掉上次运行中途退出的“因子现算”回放留在因子引擎上的会话，再把遗留的运行中回放标成 interrupted。
	if err := replay.CloseInterruptedSessions(ctx, db, engineClient, log.Warnf); err != nil {
		return nil, nil, fmt.Errorf("读取运行中的回放失败：%w", err)
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
	live := newInputClient(cfg, gateway)
	var inputClient upstream.Client = live
	ranged := *live
	ranged.PageSize = uint32(cfg.Replay.PageSize)
	var replayClient upstream.Client = &ranged
	service := &rpc.Service{Store: db, Resolver: input.Service{Client: inputClient, Sets: live, ReadyWait: factorTaskReadyWait, ReadyStep: factorTaskReadyStep}}
	// 启用绑了组合账户的实例前读一次 Trade 的账户；停用后通知 Trade 关闭会话（批 3 的 3.6）。
	service.Trade = tradeowner.New(cfg.Trade, gateway)
	// 删除实例：经 Factor 删除关联的任务。
	service.Tasks = live
	// 策略池启用后在后台预热：用回放的历史读取从启用时刻往前推进到最新已收盘的一期。
	warm := &warmup.Manager{Store: db, Client: replayClient, ChunkBars: cfg.Replay.ChunkBars, Logf: log.Infof}
	closers = append(closers, func() error {
		warm.Close()
		return nil
	})
	service.Warmer = warm
	runner := &replay.Runner{Store: db, Client: replayClient, Engine: engineClient, ChunkBars: cfg.Replay.ChunkBars, MissingPriceLiquidateBars: cfg.Replay.MissingPriceLiquidateBars, Logf: log.Infof}
	service.Replays = runner

	observer, err := newInstanceObserver(db, prometheus.DefaultRegisterer, log.Warnf)
	if err != nil {
		return nil, nil, err
	}
	observer.SetStaleAlertPeriods(cfg.Params.StaleAlertPeriods)
	// 首次历史补齐：每分钟向 Collector 取一次补齐中的数据集，用于把补齐中的实例在监控期望里置“暂停（历史补齐中）”。
	seedingTracker := seeding.New(seeding.GatewayLister{Gateway: gateway, Timeout: seedingListTimeout}, log.Warnf)
	observer.SetSeeding(seedingTracker.Bound)

	// 进程在预热中途重启：会话里还没有预热结果的池实例重新开始预热（带着沿用状态的从状态之后接着推进，否则从头；先于开始消费事件，门禁立刻拦住它们的事件）。
	if err := warm.Resume(ctx); err != nil {
		return nil, nil, fmt.Errorf("恢复策略池预热失败：%w", err)
	}
	if err := eventRuntime.Start(ctx); err != nil {
		return nil, nil, err
	}
	reporter, err := registerMetricsReporter(s, observer)
	if err != nil {
		return nil, nil, err
	}

	var consumer *eventconsumer.Consumer
	var handlerRef atomic.Pointer[trigger.Handler]
	ready := newModuleCheck(db, eventRuntime.Connected, func() bool { return consumer == nil || consumer.Ready() })
	life, err := newLifecycle(cfg.Database, lifecycleDeps{db: db, handler: &handlerRef, warm: warm, ready: ready})
	if err != nil {
		return nil, nil, fmt.Errorf("打开策略生命周期状态：%w", err)
	}
	observer.SetHalted(life.Halted)
	if inputClient != nil {
		meta := input.NewMetaCache()
		meta.Ages = db
		handler := &trigger.Handler{Store: db, Loader: input.Loader{Client: inputClient, Meta: meta}, AttemptBudget: cfg.Evaluation.AttemptBudget, Observer: observer, Logf: log.Infof, Gate: triggerGate(life, ready, warm), ReleasedAt: releasedAt(life), EnsureWarm: warm.Ensure}
		handlerRef.Store(handler)
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
	loops.Add(6)
	submitter := &paramsubmit.Submitter{Store: db, Factors: live, Allowed: paramSubmitAllowed(triggerGate(life, ready, warm)), OnResult: observer.ParamSubmitted,
		OnError: func(requestID string, err error) { log.Warnf("因子参数提交 %s 未完成：%v", requestID, err) }}
	go func() {
		defer loops.Done()
		submitter.Run(background)
	}()
	go func() {
		defer loops.Done()
		runner.Run(background)
	}()
	go func() {
		defer loops.Done()
		seedingTracker.Run(background, seeding.RefreshEvery)
	}()
	go func() {
		defer loops.Done()
		retentionLoop(background, db, cfg.Retention)
	}()
	go func() {
		defer loops.Done()
		runModuleCheck(background, ready)
	}()
	go func() {
		defer loops.Done()
		life.Run(background, lifecycleRefreshEvery)
	}()

	// 只注册到 StrategyMgr 自己的监听；注册到整个 server 会让健康检查端口也能调用管理方法。
	strategyMgr := s.Service("trpc.moox.strategy.StrategyMgr")
	if strategyMgr == nil {
		return nil, nil, errors.New("缺少 tRPC 服务 trpc.moox.strategy.StrategyMgr")
	}
	service.Lifecycle = life
	strategypb.RegisterStrategyMgrService(strategyMgr, service)
	healthState := health.New("strategy", "strategy", "", "")
	healthState.SnapshotFunc = strategyHealthSnapshot(db, eventRuntime, healthState, consumer)
	// 上报带给 Monitor 的就绪与 /readyz 是同一个判断；模块级生命周期也随上报，首页的系统状态读它。
	reporter.SetReadiness(healthz.Readiness(healthState.Snapshot))
	reporter.SetLifecycle(life.ReportState)
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

// seedingListTimeout 是单次读取历史补齐状态的超时：调不通就沿用上一次的结果。
const seedingListTimeout = 10 * time.Second

// 私有因子任务的结果 View 由 Storage 异步补齐成员列：启用实例时最多等这么久。
const (
	factorTaskReadyWait = 90 * time.Second
	factorTaskReadyStep = 3 * time.Second
)

// paramSubmitAllowed 把触发门禁转成参数提交的门禁：模块暂停时不提交（ModeOff），模块自检没通过、策略池预热中也不提交（ModeWait），其余模式照常。
func paramSubmitAllowed(gate func(string) trigger.Mode) func(string) bool {
	return func(instanceID string) bool {
		mode := gate(instanceID)
		return mode != trigger.ModeOff && mode != trigger.ModeWait
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
		if deleted, err := repo.DeleteReadyEventsBefore(ctx, now.AddDate(0, 0, -cfg.ReadyEventsDays)); err != nil {
			log.Warnf("清理过期就绪事件记录失败：%v", err)
		} else if deleted > 0 {
			log.Infof("已清理 %d 条过期就绪事件记录", deleted)
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

// stockCalendarWarning 在有启用实例使用股票市场日历（A 股、港股等）时检查对应的内嵌日历：将到期时给出提醒，
// 已过期时报未就绪（之后的周期无法换算，会被丢弃）。多份日历都有问题时合并说明。
func stockCalendarWarning(ctx context.Context, db *store.Store, now time.Time) (string, bool) {
	enabled := true
	instances, err := db.ListInstances(ctx, "", &enabled)
	if err != nil {
		return "", false
	}
	var calendars []string
	for _, instance := range instances {
		var resolved struct {
			Calendar string `json:"calendar"`
		}
		if json.Unmarshal(instance.ResolvedJSON, &resolved) != nil {
			continue
		}
		calendar := strings.ToLower(strings.TrimSpace(resolved.Calendar))
		if period.SessionCalendar(calendar) && !slices.Contains(calendars, calendar) {
			calendars = append(calendars, calendar)
		}
	}
	slices.Sort(calendars)
	var messages []string
	down := false
	for _, calendar := range calendars {
		err := period.StockCalendarReadiness(calendar, now, calendarWarningWindow, trigger.ValidBars)
		switch {
		case err == nil:
		case errors.Is(err, period.ErrCalendarExpiring):
			messages = append(messages, err.Error())
		default:
			messages = append(messages, err.Error()+"；使用日历 "+calendar+" 的实例无法求值")
			down = true
		}
	}
	return strings.Join(messages, "；"), down
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
func newInputClient(cfg Config, gateway gatewayclient.Invoker) *upstream.RPCClient {
	return upstream.NewGatewayClient(gateway, cfg.Storage.Timeout, cfg.Factor.Timeout)
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
		switch {
		case !databaseReady:
			rsp.Reason = "数据库不可用"
		case !eventBusConnected:
			rsp.Reason = "出站 EventBus 未连接"
		case !consumerReady:
			rsp.Reason = "就绪事件消费者未连接"
		case statsErr != nil:
			rsp.Reason = "读取待投递目标失败：" + statsErr.Error()
		case outboxStalled:
			rsp.Reason = "目标投递停滞"
		case calendarExpired:
			rsp.Reason = calendarWarning
		}
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
