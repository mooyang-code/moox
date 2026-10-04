package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/okx"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	executionpaper "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/health"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradeobservability "github.com/mooyang-code/moox/modules/trade/internal/observability"
	tradeResolver "github.com/mooyang-code/moox/modules/trade/internal/resolver"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/modules/trade/internal/secretclient"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var startedAt = time.Now()

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

	secrets := secretclient.New(secretclient.Config{
		GatewayBaseURL: cfg.Admin.BaseURL,
		ServiceAuth: secretclient.ServiceAuthConfig{
			AccessKey:  cfg.Admin.ServiceAuth.AccessKey,
			SecretKey:  cfg.Admin.ServiceAuth.SecretKey,
			TargetNode: cfg.Admin.ServiceAuth.TargetNode,
			CAFile:     cfg.Admin.ServiceAuth.CAFile,
			ExpireSecs: cfg.Admin.ServiceAuth.ExpireSeconds,
		},
	})
	registry := execution.NewRegistry()
	registerBuiltins(registry)
	tradeStore.SetModuleMetrics(registerMetricsReporter(serverInstance))

	manager := &traderuntime.Manager{
		Accounts: tradeStore, PollInterval: 5 * time.Second,
		RetryMin: time.Second, RetryMax: 30 * time.Second,
	}
	accounts := &accountapp.Service{
		Store: accountapp.Repository{Store: tradeStore}, Secrets: secrets,
		SessionState: manager, LiveTradingEnabled: cfg.Runtime.LiveTradingEnabled,
	}
	orderService := &orderapp.Service{
		Store: tradeStore, Adapters: manager,
		Validator: orderapp.Validator{
			Accounts:         accounts,
			Instruments:      instrumentSource{store: tradeStore},
			Positions:        positionSource{store: tradeStore},
			MaxReferenceAge:  10 * time.Second,
			MaxChildNotional: shared.MustDecimal("100000"),
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
				paperSnapshotRecord(snapshot),
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
		Store: tradeStore, Orders: orderService,
		Prices:           targetapp.ExchangePriceSource{Adapters: manager},
		MaxChildNotional: shared.MustDecimal("100000"),
	}
	weightResolver := &targetapp.WeightResolver{
		Store:  tradeStore,
		Prices: targetapp.ExchangePriceSource{Adapters: manager},
		Equity: equityService,
	}
	targetWorker := &traderuntime.TargetWorker{
		Store: tradeStore, Executor: targetExecutor, Interval: time.Second,
		Metrics: tradeStore.ModuleMetrics(),
	}
	factsObserver := &accountsync.LogicalAccountFactsObserver{
		Store: tradeStore,
		Wake:  targetWorker.Wake,
	}
	syncService.Facts = factsObserver
	logicalAccounts := &logicalapp.Service{
		Store: tradeStore, Syncer: accountSyncer{service: syncService},
	}
	operatorService := &operatorapp.Service{
		Store: tradeStore, Orders: orderService,
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
			MarketType:       exchange.MarketType(record.MarketType),
			ExecutionMode:    exchange.ExecutionMode(record.ExecutionMode),
			Environment:      exchange.AccountEnvironment(record.Environment),
			SettlementAsset:  record.SettlementAsset,
			MarginMode:       exchange.MarginMode(record.MarginMode),
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
			marketData = publicMarketData(publicAdapter)
			if marketData == nil {
				return nil, errors.New("trade bootstrap: paper adapter does not provide public market data")
			}
			adapter = &executionpaper.Adapter{Account: record, Store: tradeStore, MarketData: marketData, Wake: paperMatcherWorker.Wake}
		} else {
			var bindErr error
			adapter, bindErr = registry.Bind(accountConfig, credential)
			if bindErr != nil {
				return nil, bindErr
			}
			marketData = publicMarketData(adapter)
			accountEvents, _ = adapter.(execution.AccountEventSource)
			if marketData == nil || accountEvents == nil {
				return nil, errors.New("trade bootstrap: live adapter does not provide execution ports")
			}
		}
		reservationPolicy := execution.ReservationPolicy(execution.LiveReservationPolicy{})
		if accountConfig.ExecutionMode == exchange.ExecutionModePaper {
			reservationPolicy = execution.PaperReservationPolicy{}
		}
		return &traderuntime.ExchangeSession{
			Account: record, Adapter: adapter, MarketData: marketData, AccountEvents: accountEvents,
			ReservationPolicy: reservationPolicy, Sync: syncService,
			PaperMatcherReady:     paperMatcherWorker.Ready,
			PaperAccountState:     paperMatcher.AccountState,
			PaperAccountRecovered: paperMatcher.RecoverAccount,
			OnReady:               equitySampler.Enqueue,
			SyncInterval:          30 * time.Second,
		}, nil
	}

	var dnsResolver *tradeResolver.Resolver
	if cfg.DNSResolver.Enabled {
		dnsMetrics, metricsErr := tradeResolver.NewMetrics(prometheus.DefaultRegisterer)
		if metricsErr != nil {
			return nil, fmt.Errorf("register Trade DNS resolver metrics: %w", metricsErr)
		}
		dnsResolver = tradeResolver.New(tradeResolver.Config{
			Domains:         cfg.DNSResolver.Domains,
			LookupTimeout:   time.Duration(cfg.DNSResolver.LookupTimeoutMS) * time.Millisecond,
			ProbeTimeout:    time.Duration(cfg.DNSResolver.ProbeTimeoutMS) * time.Millisecond,
			ProbePort:       cfg.DNSResolver.ProbePort,
			CacheTTL:        time.Duration(cfg.DNSResolver.CacheTTLSeconds) * time.Second,
			MaxIPsPerDomain: cfg.DNSResolver.MaxIPsPerDomain,
			Metrics:         dnsMetrics,
		})
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
	runWorker(factsObserver.Run)
	runWorker(targetWorker.Run)
	runWorker(operatorWorker.Run)
	runWorker(paperMatcherWorker.Run)
	runWorker(equitySampler.Run)
	if eventBus != nil {
		client := eventBus
		runWorker(func(workerCtx context.Context) error {
			return eventconsumer.RunTarget(workerCtx, eventconsumer.TargetOptions{
				Client: client, ConsumerName: cfg.EventBus.TargetConsumer,
				Store: tradeStore, WakeTarget: targetWorker.WakeTarget,
				SetReady:       targetConsumerReady.Store,
				WeightResolver: weightResolver,
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
		&rpc.DNSResolverServer{Resolver: dnsResolver},
		rpc.ConsoleOptions{
			Paper:              &papersimulation.Service{Store: tradeStore},
			LiveTradingEnabled: cfg.Runtime.LiveTradingEnabled,
			MatcherReady:       paperMatcherWorker.Ready,
			Holdings:           &rpc.HoldingQuery{Store: tradeStore, Adapters: manager},
		},
	)
	if err := registerHealth(
		serverInstance,
		tradeStore,
		manager,
		factsObserver,
		targetWorker,
		operatorWorker,
		paperMatcherWorker,
		paperMatcher,
		cfg.EventBus.Enabled,
		eventBus,
		&targetConsumerReady,
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
		_ = tradeStore.Close()
	})
	cleanupStore = false
	return serverInstance, nil
}

func registerBuiltins(registry *execution.Registry) {
	registry.Register(exchange.ExchangeBinance, func(
		config exchange.AccountConfig,
		credential exchange.Credential,
	) (execution.ExecutionAdapter, error) {
		return binance.New(config, credential), nil
	})
	registry.Register(exchange.ExchangeOKX, func(
		config exchange.AccountConfig,
		credential exchange.Credential,
	) (execution.ExecutionAdapter, error) {
		if config.ExecutionMode == exchange.ExecutionModeLive &&
			strings.TrimSpace(credential.Passphrase) == "" {
			return nil, errors.New("OKX live account requires passphrase")
		}
		return okx.New(config, credential), nil
	})
}

func publicMarketData(adapter execution.ExecutionAdapter) execution.MarketDataSource {
	source, _ := adapter.(execution.MarketDataSource)
	return source
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
	var extra struct {
		Passphrase string `json:"passphrase"`
	}
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
		Passphrase: extra.Passphrase,
	}, nil
}

type accountSyncer struct {
	service *accountsync.Service
}

func (s accountSyncer) SyncAccount(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccount(ctx, accountID)
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

func (s positionSource) GetPosition(
	ctx context.Context,
	tradingAccountID string,
	symbol string,
) (exchange.Position, error) {
	account, err := s.store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return exchange.Position{}, err
	}
	record, found, err := s.store.GetPosition(
		ctx,
		account.SpaceID,
		tradingAccountID,
		symbol,
		string(exchange.PositionSideNet),
	)
	if err != nil {
		return exchange.Position{}, err
	}
	if !found {
		return exchange.Position{
			TradingAccountID: tradingAccountID, ExchangeSymbol: symbol,
			PositionSide: exchange.PositionSideNet,
		}, nil
	}
	return exchange.Position{
		TradingAccountID: tradingAccountID, ExchangeSymbol: symbol,
		PositionSide:   exchange.PositionSide(record.PositionSide),
		SignedQuantity: decimal(record.SignedQuantity),
		EntryPrice:     decimal(record.EntryPrice), MarkPrice: decimal(record.MarkPrice),
		Leverage:          decimal(record.Leverage),
		MarginMode:        exchange.MarginMode(record.MarginMode),
		UsedMargin:        decimal(record.UsedMargin),
		LiquidationPrice:  decimal(record.LiquidationPrice),
		UnrealizedPnL:     decimal(record.UnrealizedPnL),
		RealizedPnL:       decimal(record.RealizedPnL),
		ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}, nil
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
	record, found, err := s.store.GetPosition(ctx, account.SpaceID, tradingAccountID, symbol, string(exchange.PositionSideNet))
	if err != nil {
		return exchange.Position{}, err
	}
	if !found {
		if instrument, instrumentErr := s.store.GetInstrumentByIDForAccount(ctx, account.SpaceID, tradingAccountID, symbol); instrumentErr == nil {
			record, found, err = s.store.GetPosition(ctx, account.SpaceID, tradingAccountID, instrument.ExchangeSymbol, string(exchange.PositionSideNet))
			if err != nil {
				return exchange.Position{}, err
			}
		}
	}
	if !found {
		return exchange.Position{TradingAccountID: tradingAccountID, InstrumentID: symbol, ExchangeSymbol: symbol, PositionSide: exchange.PositionSideNet}, nil
	}
	return exchange.Position{
		TradingAccountID: tradingAccountID, InstrumentID: record.InstrumentID,
		ExchangeSymbol: record.ExchangeSymbol,
		PositionSide:   exchange.PositionSide(record.PositionSide), SignedQuantity: decimal(record.SignedQuantity),
		EntryPrice: decimal(record.EntryPrice), MarkPrice: decimal(record.MarkPrice), Leverage: decimal(record.Leverage),
		MarginMode: exchange.MarginMode(record.MarginMode), UsedMargin: decimal(record.UsedMargin),
		LiquidationPrice: decimal(record.LiquidationPrice), UnrealizedPnL: decimal(record.UnrealizedPnL),
		RealizedPnL: decimal(record.RealizedPnL), ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}, nil
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

func paperSnapshotRecord(snapshot exchange.AccountSnapshot) store.TradingAccountSnapshot {
	balances := make([]store.AssetBalance, 0, len(snapshot.Balances))
	for _, balance := range snapshot.Balances {
		balances = append(balances, store.AssetBalance{
			Asset: balance.Asset, Available: balance.Available.String(), Locked: balance.Locked.String(), Total: balance.Total.String(),
		})
	}
	return store.TradingAccountSnapshot{
		Balances: balances, Equity: snapshot.Equity.String(), AvailableFunds: snapshot.AvailableFunds.String(),
		UsedMargin: snapshot.UsedMargin.String(), MaintenanceMargin: snapshot.MaintenanceMargin.String(),
		UnrealizedPnL: snapshot.UnrealizedPnL.String(), ExchangeUpdatedAt: snapshot.ExchangeUpdatedAt.UnixMilli(),
	}
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
	handler, err := report.NewHandler(report.DefaultConfig("trade", "moox_trade"))
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
	serverInstance *server.Server,
	tradeStore *store.Store,
	manager *traderuntime.Manager,
	factsObserver *accountsync.LogicalAccountFactsObserver,
	targetWorker *traderuntime.TargetWorker,
	operatorWorker *traderuntime.OperatorWorker,
	paperMatcherWorker *traderuntime.PaperMatcherWorker,
	paperMatcher *executionpaper.Matcher,
	eventBusEnabled bool,
	eventBus *jetstream.Client,
	targetConsumerReady *atomic.Bool,
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
		Sessions: manager,
		LogicalAccountWorker: func() (bool, string) {
			snapshot := factsObserver.Snapshot()
			return snapshot.Ready, snapshot.LastError
		},
		TargetWorker:       targetWorker.Snapshot,
		OperatorWorker:     operatorWorker.Snapshot,
		PaperMatcherWorker: paperMatcherWorker.State,
		PaperAccountErrors: paperMatcher.AccountErrors,
	}
	state.SnapshotFunc = health.SnapshotFunc(
		state,
		readiness,
		"trade",
		"trade",
		"",
		"",
		startedAt,
	)
	if err := health.Register(
		serverInstance.Service("trpc.moox.trade.Health"),
		state,
	); err != nil {
		return fmt.Errorf("trade health server failed to register: %w", err)
	}
	return nil
}
