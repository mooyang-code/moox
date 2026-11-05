package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	equityapp "github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/okx"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	executionpaper "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/health"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradeobservability "github.com/mooyang-code/moox/modules/trade/internal/observability"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/modules/trade/internal/secretclient"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var startedAt = time.Now()

// maxChildNotional 是单笔子单的金额上限，下单校验和目标拆单共用。
const maxChildNotional = "100000"

func Initialize(ctx context.Context, serverInstance *server.Server) (*server.Server, error) {
	cfg, err := config.Load("./config/app.yaml")
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
	registerBuiltins(registry)
	tradeStore.SetModuleMetrics(registerMetricsReporter(serverInstance))

	manager := &traderuntime.Manager{
		Accounts: tradeStore, PollInterval: 5 * time.Second,
		RetryMin: time.Second, RetryMax: 30 * time.Second,
	}
	accounts := &accountapp.Service{
		Store: accountapp.Repository{Store: tradeStore}, Secrets: secrets,
		SessionState: manager, SymbolConfigs: manager, Exchanges: registry, LiveTradingEnabled: cfg.Runtime.LiveTradingEnabled,
	}
	orderService := &orderapp.Service{
		Store: tradeStore, Adapters: manager, SymbolConfig: manager, RequestSync: manager.RequestSync,
		Validator: orderapp.Validator{
			Accounts:         accounts,
			Instruments:      instrumentSource{store: tradeStore},
			Positions:        positionSource{store: tradeStore},
			MaxReferenceAge:  10 * time.Second,
			MaxChildNotional: shared.MustDecimal(maxChildNotional),
			MaxLeverage:      shared.MustDecimal("20"),
			FeeBufferRate:    shared.MustDecimal("0.002"),
		},
	}
	balanceMetrics, err := tradeobservability.DefaultBalanceMetrics()
	if err != nil {
		return nil, fmt.Errorf("register trade balance metrics: %w", err)
	}
	manager.OnSessionRemoved = balanceMetrics.Remove
	equityService := &equityapp.Service{Store: tradeStore, Adapters: manager}
	equitySampler := traderuntime.NewEquitySampler(equityService)
	registerEquitySamplerTimer(serverInstance, equitySampler)
	fillReducer := &consumer.Reducer{Store: tradeStore, Enqueue: equitySampler.Enqueue}
	syncService := &accountsync.Service{
		Store: tradeStore, Adapters: manager, SessionState: manager,
		Fills: fillReducer, Orders: orderService, Metrics: balanceMetrics,
	}
	paperMatcher := &executionpaper.Matcher{
		Store:   tradeStore,
		Reducer: fillReducer,
		Enqueue: equitySampler.Enqueue,
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
			return executionpaper.InfrastructureError{Err: accountErr}
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
				store.SnapshotFromExchange(snapshot),
				at,
				at,
			)
		})
		if err != nil {
			return executionpaper.InfrastructureError{Err: err}
		}
		return nil
	}
	paperMatcher.DecideContext = (&executionpaper.Decider{Store: tradeStore, Adapters: manager}).Decide
	paperMatcherWorker := traderuntime.NewPaperMatcherWorker(paperMatcher, time.Second)
	orderService.Syncer = accountSyncer{service: syncService}
	targetExecutor := &targetapp.Executor{
		Exchanges: registry,
		Store:     tradeStore, Orders: orderService,
		Prices:           targetapp.ExchangePriceSource{Adapters: manager},
		MaxChildNotional: shared.MustDecimal(maxChildNotional),
		OnReversal:       noteTargetReversal(tradeStore),
		Observe:          &telemetry.ExecutionMetrics{},
		// 每秒最多一批：永续每批 16 单、现货 8 单，约为交易所下单限额的 80%。
		MinBatchInterval: time.Second,
		// 模拟盘没有交易所限频，批间隔缩短到 0.3 秒。
		PaperMinBatchInterval: 300 * time.Millisecond,
		RateStatus:            adapterRateStatus(manager),
	}
	observeRateLimits()
	weightResolver := &targetapp.WeightResolver{
		Store:  tradeStore,
		Prices: targetapp.ExchangePriceSource{Adapters: manager},
		Equity: equityService,
	}
	// 生命周期在 registerHealth 里才创建，门禁先建好，创建后绑定；绑定之前放行。
	lifeGate := &lifecycleGate{}
	targetWorker := &traderuntime.TargetWorker{
		Store: tradeStore, Executor: targetExecutor, Interval: time.Second,
		Metrics: tradeStore.ModuleMetrics(), Gate: lifeGate.Open,
	}
	syncService.Wake = targetWorker.Wake
	paperMatcher.WakeTarget = targetWorker.WakeTarget
	logicalAccounts := &logicalapp.Service{
		Store: tradeStore, Syncer: accountSyncer{service: syncService},
		Prices: targetapp.ExchangePriceSource{Adapters: manager},
	}
	operatorService := &operatorapp.Service{
		Exchanges: registry,
		Store:     tradeStore, Orders: orderService,
		Syncer: accountSyncer{service: syncService},
		Prices: targetapp.ExchangePriceSource{Adapters: manager},
	}
	operatorWorker := &traderuntime.OperatorWorker{
		Actions: tradeStore, Resumer: operatorService, Interval: time.Second,
	}
	manager.NewSession = func(record store.TradingAccountRecord) (traderuntime.ManagedSession, error) {
		credential := exchange.Credential{}
		if exchange.ExecutionMode(record.ExecutionMode) == exchange.ExecutionModeLive {
			var credentialErr error
			credential, credentialErr = exchangeCredential(
				context.Background(),
				secrets,
				exchange.Exchange(record.Exchange),
				record.CredentialSecretID,
			)
			if credentialErr != nil {
				return nil, credentialErr
			}
		}
		accountConfig := exchange.AccountConfig{
			TradingAccountID: record.TradingAccountID,
			Exchange:         exchange.Exchange(record.Exchange),
			Markets:          marketTypes(record.Markets),
			ExecutionMode:    exchange.ExecutionMode(record.ExecutionMode),
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
			adapter = &executionpaper.Adapter{Account: record, Store: tradeStore, MarketData: publicAdapter, Wake: paperMatcherWorker.Wake}
		} else {
			var bindErr error
			adapter, bindErr = registry.Bind(accountConfig, credential)
			if bindErr != nil {
				return nil, bindErr
			}
			marketData = adapter
			// 没有私有流的场所不订阅，会话改为短周期同步。
			accountEvents, _ = adapter.(execution.AccountEventSource)
		}
		return &traderuntime.ExchangeSession{
			Account: record, Adapter: adapter, MarketData: marketData, AccountEvents: accountEvents,
			Sync:                  syncService,
			PaperMatcherReady:     paperMatcherWorker.Ready,
			PaperAccountState:     paperMatcher.AccountState,
			PaperAccountRecovered: paperMatcher.RecoverAccount,
			OnReady:               equitySampler.Enqueue,
			SyncInterval:          30 * time.Second,
		}, nil
	}

	var eventBus *jetstream.Client
	var targetConsumerReady atomic.Bool
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
	runWorker(targetWorker.Run)
	runWorker(operatorWorker.Run)
	runWorker(paperMatcherWorker.Run)
	runWorker(equitySampler.Run)
	runWorker((&traderuntime.AttentionWorker{Store: tradeStore, Executor: targetExecutor}).Run)
	if eventBus != nil {
		client := eventBus
		runWorker(func(workerCtx context.Context) error {
			return eventconsumer.RunTarget(workerCtx, eventconsumer.TargetOptions{
				Client: client, ConsumerName: cfg.EventBus.TargetConsumer,
				Store: tradeStore, WakeTarget: targetWorker.WakeTarget,
				SetReady:       targetConsumerReady.Store,
				WeightResolver: weightResolver,
				Intake:         lifeGate.Open,
			})
		})
	}
	rpc.RegisterAll(
		serverInstance,
		&rpc.AccountServer{Accounts: accounts, Sync: syncService, Store: tradeStore},
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
			PlaceManual: func(
				callCtx context.Context,
				command rpc.ManualOrderCommand,
			) (store.OperatorActionRecord, store.OrderRecord, error) {
				result, placeErr := operatorService.PlaceManualOrder(
					callCtx,
					operatorapp.ManualOrderCommand{
						SpaceID: command.SpaceID, ActionID: command.ActionID,
						TradingAccountID: command.TradingAccountID,
						ClientOrderID:    command.ClientOrderID,
						InstrumentID:     command.InstrumentID,
						Type:             command.OrderType, FillPolicy: command.FillPolicy,
						Side: command.Side, PositionSide: command.PositionSide,
						Quantity: command.Quantity, LimitPrice: command.LimitPrice,
						Reason: command.Reason, DeadlineAt: command.DeadlineAt,
					},
				)
				return result.Action, result.Order, placeErr
			},
			SubmitOrdinary: func(callCtx context.Context, command rpc.SubmitOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
				result, submitErr := operatorService.SubmitOrder(callCtx, operatorapp.SubmitOrderCommand{
					LogicalAccountID: command.LogicalAccountID,
					ManualOrderCommand: operatorapp.ManualOrderCommand{
						SpaceID: command.SpaceID, ActionID: command.ActionID,
						TradingAccountID: command.TradingAccountID, ClientOrderID: command.ClientOrderID,
						InstrumentID: command.InstrumentID, Type: command.OrderType, FillPolicy: command.FillPolicy,
						Side: command.Side, PositionSide: command.PositionSide,
						Quantity: command.Quantity, LimitPrice: command.LimitPrice,
						Reason: command.Reason, DeadlineAt: command.DeadlineAt,
					},
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
			Paper:              &papersimulation.Service{Store: tradeStore},
			Exchanges:          registry,
			LiveTradingEnabled: cfg.Runtime.LiveTradingEnabled,
			MatcherReady:       paperMatcherWorker.Ready,
			Holdings:           &rpc.HoldingQuery{Store: tradeStore, Adapters: manager},
			Adapters:           manager,
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
	if err := registerHealth(
		runtimeCtx,
		cfg.Database.Path,
		serverInstance,
		tradeStore,
		manager,
		targetWorker,
		operatorWorker,
		paperMatcherWorker,
		paperMatcher,
		cfg.EventBus.Enabled,
		eventBus,
		&targetConsumerReady,
		lifeGate,
		accountSyncer{service: syncService}.SyncAccount,
	); err != nil {
		cancelRuntime()
		if eventBus != nil {
			eventBus.Close()
		}
		workers.Wait()
		return nil, err
	}

	serverInstance.RegisterOnShutdown(func() {
		cancelRuntime()
		if eventBus != nil {
			eventBus.Close()
		}
		workers.Wait()
		_ = gateway.Close()
		_ = tradeStore.Close()
	})
	cleanupStore = false
	return serverInstance, nil
}

// registerBuiltins 是唯一的场所注册列表，新增场所时在这里加一行。
func registerBuiltins(registry *execution.Registry) {
	cryptoExchange := func(name exchange.Exchange) execution.ExchangeDescriptor {
		return execution.ExchangeDescriptor{
			Exchange:     name,
			OrderTypes:   []exchange.OrderType{exchange.OrderTypeMarket, exchange.OrderTypeLimit},
			FillPolicies: []exchange.FillPolicy{exchange.FillPolicyGTC, exchange.FillPolicyIOC, exchange.FillPolicyFOK},
		}
	}
	registry.Register(cryptoExchange(exchange.ExchangeBinance), func(
		config exchange.AccountConfig,
		credential exchange.Credential,
	) (execution.ExecutionAdapter, error) {
		if err := requireAPIKey(config, credential); err != nil {
			return nil, err
		}
		return binance.New(config, credential), nil
	})
	okxExchange := cryptoExchange(exchange.ExchangeOKX)
	registry.Register(okxExchange, func(
		config exchange.AccountConfig,
		credential exchange.Credential,
	) (execution.ExecutionAdapter, error) {
		if err := requireAPIKey(config, credential); err != nil {
			return nil, err
		}
		if config.ExecutionMode == exchange.ExecutionModeLive &&
			strings.TrimSpace(credential.Passphrase) == "" {
			return nil, errors.New("OKX live account requires passphrase")
		}
		return okx.New(config, credential), nil
	})
}

// 模拟盘用空凭据绑定公共行情，只有实盘才要求密钥。
func requireAPIKey(config exchange.AccountConfig, credential exchange.Credential) error {
	if config.ExecutionMode == exchange.ExecutionModeLive &&
		(strings.TrimSpace(credential.APIKey) == "" || strings.TrimSpace(credential.APISecret) == "") {
		return errors.New("live account requires credential")
	}
	return nil
}

func exchangeCredential(
	ctx context.Context,
	secrets accountapp.SecretSource,
	exchangeName exchange.Exchange,
	secretID string,
) (exchange.Credential, error) {
	value, err := secrets.GetExchangeSecret(ctx, secretID)
	if err != nil {
		return exchange.Credential{}, err
	}
	if value.SecretID != secretID ||
		value.Exchange != exchangeName ||
		value.Category != "exchange" ||
		value.Status != "active" {
		return exchange.Credential{}, fmt.Errorf(
			"trade bootstrap: Exchange credential %q metadata mismatch",
			secretID,
		)
	}
	extra := map[string]string{}
	if strings.TrimSpace(value.ExtraConfig) != "" {
		if err := json.Unmarshal([]byte(value.ExtraConfig), &extra); err != nil {
			return exchange.Credential{}, fmt.Errorf(
				"trade bootstrap: decode credential extra config: %w",
				err,
			)
		}
	}
	return exchange.Credential{
		APIKey: value.KeyID, APISecret: value.SecretValue,
		Passphrase: extra["passphrase"], Extra: extra,
	}, nil
}

type accountSyncer struct {
	service *accountsync.Service
}

func (s accountSyncer) SyncAccount(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccount(ctx, accountID)
	return err
}

func (s accountSyncer) SyncAccountLight(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccountLight(ctx, accountID)
	return err
}

func (s accountSyncer) ConfirmCancel(ctx context.Context, spaceID, orderID string) error {
	return s.service.ConfirmCancel(ctx, spaceID, orderID)
}

type instrumentSource struct {
	store *store.Store
}

func (s instrumentSource) GetInstrument(
	ctx context.Context,
	exchangeName exchange.Exchange,
	market exchange.MarketType,
	symbol string,
) (exchange.Instrument, error) {
	// Order specs carry the canonical InstrumentID. Resolve that identity and
	// retain the native symbol only at the adapter boundary.
	record, err := s.store.GetInstrumentByIDScoped(ctx, symbol, string(exchangeName), string(market))
	if err != nil {
		return exchange.Instrument{}, err
	}
	return instrumentFromRecord(exchangeName, market, record), nil
}

func (s instrumentSource) GetInstrumentForAccount(
	ctx context.Context,
	tradingAccountID string,
	exchangeName exchange.Exchange,
	market exchange.MarketType,
	symbol string,
) (exchange.Instrument, error) {
	account, err := s.store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return exchange.Instrument{}, err
	}
	record, err := s.store.GetInstrumentByIDForAccount(ctx, account.SpaceID, tradingAccountID, symbol)
	if err != nil {
		return exchange.Instrument{}, err
	}
	return instrumentFromRecord(exchangeName, market, record), nil
}

func instrumentFromRecord(exchangeName exchange.Exchange, market exchange.MarketType, record store.InstrumentRecord) exchange.Instrument {
	return exchange.Instrument{
		Exchange: exchangeName, MarketType: market, ExchangeSymbol: record.ExchangeSymbol,
		InstrumentID: record.InstrumentID, BaseAsset: record.BaseAsset,
		QuoteAsset: record.QuoteAsset, SettlementAsset: record.SettlementAsset,
		Linear: record.Linear, ContractValue: decimal(record.ContractValue),
		ContractValueAsset:   record.ContractValueAsset,
		ExchangeQuantityStep: decimal(record.ExchangeQuantityStep),
		MinExchangeQuantity:  decimal(record.MinExchangeQuantity),
		PriceTick:            decimal(record.PriceTick), MinNotional: decimal(record.MinNotional),
		Status: record.Status, ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}
}

type positionSource struct {
	store *store.Store
}

// GetPosition 按内部标的 ID 取持仓；标的 ID 自带市场，现货和合约同名的交易所代码不会串。
func (s positionSource) GetPosition(
	ctx context.Context,
	tradingAccountID string,
	instrumentID string,
) (exchange.Position, error) {
	return s.GetPositionForAccount(ctx, tradingAccountID, instrumentID)
}

func (s positionSource) GetPositionForAccount(
	ctx context.Context,
	tradingAccountID string,
	symbol string,
) (exchange.Position, error) {
	account, err := s.store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return exchange.Position{}, err
	}
	instrument, err := s.store.GetInstrumentByIDForAccount(ctx, account.SpaceID, tradingAccountID, symbol)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return exchange.Position{TradingAccountID: tradingAccountID, InstrumentID: symbol, ExchangeSymbol: symbol, PositionSide: exchange.PositionSideNet}, nil
		}
		return exchange.Position{}, err
	}
	key := exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol)
	record, found, err := s.store.GetPosition(ctx, account.SpaceID, tradingAccountID, key, string(exchange.PositionSideNet))
	if err != nil {
		return exchange.Position{}, err
	}
	if !found {
		return exchange.Position{TradingAccountID: tradingAccountID, Market: key.Market, InstrumentID: symbol, ExchangeSymbol: key.Symbol, PositionSide: exchange.PositionSideNet}, nil
	}
	return exchange.Position{
		TradingAccountID: tradingAccountID, Market: exchange.MarketType(record.MarketType), InstrumentID: record.InstrumentID,
		ExchangeSymbol: record.ExchangeSymbol,
		PositionSide:   exchange.PositionSide(record.PositionSide), SignedQuantity: decimal(record.SignedQuantity),
		EntryPrice: decimal(record.EntryPrice), MarkPrice: decimal(record.MarkPrice), Leverage: decimal(record.Leverage),
		MarginMode: exchange.MarginMode(record.MarginMode), UsedMargin: decimal(record.UsedMargin),
		LiquidationPrice: decimal(record.LiquidationPrice), UnrealizedPnL: decimal(record.UnrealizedPnL),
		RealizedPnL: decimal(record.RealizedPnL), ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}, nil
}

