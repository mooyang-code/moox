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
	equitySampler := traderuntime.NewEquitySampler(&equityapp.Service{Store: tradeStore, Adapters: manager})
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
			return accountErr
		}
		return tradeStore.Transaction(refreshCtx, func(tx *store.Tx) error {
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
	}
	paperMatcher.DecideContext = func(ctx context.Context, candidate store.OrderRecord) (executionpaper.Decision, error) {
		adapter, adapterErr := manager.Adapter(candidate.TradingAccountID)
		if adapterErr != nil {
			return executionpaper.Decision{}, adapterErr
		}
		priceSource, hasReferencePrice := adapter.(execution.ReferencePriceSource)
		marketDataSource, hasMarketData := adapter.(execution.MarketDataSource)
		if !hasReferencePrice && !hasMarketData {
			return executionpaper.Decision{}, errors.New("paper reference quote unavailable")
		}
		paperConfig, configErr := tradeStore.GetPaperAccountConfig(ctx, candidate.SpaceID, candidate.TradingAccountID)
		if configErr != nil {
			return executionpaper.Decision{}, configErr
		}
		slippage := shared.Zero()
		if candidate.OrderType == string(exchange.OrderTypeMarket) && candidate.PaperExecutionPrice == nil {
			if paperConfig.SlippageBPS != "" {
				parsed, parseErr := shared.ParseDecimal(paperConfig.SlippageBPS)
				if parseErr != nil || parsed.IsNegative() || parsed.Cmp(shared.MustDecimal("10000")) >= 0 {
					return executionpaper.Decision{}, fmt.Errorf("paper: invalid slippage bps %q", paperConfig.SlippageBPS)
				}
				slippage = parsed
			}
		}
		price := shared.Zero()
		if candidate.OrderType == string(exchange.OrderTypeMarket) && candidate.PaperExecutionPrice != nil {
			parsed, parseErr := shared.ParseDecimal(*candidate.PaperExecutionPrice)
			if parseErr == nil && parsed.Cmp(shared.Zero()) > 0 {
				price = parsed
			}
		}
		if candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.FirstMatchPending {
			if parsed, parseErr := shared.ParseDecimal(candidate.ReferencePrice); parseErr == nil && parsed.Cmp(shared.Zero()) > 0 {
				price = parsed
			}
		}
		if price.Cmp(shared.Zero()) <= 0 {
			var quoteErr error
			if hasMarketData {
				quote, marketErr := marketDataSource.GetQuote(ctx, shared.ExchangeSymbol(candidate.ExchangeSymbol))
				quoteErr = marketErr
				if quoteErr == nil && !executionpaper.QuoteFresh(quote, time.Now().UTC(), 10*time.Second) {
					quoteErr = errors.New("paper public quote is stale")
				}
				if quoteErr == nil {
					if candidate.OrderType == string(exchange.OrderTypeMarket) {
						price, quoteErr = executionpaper.MarketExecutionPrice(exchange.Side(candidate.Side), quote, slippage)
					} else {
						price, quoteErr = executionpaper.MarketExecutionPrice(exchange.Side(candidate.Side), quote, shared.Zero())
					}
				}
			} else {
				quote, referenceErr := priceSource.GetReferencePrice(ctx, candidate.ExchangeSymbol)
				quoteErr = referenceErr
				if quoteErr == nil {
					price = quote.Price
					if quote.Price.Cmp(shared.Zero()) <= 0 ||
						(!quote.UpdatedAt.IsZero() && time.Since(quote.UpdatedAt) > 10*time.Second) {
						quoteErr = errors.New("paper reference quote is stale or empty")
					}
				}
			}
			if quoteErr != nil || price.Cmp(shared.Zero()) <= 0 {
				if candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.TimeInForce == string(exchange.FillPolicyGTC) {
					return executionpaper.Decision{Rest: true}, nil
				}
				return executionpaper.Decision{Cancel: true, Reason: "paper reference quote unavailable"}, nil
			}
		}
		if candidate.OrderType == string(exchange.OrderTypeMarket) && candidate.PaperExecutionPrice == nil && !hasMarketData {
			if slippage.Cmp(shared.Zero()) > 0 {
				factor := shared.MustDecimal("1").Add(slippage.Div(shared.MustDecimal("10000")))
				if candidate.Side == string(exchange.SideSell) {
					factor = shared.MustDecimal("1").Sub(slippage.Div(shared.MustDecimal("10000")))
				}
				price = price.Mul(factor)
			}
		}
		if candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.LimitPrice != nil {
			limit, parseErr := shared.ParseDecimal(*candidate.LimitPrice)
			if parseErr != nil {
				return executionpaper.Decision{Cancel: true, Reason: "paper limit price invalid"}, nil
			}
			if !executionpaper.LimitMarketable(exchange.Side(candidate.Side), limit, price) {
				if candidate.TimeInForce == string(exchange.FillPolicyGTC) {
					return executionpaper.Decision{Rest: true}, nil
				}
				return executionpaper.Decision{Cancel: true, Reason: "paper limit order is not marketable"}, nil
			}
		}
		fee := shared.Zero()
		feeAsset := candidate.ReservedAsset
		role := "TAKER"
		if candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.TimeInForce == string(exchange.FillPolicyGTC) && !candidate.FirstMatchPending {
			role = "MAKER"
		}
		if paperConfig.TakerFeeRate == "" {
			paperConfig.TakerFeeRate = "0"
		}
		feeRateRaw := paperConfig.TakerFeeRate
		if role == "MAKER" && paperConfig.MakerFeeRate != "" {
			feeRateRaw = paperConfig.MakerFeeRate
		}
		feeRate, feeErr := shared.ParseDecimal(feeRateRaw)
		if feeErr != nil || feeRate.IsNegative() {
			return executionpaper.Decision{}, fmt.Errorf("paper: invalid fee rate %q", feeRateRaw)
		}
		fee = price.Mul(shared.MustDecimal(candidate.Quantity)).Mul(feeRate)
		realizedPnL := shared.Zero()
		account, accountErr := tradeStore.GetTradingAccountByID(ctx, candidate.TradingAccountID)
		if accountErr != nil {
			return executionpaper.Decision{}, accountErr
		}
		if account.SettlementAsset != "" {
			feeAsset = account.SettlementAsset
		}
		if !candidate.ReduceOnly {
			snapshot, snapshotErr := adapter.GetAccountSnapshot(ctx)
			if snapshotErr != nil {
				return executionpaper.Decision{}, snapshotErr
			}
			if !paperReservationSufficient(candidate, account, snapshot, price, fee) {
				return executionpaper.Decision{Cancel: true, Reason: "paper reservation insufficient at match"}, nil
			}
		}
		if account.MarketType == string(exchange.MarketTypeSwap) {
			position, found, positionErr := tradeStore.GetPosition(ctx, account.SpaceID, account.TradingAccountID, candidate.ExchangeSymbol, string(exchange.PositionSideNet))
			if positionErr != nil {
				return executionpaper.Decision{}, positionErr
			}
			if found {
				positionQty := decimal(position.SignedQuantity)
				closeQty := decimal(candidate.Quantity)
				if positionQty.Abs().Cmp(closeQty) < 0 {
					closeQty = positionQty.Abs()
				}
				if !positionQty.IsZero() && !closeQty.IsZero() && ((positionQty.Cmp(shared.Zero()) > 0 && candidate.Side == string(exchange.SideSell)) || (positionQty.Cmp(shared.Zero()) < 0 && candidate.Side == string(exchange.SideBuy))) {
					direction := shared.MustDecimal("1")
					if positionQty.IsNegative() {
						direction = direction.Neg()
					}
					realizedPnL = price.Sub(decimal(position.EntryPrice)).Mul(closeQty).Mul(direction)
				}
			}
		}
		return executionpaper.Decision{Fill: exchange.Fill{
			ExchangeTradeID: candidate.TradingAccountID + ":" + candidate.ClientOrderID,
			ExchangeOrderID: candidate.ExchangeOrderID, ClientOrderID: candidate.ClientOrderID,
			ExchangeSymbol: candidate.ExchangeSymbol, Symbol: candidate.ExchangeSymbol,
			Side: exchange.Side(candidate.Side), PositionSide: exchange.PositionSide(candidate.PositionSide),
			Quantity: decimal(candidate.Quantity), Price: price, Fee: fee, RealizedPnL: realizedPnL,
			FeeAsset: feeAsset, SettlementAsset: feeAsset, LiquidityRole: role,
			TradedAt: time.Now().UTC(),
		}}, nil
	}
	paperMatcherWorker := traderuntime.NewPaperMatcherWorker(paperMatcher, time.Second)
	orderService.Syncer = accountSyncer{service: syncService}
	targetExecutor := &targetapp.Executor{
		Store: tradeStore, Orders: orderService,
		Prices:           targetapp.ExchangePriceSource{Adapters: manager},
		MaxChildNotional: shared.MustDecimal("100000"),
	}
	targetGate := &sync.Mutex{}
	targetWorker := &traderuntime.TargetWorker{
		Store: tradeStore, Executor: targetExecutor, Interval: time.Second,
		Gate: targetGate, Metrics: tradeStore.ModuleMetrics(),
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
			PaperMatcherReady: paperMatcherWorker.Ready,
			OnReady:           equitySampler.Enqueue,
			SyncInterval:      30 * time.Second,
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
				Store: tradeStore, Wake: targetWorker.Wake,
				SetReady: targetConsumerReady.Store, Gate: targetGate,
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
						InstrumentID:     command.Symbol,
						Type:             command.OrderType, FillPolicy: command.FillPolicy,
						Side: command.Side, PositionSide: command.PositionSide,
						Quantity: command.Quantity, LimitPrice: command.LimitPrice,
						Reason: command.Reason,
					},
				)
				return result.Action, result.Order, placeErr
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
	// Order specs carry the canonical InstrumentID. During the green-field
	// cutover callers may still pass an exchange-native symbol, so resolve the
	// canonical identity first and retain the native symbol only at the adapter
	// boundary.
	record, err := s.store.GetInstrumentByIDScoped(ctx, symbol, string(exchangeName), string(market))
	if err != nil {
		record, err = s.store.GetInstrument(ctx, string(exchangeName), string(market), symbol)
	}
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
		record, err = s.store.GetInstrumentInEnvironment(ctx, string(exchangeName), string(account.Environment), string(market), symbol)
	}
	if err != nil {
		return exchange.Instrument{}, err
	}
	return instrumentFromRecord(exchangeName, market, record), nil
}

