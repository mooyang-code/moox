package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	equityapp "github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	"github.com/mooyang-code/moox/modules/trade/internal/application/fundmaint"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/application/marginmonitor"
	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	walletapp "github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/hkex"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	executionpaper "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/health"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradeobservability "github.com/mooyang-code/moox/modules/trade/internal/observability"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/modules/trade/internal/secretclient"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/modules/trade/internal/wiring"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var startedAt = time.Now()

// maxChildNotional 是下单校验的单笔金额硬上限；目标拆单的单笔上限在组合的下单设置里，不能超过它。
const maxChildNotional = ordersettings.MaxChildNotionalCeiling

// Initialize 读 configPath 指向的业务配置（-config）并装配服务。
func Initialize(ctx context.Context, serverInstance *server.Server, configPath string) (*server.Server, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return initialize(ctx, serverInstance, cfg)
}

func initialize(
	ctx context.Context,
	serverInstance *server.Server,
	cfg *config.AppConfig,
) (*server.Server, error) {
	if serverInstance == nil || cfg == nil {
		return nil, errors.New("trade bootstrap: server and config are required")
	}
	tradeStore, err := store.Open(cfg.Database.Path)
	if err != nil {
		return nil, err
	}
	cleanupStore := true
	defer func() {
		if cleanupStore {
			_ = tradeStore.Close()
		}
	}()

	gateway, err := cfg.OpenGateway(func(err error) { log.WarnContextf(ctx, "trade gateway directory refresh failed: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("initialize trade gateway: %w", err)
	}
	defer func() {
		if cleanupStore {
			_ = gateway.Close()
		}
	}()
	secrets := secretclient.New(gateway)
	registry := execution.NewRegistry()
	// 股票场所（A 股、港股）的公共行情整个进程各一份：快照缓存、标的列表都共用；列表加载完之前用库里已有的标的。
	stockSeed := func(name exchange.Exchange) stockfeed.Seed {
		return func(seedCtx context.Context) ([]exchange.Instrument, error) {
			records, listErr := tradeStore.ListInstruments(seedCtx, string(name), string(exchange.MarketTypeEquity))
			if listErr != nil {
				return nil, listErr
			}
			instruments := make([]exchange.Instrument, 0, len(records))
			for _, record := range records {
				instruments = append(instruments, wiring.InstrumentFromRecord(name, exchange.MarketTypeEquity, record))
			}
			return instruments, nil
		}
	}
	ashareMarket := ashare.NewMarket(nil, stockSeed(exchange.ExchangeAShare))
	hkexMarket := hkex.NewMarket(nil, stockSeed(exchange.ExchangeHKEX))
	if err := wiring.RegisterBuiltins(registry, ashareMarket, hkexMarket); err != nil {
		return nil, err
	}
	moduleMetrics, reporter := registerMetricsReporter(serverInstance)
	tradeStore.SetModuleMetrics(moduleMetrics)

	manager := &traderuntime.Manager{
		Accounts: tradeStore, PollInterval: 5 * time.Second,
		RetryMin: time.Second, RetryMax: 30 * time.Second,
	}
	accounts := &accountapp.Service{
		Store: accountapp.Repository{Store: tradeStore}, RawStore: tradeStore, Secrets: secrets,
		SessionState: manager, SymbolConfigs: manager, Exchanges: registry, LiveTradingEnabled: cfg.Runtime.LiveTradingEnabled,
		Inspector: registry,
	}
	orderService := &orderapp.Service{
		Store: tradeStore, Adapters: manager, SymbolConfig: manager, RequestSync: manager.RequestSync,
		Validator: orderapp.Validator{
			Accounts:         accounts,
			Instruments:      wiring.InstrumentSource{Store: tradeStore},
			Positions:        wiring.PositionSource{Store: tradeStore},
			MaxReferenceAge:  10 * time.Second,
			MaxChildNotional: shared.MustDecimal(maxChildNotional),
			MaxLeverage:      shared.MustDecimal("20"),
			FeeBufferRate:    shared.MustDecimal("0.002"),
			Quotes:           manager,
		},
	}
	balanceMetrics, err := tradeobservability.DefaultBalanceMetrics()
	if err != nil {
		return nil, fmt.Errorf("register trade balance metrics: %w", err)
	}
	manager.OnSessionRemoved = balanceMetrics.Remove
	balanceLimit := cfg.Runtime.BalanceDifferenceThreshold
	telemetry.Attention.AddSource("balance", func(now time.Time) map[string][]string {
		return balanceMetrics.Attention(telemetry.AttentionBalanceSyncFailing, balanceLimit, now)
	})
	equityService := &equityapp.Service{Store: tradeStore, Adapters: manager, Exchanges: registry}
	equitySampler := traderuntime.NewEquitySampler(equityService)
	registerEquitySamplerTimer(serverInstance, equitySampler)
	fillReducer := &consumer.Reducer{Store: tradeStore, Enqueue: equitySampler.Enqueue}
	syncService := &accountsync.Service{
		Store: tradeStore, Adapters: manager, SessionState: manager,
		Fills: fillReducer, Orders: orderService, Metrics: balanceMetrics, Equity: equityService,
	}
	paperMatcher := &executionpaper.Matcher{
		Store:     tradeStore,
		Reducer:   fillReducer,
		Enqueue:   equitySampler.Enqueue,
		Exchanges: registry,
	}
	paperMatcher.Refresh = func(refreshCtx context.Context, tradingAccountID string) error {
		adapter, adapterErr := manager.Adapter(tradingAccountID)
		if adapterErr != nil {
			return adapterErr
		}
		snapshot, snapshotErr := adapter.GetAccountSnapshot(refreshCtx)
		if snapshotErr != nil {
			return snapshotErr
		}
		account, accountErr := tradeStore.GetTradingAccountByID(refreshCtx, tradingAccountID)
		if accountErr != nil {
			return execution.InfrastructureError{Err: accountErr}
		}
		// 模拟盘快照的权益同样按统一公式落库（与实盘同步、权益采样一致）；估值失败保留适配器的值。
		refreshed := store.SnapshotFromExchange(snapshot)
		if equity, equityErr := equityService.ValueEquity(refreshCtx, account, refreshed, adapter); equityErr == nil {
			refreshed.Equity = equity
		}
		err := tradeStore.Transaction(refreshCtx, func(tx *store.Tx) error {
			// The paper snapshot is reconstructed from the same SQLite facts that
			// back reservations. Advance the sync watermark together with it so
			// the next order does not count already-reflected resting reservations
			// a second time, while orders created after this refresh remain visible
			// through GetUnreflectedReservation.
			at := snapshot.ExchangeUpdatedAt.UnixMilli()
			return tx.UpdateTradingAccountFacts(
				account.SpaceID,
				tradingAccountID,
				account.FillCursors,
				refreshed,
				at,
				at,
			)
		})
		if err != nil {
			return execution.InfrastructureError{Err: err}
		}
		return nil
	}
	paperMatcher.DecideContext = (&executionpaper.Decider{Store: tradeStore, Adapters: manager, Exchanges: registry}).Decide
	paperMatcherWorker := traderuntime.NewPaperMatcherWorker(paperMatcher, time.Second)
	syncer := wiring.AccountSyncer{Service: syncService}
	orderService.Syncer = syncer
	targetExecutor := &targetapp.Executor{
		Exchanges: registry,
		Store:     tradeStore, Orders: orderService,
		Prices:     targetapp.ExchangePriceSource{Adapters: manager, Exchanges: registry},
		OnReversal: noteTargetReversal(tradeStore),
		Observe:    &telemetry.ExecutionMetrics{},
		// 每秒最多一批：永续每批 16 单、现货 8 单，约为交易所下单限额的 80%。
		MinBatchInterval: time.Second,
		// 模拟盘没有交易所限频，批间隔缩短到 0.3 秒。
		PaperMinBatchInterval: 300 * time.Millisecond,
		RateStatus:            adapterRateStatus(manager),
		// 普通账户的第 4、6 阶段：钱包划转与归集；统一账户、OKX、模拟盘不会调用。
		Transfers: targetapp.NewWalletTransfer(&walletapp.Service{
			Store: tradeStore, Adapters: manager,
			RequestSync: func(accountID string) { manager.RequestSync(accountID) },
		}),
	}
	observeRateLimits()
	weightResolver := &targetapp.WeightResolver{
		Store:     tradeStore,
		Exchanges: registry,
		Prices:    targetapp.ExchangePriceSource{Adapters: manager, Exchanges: registry},
		Equity:    equityService,
	}
	// 工人先拿到门禁，生命周期控制器在第一个工人启动之前建好并绑定（见 runWorker 之前）。
	lifeGate := &lifecycleGate{}
	targetWorker := &traderuntime.TargetWorker{
		Store: tradeStore, Executor: targetExecutor, Interval: time.Second,
		Metrics: tradeStore.ModuleMetrics(), Gate: lifeGate.Open,
	}
	syncService.Wake = targetWorker.Wake
	paperMatcher.WakeTarget = targetWorker.WakeTarget
	logicalAccounts := &logicalapp.Service{
		Store: tradeStore, Syncer: syncer,
		Prices: targetapp.ExchangePriceSource{Adapters: manager, Exchanges: registry},
		// 关闭策略会话之后唤醒执行器，由过期路径撤掉这份目标的挂单。
		WakeTarget: targetWorker.WakeTarget,
	}
	inspectionWorker := &traderuntime.InspectionWorker{Store: tradeStore, Accounts: accounts, Logical: logicalAccounts}
	// 资金维护（BNB 抵扣与补买、小额兑 BNB、稳定币调平）：每小时一次，资金设置变更后马上一次。
	fundService := &fundmaint.Service{Store: tradeStore, Adapters: manager, Orders: orderService}
	fundWorker := &traderuntime.FundWorker{Maintainer: fundService, Interval: time.Hour, Gate: lifeGate.Open}
	logicalAccounts.OnFundSettingsChanged = func(spaceID, logicalAccountID string) {
		fundService.Invalidate(context.Background(), spaceID, logicalAccountID)
		fundWorker.Wake(spaceID, logicalAccountID)
	}
	operatorService := &operatorapp.Service{
		Exchanges: registry, Wake: targetWorker.Wake,
		Store: tradeStore, Orders: orderService,
		Syncer: syncer,
		Prices: targetapp.ExchangePriceSource{Adapters: manager, Exchanges: registry},
	}
	// 保证金率监控：每分钟检查一次实盘账户；它复用资金维护的定时任务外壳（定时、门禁、超时一样）。
	marginService := &marginmonitor.Service{
		Store: tradeStore,
		Reduce: func(
			callCtx context.Context, spaceID, actionID, logicalAccountID string, ratio shared.Decimal, pause store.Pause,
		) (store.OperatorActionRecord, error) {
			result, reduceErr := operatorService.FlattenLogicalAccount(callCtx, operatorapp.FlattenCommand{
				SpaceID: spaceID, ActionID: actionID, LogicalAccountID: logicalAccountID,
				Reason: pause.Reason, ReduceRatio: ratio, PauseAs: pause, Source: operatordomain.SourceMargin,
			})
			return result.Action, reduceErr
		},
		Collect: func(
			callCtx context.Context, spaceID, logicalAccountID, idempotencyKey string, accounts []store.TradingAccountRecord,
		) (bool, error) {
			// 归集用执行器的钱包划转。风控划转没有目标：TargetID 为空串（见 wallet_transfer.sql）。idempotencyKey（风控减仓的动作号 margin-<组合>-<时间>）
			// 是这次风控事件的幂等键：钱包按它做幂等（划转记录的本地引用号带着它），不同风控事件的归集互不影响。
			return targetExecutor.Transfers.CollectForRisk(callCtx, spaceID, logicalAccountID, idempotencyKey, accounts)
		},
		Pause: logicalAccounts.PauseIf,
	}
	marginWorker := &traderuntime.FundWorker{Maintainer: marginService, Interval: time.Minute, Gate: lifeGate.Open, Name: "保证金率监控"}
	// 目标回执与影子目标的清理：启动时一次，之后每 6 小时一次；同样复用这个定时任务外壳。清理不是交易行为，不受暂停影响。
	targetHistoryWorker := &traderuntime.FundWorker{
		Maintainer: &traderuntime.TargetHistoryPruner{Store: tradeStore}, Interval: 6 * time.Hour, Name: "目标回执与影子目标清理",
	}
	// 结束超过保留天数（模拟账户配置，默认 5 天）的订单与成交的清理（所有模拟账户，见 OrderHistoryPruner）：启动时一次，之后每小时一次，分批让出连接（积压清完后每轮只有一小时的量）。
	orderHistoryWorker := &traderuntime.FundWorker{
		Maintainer: &traderuntime.OrderHistoryPruner{Store: tradeStore}, Interval: time.Hour, Name: "历史订单清理",
	}
	// 分钟净值历史降采样：与 store.DefaultEquityRetentionTiers 一致，0~7 天保留分钟级，7~30 天降采样为 15 分钟级，30~365 天降采样为小时级，365 天以上降采样为日级。
	equityHistoryWorker := &traderuntime.FundWorker{
		Maintainer: &traderuntime.EquityHistoryDownsampler{Store: tradeStore}, Interval: 6 * time.Hour, Name: "分钟净值历史降采样",
	}
	// 下架处理：收事件、名单查询与定时推进共用这一个实例。清仓在进程内直接调用 FlattenLogicalAccount。
	delistService := &delistapp.Service{
		Store: tradeStore, OnChanged: targetWorker.Wake,
		Prices: targetapp.ExchangePriceSource{Adapters: manager, Exchanges: registry},
		Flatten: func(callCtx context.Context, request delistapp.FlattenRequest) error {
			_, flattenErr := operatorService.FlattenLogicalAccount(callCtx, operatorapp.FlattenCommand{
				SpaceID: request.SpaceID, ActionID: request.ActionID, LogicalAccountID: request.LogicalAccountID,
				Reason: request.Reason, InstrumentID: request.InstrumentID, Exchange: request.Exchange,
				Source: operatordomain.SourceDelist, DeadlineAt: request.DeadlineAt,
			})
			if errors.Is(flattenErr, operatorapp.ErrLogicalAccountBusy) {
				return delistapp.ErrFlattenBusy
			}
			return flattenErr
		},
	}
	// 下架定时任务：每 30 秒按记录推进时间线；暂停、恢复追赶时不推进（收事件、写名单照常）。
	delistWorker := &traderuntime.FundWorker{Maintainer: delistService, Interval: 30 * time.Second, Gate: lifeGate.Open, Name: "下架处理"}
	operatorWorker := &traderuntime.OperatorWorker{
		Actions: tradeStore, Resumer: operatorService, Interval: time.Second,
		ActionScoped: operatorapp.ActionScopedError,
	}
	manager.NewSession = func(record store.TradingAccountRecord) (traderuntime.ManagedSession, error) {
		credential := exchange.Credential{}
		if exchange.ExecutionMode(record.ExecutionMode) == exchange.ExecutionModeLive {
			var credentialErr error
			credential, credentialErr = wiring.ExchangeCredential(
				context.Background(),
				secrets,
				exchange.Exchange(record.Exchange),
				record.CredentialSecretID,
			)
			if credentialErr != nil {
				return nil, credentialErr
			}
			// 同一个密钥 ID 下直接改了密钥内容会绕过检测：绑定前核对密钥指纹，对不上就不绑定并立即重新检测。
			if fingerprintErr := verifyCredentialFingerprint(record, credential); fingerprintErr != nil {
				recheckInspection(inspectionWorker, record.SpaceID, record.TradingAccountID)
				return nil, fingerprintErr
			}
		}
		accountConfig := exchange.AccountConfig{
			TradingAccountID: record.TradingAccountID,
			Exchange:         exchange.Exchange(record.Exchange),
			Markets:          wiring.MarketTypes(record.Markets),
			ExecutionMode:    exchange.ExecutionMode(record.ExecutionMode),
			AccountMode:      exchange.AccountMode(record.AccountMode),
			Environment:      exchange.AccountEnvironment(record.Environment),
			SettlementAsset:  record.SettlementAsset,
			MarginMode:       exchange.MarginMode(record.MarginMode),
			CreatedAt:        record.CreatedAt,
		}
		var adapter execution.ExecutionAdapter
		var marketData execution.MarketDataSource
		var accountEvents execution.AccountEventSource
		if accountConfig.ExecutionMode == exchange.ExecutionModePaper {
			publicConfig := accountConfig
			// Public market-data endpoints do not require private credentials. Keep
			// this binding PAPER so the registry does not apply live credential
			// requirements; the environment still pins it to production.
			publicConfig.ExecutionMode = exchange.ExecutionModePaper
			publicConfig.Environment = exchange.AccountEnvironmentProduction
			publicAdapter, bindErr := registry.Bind(publicConfig, exchange.Credential{})
			if bindErr != nil {
				return nil, bindErr
			}
			marketData = publicAdapter
			adapter = &executionpaper.Adapter{
				Account: record, Store: tradeStore, MarketData: publicAdapter, Exchanges: registry, Wake: paperMatcherWorker.Wake,
			}
		} else {
			var bindErr error
			adapter, bindErr = registry.Bind(accountConfig, credential)
			if bindErr != nil {
				return nil, bindErr
			}
			marketData = adapter
			// 没有私有流的场所不订阅，会话改为短周期同步。
			accountEvents = execution.AccountEventsOf(adapter)
		}
		return &traderuntime.ExchangeSession{
			Account: record, Adapter: adapter, MarketData: marketData, AccountEvents: accountEvents,
			Sync:                  syncService,
			PaperMatcherReady:     paperMatcherWorker.Ready,
			PaperAccountState:     paperMatcher.AccountState,
			PaperAccountRecovered: paperMatcher.RecoverAccount,
			SubscribePaperState:   traderuntime.CombineStateSubscriptions(paperMatcherWorker.SubscribeState, paperMatcher.SubscribeState),
			OnReady: func(tradingAccountID string) {
				equitySampler.Enqueue(tradingAccountID)
				fundWorker.AccountReady(tradingAccountID)
			},
			SyncInterval: 30 * time.Second,
		}, nil
	}

	var eventBus *jetstream.Client
	var targetConsumerReady atomic.Bool
	delistConsumerState := eventconsumer.NewRunState(time.Now())
	if cfg.EventBus.Enabled {
		clientConfig := jetstream.ConfigFromEnv(cfg.EventBus.URLs, "moox-trade")
		if cfg.EventBus.CredentialFile != "" {
			if err := clientConfig.ApplyCredentialFile(
				jetstream.ExpandCredentialPath(cfg.EventBus.CredentialFile),
			); err != nil {
				return nil, err
			}
		}
		eventBus, err = jetstream.Connect(ctx, clientConfig)
		if err != nil {
			return nil, err
		}
	}

	// 就绪判定和生命周期控制器在第一个工人启动之前建好并绑定到门禁：暂停状态下重启时，
	// 后台循环的第一轮就要看到“已暂停”。就绪判定里存的只是函数引用，用到时才求值，所以这里就能建；
	// 控制器构造时只读状态文件、不调用任何钩子，健康检查注册和 Recover、Run 仍留在后面。
	readiness := newReadiness(
		tradeStore, manager, targetWorker, operatorWorker, paperMatcherWorker, paperMatcher,
		cfg.EventBus.Enabled, eventBus, &targetConsumerReady, delistConsumerState,
	)
	life, err := newLifecycle(
		cfg.Database.Path, targetWorker.Quiesce,
		tradeCatchup(readiness.Evaluate, tradeStore, targetWorker, time.Now),
	)
	if err != nil {
		if eventBus != nil {
			eventBus.Close()
		}
		return nil, fmt.Errorf("trade lifecycle state failed to open: %w", err)
	}
	lifeGate.Bind(life)

	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	runWorker := func(run func(context.Context) error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if runErr := run(runtimeCtx); runErr != nil &&
				!errors.Is(runErr, context.Canceled) {
				log.Warnf("trade runtime worker stopped: %v", runErr)
			}
		}()
	}
	runWorker(manager.Run)
	for _, market := range []*stockfeed.Market{ashareMarket, hkexMarket} {
		runWorker(func(workerCtx context.Context) error {
			market.RunListRefresh(workerCtx)
			return workerCtx.Err()
		})
	}
	runWorker(targetWorker.Run)
	runWorker(operatorWorker.Run)
	runWorker(paperMatcherWorker.Run)
	runWorker(equitySampler.Run)
	runWorker(inspectionWorker.Run)
	runWorker(fundWorker.Run)
	runWorker(marginWorker.Run)
	runWorker(targetHistoryWorker.Run)
	runWorker(orderHistoryWorker.Run)
	runWorker(equityHistoryWorker.Run)
	runWorker(delistWorker.Run)
	attentionWorker := &traderuntime.AttentionWorker{Store: tradeStore, Executor: targetExecutor, Delist: delistService}
	if eventBus != nil {
		attentionWorker.DelistConsumer = delistConsumerState.Status
	}
	runWorker(attentionWorker.Run)
	// 收盘撤单：有日收盘的场所（A 股、港股）日收盘后先全量同步，再把收盘前提交、仍在途的订单按已撤结束；重启后第一轮就补做。
	sessionCloser := &orderapp.SessionCloser{
		Store: tradeStore, Exchanges: registry,
		Sync: func(syncCtx context.Context, tradingAccountID string) error {
			_, syncErr := syncService.SyncAccountFull(syncCtx, tradingAccountID)
			return syncErr
		},
	}
	runWorker(func(workerCtx context.Context) error {
		return runSessionClose(workerCtx, sessionCloser, lifeGate.Open)
	})
	if eventBus != nil {
		client := eventBus
		runWorker(func(workerCtx context.Context) error {
			return eventconsumer.RunTarget(workerCtx, eventconsumer.TargetOptions{
				Client: client, ConsumerName: cfg.EventBus.TargetConsumer,
				Store: tradeStore, WakeTarget: targetWorker.WakeTarget,
				SetReady:       targetConsumerReady.Store,
				WeightResolver: weightResolver,
				Intake:         lifeGate.Open,
				Exchanges:      registry,
			})
		})
		runWorker(func(workerCtx context.Context) error {
			return eventconsumer.RunDelist(workerCtx, eventconsumer.DelistOptions{
				Client: client, ConsumerName: cfg.EventBus.DelistConsumer,
				Delist: delistService, State: delistConsumerState,
			})
		})
	}
	rpc.RegisterAll(
		serverInstance,
		&rpc.AccountServer{Accounts: accounts, Sync: syncService, Store: tradeStore, AfterInspection: inspectionWorker.Handle},
		&rpc.LogicalAccountServer{
			LogicalAccounts: logicalAccounts,
			Store:           tradeStore,
			Flatten: func(
				callCtx context.Context,
				spaceID string,
				actionID string,
				logicalAccountID string,
				reason string,
			) (store.OperatorActionRecord, error) {
				result, flattenErr := operatorService.FlattenLogicalAccount(
					callCtx,
					operatorapp.FlattenCommand{
						SpaceID: spaceID, ActionID: actionID,
						LogicalAccountID: logicalAccountID, Reason: reason,
					},
				)
				return result.Action, flattenErr
			},
		},
		&rpc.ExecutionServer{
			Store: tradeStore,
			Lanes: targetExecutor.LaneStatuses,
			Submit: func(callCtx context.Context, command rpc.SubmitOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
				result, submitErr := operatorService.PlaceOrder(callCtx, operatorapp.ManualOrderCommand{
					SpaceID: command.SpaceID, ActionID: command.ActionID, LogicalAccountID: command.LogicalAccountID,
					TradingAccountID: command.TradingAccountID, ClientOrderID: command.ClientOrderID,
					InstrumentID: command.InstrumentID, Type: command.OrderType, FillPolicy: command.FillPolicy,
					Side: command.Side, PositionSide: command.PositionSide,
					Quantity: command.Quantity, LimitPrice: command.LimitPrice,
					Reason: command.Reason, DeadlineAt: command.DeadlineAt,
				})
				return result.Action, result.Order, submitErr
			},
			Cancel: func(
				callCtx context.Context,
				spaceID string,
				actionID string,
				orderID string,
				reason string,
			) (store.OperatorActionRecord, store.OrderRecord, error) {
				result, cancelErr := operatorService.CancelOrder(
					callCtx,
					operatorapp.CancelOrderCommand{
						SpaceID: spaceID, ActionID: actionID,
						OrderID: orderID, Reason: reason,
					},
				)
				return result.Action, result.Order, cancelErr
			},
		},

		rpc.ConsoleOptions{
			Exchanges:          registry,
			LiveTradingEnabled: cfg.Runtime.LiveTradingEnabled,
			MatcherReady:       paperMatcherWorker.Ready,
			Holdings:           &rpc.HoldingQuery{Store: tradeStore.ReadOnly(), Adapters: manager},
			Adapters:           manager,
			Delist:             delistService,
			Lifecycle:          life,
			FlattenAccount: func(
				callCtx context.Context, spaceID, actionID, tradingAccountID, reason string,
			) (store.OperatorActionRecord, error) {
				result, flattenErr := operatorService.FlattenTradingAccount(callCtx, operatorapp.FlattenCommand{
					SpaceID: spaceID, ActionID: actionID, TradingAccountID: tradingAccountID, Reason: reason,
				})
				return result.Action, flattenErr
			},
		},
	)
	// 停止顺序：先取消工人、等它们都退出，再关事件总线连接。目标消费者停下时要把在途消息立即退回（见 jetstream
	// Runner 的 releaseOnStop），连接先关了退回必然失败，这些消息要等 AckWait（60 秒）才重投。
	// 工人退出不依赖连接关闭：拉取消息用的是可取消的上下文。
	stopWorkers := func() {
		cancelRuntime()
		workers.Wait()
		if eventBus != nil {
			eventBus.Close()
		}
	}
	if err := registerHealth(runtimeCtx, serverInstance, life, readiness, reporter); err != nil {
		stopWorkers()
		return nil, err
	}

	serverInstance.RegisterOnShutdown(func() {
		stopWorkers()
		_ = gateway.Close()
		_ = tradeStore.Close()
	})
	cleanupStore = false
	return serverInstance, nil
}

// sessionCloseInterval 是收盘撤单的扫描间隔：没有要撤的单时每轮只读一次各股票账户的在途订单。
const sessionCloseInterval = 30 * time.Second

// runSessionClose 定时跑收盘撤单；生命周期门禁关着（暂停、恢复追赶）时跳过这一轮。
func runSessionClose(ctx context.Context, closer *orderapp.SessionCloser, gate func() bool) error {
	ticker := time.NewTicker(sessionCloseInterval)
	defer ticker.Stop()
	for {
		if gate == nil || gate() {
			runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			if err := closer.Run(runCtx); err != nil && ctx.Err() == nil {
				log.WarnContextf(ctx, "收盘撤单没有完成，下一轮重试：%v", err)
			}
			cancel()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// verifyCredentialFingerprint 核对账户检测结果里记录的密钥指纹和现在读到的密钥是否一致：
// 检测通过的是当时那把密钥；同一个密钥 ID 下改了密钥内容、或账户从来没检测过，都不能直接绑定。
func verifyCredentialFingerprint(record store.TradingAccountRecord, credential exchange.Credential) error {
	inspection, err := exchange.ParseInspection(record.InspectionJSON)
	if err != nil {
		return fmt.Errorf("账户的检测结果读不出来，需要重新检测：%w", err)
	}
	if inspection.InspectedAt.IsZero() || inspection.CredentialFingerprint == "" {
		return errors.New("账户的密钥还没有检测过，正在检测，通过后才会绑定")
	}
	if inspection.CredentialFingerprint != exchange.CredentialFingerprint(credential.APIKey) {
		return errors.New("密钥内容与检测时不一致（同一个密钥 ID 下改了密钥），正在重新检测，通过后才会绑定")
	}
	return nil
}

// recheckInspections 记录正在重新检测的账户，避免会话每次重试绑定都再起一个检测。
var recheckInspections sync.Map

// recheckInspection 在后台立即重新检测一个账户；检测通过后写入新的指纹，会话下次重试绑定就能通过。
func recheckInspection(worker *traderuntime.InspectionWorker, spaceID, accountID string) {
	if _, running := recheckInspections.LoadOrStore(accountID, struct{}{}); running {
		return
	}
	go func() {
		defer recheckInspections.Delete(accountID)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := worker.CheckAccount(ctx, spaceID, accountID); err != nil {
			log.Warnf("交易账户 %s 密钥内容变化后重新检测没有完成：%v", accountID, err)
		}
	}()
}

// registerMetricsReporter 注册指标上报；返回的处理器在健康检查注册时接上就绪判断与生命周期（见 registerHealth）。
func registerMetricsReporter(serverInstance *server.Server) (*report.ModuleMetrics, *report.Handler) {
	if serverInstance == nil {
		return nil, nil
	}
	moduleMetrics, err := report.NewModuleMetrics(
		prometheus.DefaultRegisterer,
		"trade",
		report.HealthCheckIDsForModule("trade"),
	)
	if err != nil {
		log.Warnf("trade module metrics disabled: %v", err)
		return nil, nil
	}
	handler, err := report.NewHandler(report.DefaultConfig("trade", "trade"))
	if err != nil {
		log.Warnf("trade metrics reporter disabled: %v", err)
		return moduleMetrics, nil
	}
	service := serverInstance.Service("trpc.moox.trade.metrics.timer")
	if service == nil {
		log.Warn("trade metrics timer service is not configured")
		return moduleMetrics, nil
	}
	timer.RegisterHandlerService(
		service,
		handler.Handle,
	)
	return moduleMetrics, handler
}

func registerEquitySamplerTimer(
	serverInstance *server.Server,
	sampler *traderuntime.EquitySampler,
) {
	if serverInstance == nil || sampler == nil {
		return
	}
	service := serverInstance.Service("trpc.moox.trade.equity.timer")
	if service == nil {
		log.Warn("trade equity timer service is not configured")
		return
	}
	timer.RegisterHandlerService(service, sampler.Handle)
}

// newReadiness 组装 Trade 的就绪判定：数据库、EventBus、账户会话和各个工人。
func newReadiness(
	tradeStore *store.Store,
	manager *traderuntime.Manager,
	targetWorker *traderuntime.TargetWorker,
	operatorWorker *traderuntime.OperatorWorker,
	paperMatcherWorker *traderuntime.PaperMatcherWorker,
	paperMatcher *executionpaper.Matcher,
	eventBusEnabled bool,
	eventBus *jetstream.Client,
	targetConsumerReady *atomic.Bool,
	delistConsumerState *eventconsumer.RunState,
) health.Readiness {
	return health.Readiness{
		DatabaseReady: func(ctx context.Context) error {
			return tradeStore.Ping(ctx)
		},
		EventBusEnabled: eventBusEnabled,
		EventBusReady: func() bool {
			return eventBus != nil && eventBus.Ready() &&
				targetConsumerReady != nil && targetConsumerReady.Load()
		},
		DelistConsumerRunning: func() bool {
			running, _ := delistConsumerState.Status()
			return running
		},
		Sessions:           manager,
		TargetWorker:       targetWorker.Snapshot,
		OperatorWorker:     operatorWorker.Snapshot,
		PaperMatcherWorker: paperMatcherWorker.State,
		PaperAccountErrors: paperMatcher.AccountErrors,
	}
}

// registerHealth 注册健康检查，把就绪判断与生命周期接到指标上报，并启动生命周期的定时刷新（生命周期的接口在 TradeConsoleService 上）。
// 控制器和就绪判定已在第一个工人启动之前建好、绑定到门禁。
func registerHealth(
	runtimeCtx context.Context,
	serverInstance *server.Server,
	life *lifecycle.Controller,
	readiness health.Readiness,
	reporter *report.Handler,
) error {
	state := health.New("trade", "trade", "", "")
	state.SnapshotFunc = health.SnapshotFunc(
		state,
		readiness,
		"trade",
		"trade",
		"",
		"",
		startedAt,
	)
	if reporter != nil {
		// 上报带给 Monitor 的就绪与 /readyz 是同一个判断；模块级生命周期也随上报，首页的系统状态读它。
		reporter.SetReadiness(healthz.Readiness(state.Snapshot))
		reporter.SetLifecycle(life.ReportState)
	}
	go life.Run(runtimeCtx, lifecycleRefreshEvery)
	if err := health.Register(serverInstance.Service("trpc.moox.trade.Health"), state); err != nil {
		return fmt.Errorf("trade health server failed to register: %w", err)
	}
	return nil
}

// noteTargetReversal 把同一目标内的反向下单记成关注项，详情用组合账户名，查不到时用 ID。
func noteTargetReversal(tradeStore *store.Store) func(string, string, string, time.Time) {
	return func(spaceID, logicalAccountID, instrumentID string, at time.Time) {
		name := logicalAccountID
		if record, err := tradeStore.GetLogicalAccount(context.Background(), spaceID, logicalAccountID); err == nil && record.Name != "" {
			name = record.Name
		}
		telemetry.Note(telemetry.AttentionTargetReversal, name+" "+instrumentID+"："+"同一目标内出现反向子单", at)
	}
}

// observeRateLimits 把限流暂停记成指标和关注项，权重读数记成指标。
func observeRateLimits() {
	httpclient.OnPause = func(host string, reason exchange.ErrorKind, until time.Time) {
		telemetry.RatePauses.WithLabelValues(host, string(reason)).Inc()
		telemetry.Note(telemetry.AttentionRateLimited, host+"："+exchange.RejectMessage(&exchange.Error{Kind: reason}), time.Now())
	}
	httpclient.OnWeight = func(host string, used, _ int) {
		telemetry.RateWeightUsed.WithLabelValues(host).Set(float64(used))
	}
}

// adapterRateStatus 返回执行账户适配器的限流状态；模拟盘和没有限流模型的场所返回 false。
func adapterRateStatus(manager *traderuntime.Manager) func(string) (exchange.RateStatus, bool) {
	return func(tradingAccountID string) (exchange.RateStatus, bool) {
		adapter, err := manager.Adapter(tradingAccountID)
		if err != nil {
			return exchange.RateStatus{}, false
		}
		source, ok := adapter.(exchange.RateStatusSource)
		if !ok {
			return exchange.RateStatus{}, false
		}
		return source.RateStatus(), true
	}
}