func marketTypes(markets []string) []exchange.MarketType {
	result := make([]exchange.MarketType, len(markets))
	for i, market := range markets {
		result[i] = exchange.MarketType(market)
	}
	return result
}

func decimal(value string) shared.Decimal {
	if strings.TrimSpace(value) == "" {
		return shared.Zero()
	}
	parsed, err := shared.ParseDecimal(value)
	if err != nil {
		return shared.Zero()
	}
	return parsed
}

func registerMetricsReporter(serverInstance *server.Server) *report.ModuleMetrics {
	if serverInstance == nil {
		return nil
	}
	moduleMetrics, err := report.NewModuleMetrics(
		prometheus.DefaultRegisterer,
		"trade",
		report.HealthCheckIDsForModule("trade"),
	)
	if err != nil {
		log.Warnf("trade module metrics disabled: %v", err)
		return nil
	}
	handler, err := report.NewHandler(report.DefaultConfig("trade", "trade"))
	if err != nil {
		log.Warnf("trade metrics reporter disabled: %v", err)
		return moduleMetrics
	}
	service := serverInstance.Service("trpc.moox.trade.metrics.timer")
	if service == nil {
		log.Warn("trade metrics timer service is not configured")
		return moduleMetrics
	}
	timer.RegisterHandlerService(
		service,
		handler.Handle,
	)
	return moduleMetrics
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

func registerHealth(
	runtimeCtx context.Context,
	databasePath string,
	serverInstance *server.Server,
	tradeStore *store.Store,
	manager *traderuntime.Manager,
	targetWorker *traderuntime.TargetWorker,
	operatorWorker *traderuntime.OperatorWorker,
	paperMatcherWorker *traderuntime.PaperMatcherWorker,
	paperMatcher *executionpaper.Matcher,
	eventBusEnabled bool,
	eventBus *jetstream.Client,
	targetConsumerReady *atomic.Bool,
	lifeGate *lifecycleGate,
	syncAccount func(ctx context.Context, accountID string) error,
) error {
	state := health.New("trade", "trade", "", "")
	readiness := health.Readiness{
		DatabaseReady: func(ctx context.Context) error {
			return tradeStore.Ping(ctx)
		},
		EventBusEnabled: eventBusEnabled,
		EventBusReady: func() bool {
			return eventBus != nil && eventBus.Ready() &&
				targetConsumerReady != nil && targetConsumerReady.Load()
		},
		Sessions:           manager,
		TargetWorker:       targetWorker.Snapshot,
		OperatorWorker:     operatorWorker.Snapshot,
		PaperMatcherWorker: paperMatcherWorker.State,
		PaperAccountErrors: paperMatcher.AccountErrors,
	}
	life, err := newLifecycle(databasePath, readiness, targetWorker.Quiesce, tradeCatchup(tradeStore, targetWorker, time.Now), tradeResetSpec(tradeStore, databasePath, syncAccount))
	if err != nil {
		return fmt.Errorf("trade lifecycle state failed to open: %w", err)
	}
	lifeGate.Bind(life)
	snapshot := health.SnapshotFunc(
		state,
		readiness,
		"trade",
		"trade",
		"",
		"",
		startedAt,
	)
	state.SnapshotFunc = func(ctx context.Context) healthz.Response {
		response := snapshot(ctx)
		if response.Details == nil {
			response.Details = map[string]any{}
		}
		response.Details["lifecycle"] = life.Compact()
		return response
	}
	go func() {
		life.Recover(runtimeCtx)
		life.Run(runtimeCtx, lifecycleRefreshEvery)
	}()
	if err := health.Register(
		serverInstance.Service("trpc.moox.trade.Health"),
		state,
		lifecycleRoutes(life, tradeStore)...,
	); err != nil {
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