func instrumentFromRecord(exchangeName exchange.Exchange, market exchange.MarketType, record store.InstrumentRecord) exchange.Instrument {
	return exchange.Instrument{
		Exchange: exchangeName, MarketType: market, ExchangeSymbol: record.ExchangeSymbol, Symbol: record.ExchangeSymbol,
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
			TradingAccountID: tradingAccountID, ExchangeSymbol: symbol, Symbol: symbol,
			PositionSide: exchange.PositionSideNet,
		}, nil
	}
	return exchange.Position{
		TradingAccountID: tradingAccountID, ExchangeSymbol: symbol, Symbol: symbol,
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
		return exchange.Position{TradingAccountID: tradingAccountID, InstrumentID: symbol, ExchangeSymbol: symbol, Symbol: symbol, PositionSide: exchange.PositionSideNet}, nil
	}
	return exchange.Position{
		TradingAccountID: tradingAccountID, InstrumentID: record.InstrumentID,
		ExchangeSymbol: record.ExchangeSymbol, Symbol: record.ExchangeSymbol,
		PositionSide: exchange.PositionSide(record.PositionSide), SignedQuantity: decimal(record.SignedQuantity),
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

func paperReservationSufficient(
	candidate store.OrderRecord,
	account store.TradingAccountRecord,
	snapshot exchange.AccountSnapshot,
	price, fee shared.Decimal,
) bool {
	if account.MarketType != string(exchange.MarketTypeSpot) && account.MarketType != string(exchange.MarketTypeSwap) {
		return true
	}
	reserved, err := shared.ParseDecimal(candidate.RemainingReservedQuantity)
	if err != nil {
		return false
	}
	quantity, err := shared.ParseDecimal(candidate.Quantity)
	if err != nil {
		return false
	}
	required := price.Mul(quantity).Add(fee)
	if account.MarketType == string(exchange.MarketTypeSwap) {
		leverage := account.LeverageSettings[candidate.InstrumentID]
		if leverage == "" {
			leverage = account.LeverageSettings[candidate.ExchangeSymbol]
		}
		if leverage == "" {
			leverage = account.LeverageSettings[candidate.Symbol]
		}
		if leverage == "" && account.ExecutionMode == string(exchange.ExecutionModePaper) {
			leverage = account.LeverageSettings["*"]
		}
		parsedLeverage, leverageErr := shared.ParseDecimal(leverage)
		if leverageErr != nil || parsedLeverage.Cmp(shared.Zero()) <= 0 {
			return false
		}
		required = price.Mul(quantity).Div(parsedLeverage).Add(fee)
		return snapshot.AvailableFunds.Add(reserved).Cmp(required) >= 0
	}
	if candidate.Side != string(exchange.SideBuy) {
		return true
	}
	for _, balance := range snapshot.Balances {
		if balance.Asset != candidate.ReservedAsset {
			continue
		}
		return balance.Available.Add(reserved).Cmp(required) >= 0
	}
	return false
}

// paperOpeningReservationSufficient is retained as a focused helper for
// callers that only have the order reservation (the production matcher uses
// paperReservationSufficient with a fresh account snapshot).
func paperOpeningReservationSufficient(
	candidate store.OrderRecord,
	account store.TradingAccountRecord,
	price, fee shared.Decimal,
) bool {
	leverage := account.LeverageSettings[candidate.InstrumentID]
	if leverage == "" {
		leverage = account.LeverageSettings[candidate.ExchangeSymbol]
	}
	if leverage == "" {
		leverage = account.LeverageSettings[candidate.Symbol]
	}
	if leverage == "" && account.ExecutionMode == string(exchange.ExecutionModePaper) {
		leverage = account.LeverageSettings["*"]
	}
	parsedLeverage, leverageErr := shared.ParseDecimal(leverage)
	reserved, reservedErr := shared.ParseDecimal(candidate.RemainingReservedQuantity)
	quantity, quantityErr := shared.ParseDecimal(candidate.Quantity)
	if leverageErr != nil || reservedErr != nil || quantityErr != nil || parsedLeverage.Cmp(shared.Zero()) <= 0 {
		return false
	}
	return reserved.Cmp(price.Mul(quantity).Div(parsedLeverage).Add(fee)) >= 0
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
		TargetWorker:   targetWorker.Snapshot,
		OperatorWorker: operatorWorker.Snapshot,
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
