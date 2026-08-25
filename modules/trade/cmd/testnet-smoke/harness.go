package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/okx"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/rs/xid"
)

type liveHarness struct {
	options  smokeOptions
	identity smokeIdentity
	store    *store.Store
	manager  *traderuntime.Manager
	sync     *accountsync.Service
	orders   *orderapp.Service
	operator *operatorapp.Service
	logical  *logicalapp.Service
	probe    *privateOrderProbe

	cancel context.CancelFunc
	done   chan error
}

func openLiveHarness(
	ctx context.Context,
	options smokeOptions,
	credential exchange.Credential,
) (*liveHarness, error) {
	if options.Environment != exchange.AccountEnvironmentTestnet {
		return nil, errors.New("testnet smoke refuses non-TESTNET environment")
	}
	database, err := store.Open(options.Database)
	if err != nil {
		return nil, err
	}
	identity, err := seedSmokeStore(ctx, database, options)
	if err != nil {
		database.Close()
		return nil, err
	}
	manager := &traderuntime.Manager{
		Accounts: database, PollInterval: 250 * time.Millisecond,
		RetryMin: 250 * time.Millisecond, RetryMax: 2 * time.Second,
	}
	accounts := &accountapp.Service{
		Store:              accountapp.Repository{Store: database},
		SessionState:       manager,
		LiveTradingEnabled: false,
	}
	orders := &orderapp.Service{
		Store: database, Adapters: manager,
		UnknownLookupWindow: 5 * time.Second,
		Validator: orderapp.Validator{
			Accounts: accounts, Instruments: smokeInstrumentSource{store: database},
			Positions:        smokePositionSource{store: database},
			MaxReferenceAge:  30 * time.Second,
			MaxChildNotional: options.MaxNotional,
			MaxLeverage:      shared.MustDecimal("20"),
			FeeBufferRate:    shared.MustDecimal("0.002"),
		},
	}
	syncService := &accountsync.Service{
		Store: database, Adapters: manager, SessionState: manager,
		Fills: &consumer.Reducer{Store: database}, Orders: orders,
	}
	probe := newPrivateOrderProbe()
	orders.Syncer = smokeSyncer{service: syncService}
	manager.NewSession = func(record store.TradingAccountRecord) (traderuntime.ManagedSession, error) {
		if record.Environment != string(exchange.AccountEnvironmentTestnet) ||
			record.ExecutionMode != string(exchange.ExecutionModeLive) ||
			record.MarketType != string(exchange.MarketTypeSpot) ||
			record.Exchange != string(options.Exchange) {
			return nil, errors.New("testnet smoke session configuration escaped TESTNET LIVE SPOT")
		}
		config := exchange.AccountConfig{
			TradingAccountID: record.TradingAccountID,
			Exchange:         exchange.Exchange(record.Exchange),
			MarketType:       exchange.MarketType(record.MarketType),
			ExecutionMode:    exchange.ExecutionMode(record.ExecutionMode),
			Environment:      exchange.AccountEnvironmentTestnet,
			SettlementAsset:  record.SettlementAsset,
		}
		var adapter exchange.Adapter
		switch options.Exchange {
		case exchange.ExchangeBinance:
			adapter = binance.New(config, credential)
		case exchange.ExchangeOKX:
			adapter = okx.New(config, credential)
		default:
			return nil, errors.New("unsupported testnet Exchange")
		}
		adapter = probingAdapter{Adapter: adapter, probe: probe}
		return &traderuntime.ExchangeSession{
			Account: record, Adapter: adapter, Sync: syncService,
			SyncInterval: 2 * time.Second,
		}, nil
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(runtimeCtx)
	}()
	harness := &liveHarness{
		options: options, identity: identity, store: database,
		manager: manager, sync: syncService, orders: orders,
		operator: &operatorapp.Service{
			Store: database, Orders: orders, Syncer: smokeSyncer{service: syncService},
			Prices: targetapp.ExchangePriceSource{Adapters: manager},
		},
		logical: &logicalapp.Service{
			Store: database, Syncer: smokeSyncer{service: syncService},
			MaxSnapshotAge: 2 * time.Minute,
		},
		probe:  probe,
		cancel: cancel, done: done,
	}
	return harness, nil
}

func (h *liveHarness) Close() error {
	if h == nil {
		return nil
	}
	h.cancel()
	var runErr error
	select {
	case runErr = <-h.done:
		if errors.Is(runErr, context.Canceled) {
			runErr = nil
		}
	case <-time.After(10 * time.Second):
		runErr = errors.New("testnet smoke runtime did not stop")
	}
	return errors.Join(runErr, h.store.Close())
}

func (h *liveHarness) waitReady(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := h.manager.Snapshot()
		if snapshot.Reconciled && snapshot.Enabled == 1 && snapshot.Ready == 1 {
			readiness, err := h.logical.Readiness(
				ctx,
				smokeSpaceID,
				h.identity.LogicalAccountID,
			)
			if err == nil && readiness.Ready {
				return nil
			}
			if err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"wait testnet Manager/LogicalAccount ready: %w; snapshot=%+v",
				ctx.Err(),
				snapshot,
			)
		case <-ticker.C:
		}
	}
}

func (h *liveHarness) waitSessionReady(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := h.manager.Snapshot()
		if snapshot.Reconciled && snapshot.Enabled == 1 && snapshot.Ready == 1 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"wait testnet Manager session ready: %w; snapshot=%+v",
				ctx.Err(),
				snapshot,
			)
		case <-ticker.C:
		}
	}
}

func (h *liveHarness) adapter() (exchange.Adapter, error) {
	return h.manager.Adapter(h.identity.AccountID)
}

func runSubmitPhase(
	ctx context.Context,
	options smokeOptions,
	credential exchange.Credential,
) (err error) {
	harness, err := openLiveHarness(ctx, options, credential)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, harness.Close())
	}()
	if err := harness.waitReady(ctx); err != nil {
		return err
	}
	if _, err := harness.sync.SyncAccount(ctx, harness.identity.AccountID); err != nil {
		return fmt.Errorf("fresh account sync: %w", err)
	}
	instrument, err := harness.store.GetInstrument(
		ctx,
		string(options.Exchange),
		string(exchange.MarketTypeSpot),
		options.Symbol,
	)
	if err != nil {
		return err
	}
	adapter, err := harness.adapter()
	if err != nil {
		return err
	}
	priceSource, ok := adapter.(exchange.ReferencePriceSource)
	if !ok {
		return errors.New("testnet adapter has no reference price source")
	}
	quote, err := priceSource.GetReferencePrice(ctx, options.Symbol)
	if err != nil {
		return err
	}
	plan, err := planPassiveBuy(
		quote.Price,
		mustDecimal(instrument.PriceTick),
		mustDecimal(instrument.ExchangeQuantityStep),
		mustDecimal(instrument.MinExchangeQuantity),
		mustDecimal(instrument.MinNotional),
		options.MaxNotional,
	)
	if err != nil {
		return err
	}
	account, err := harness.store.GetTradingAccountByID(ctx, harness.identity.AccountID)
	if err != nil {
		return err
	}
	baseline, err := snapshotAssetTotal(account.Snapshot, instrument.BaseAsset)
	if err != nil {
		return err
	}
	clientOrderID := xid.New().String()
	result, err := harness.operator.PlaceManualOrder(
		ctx,
		operatorapp.ManualOrderCommand{
			SpaceID: smokeSpaceID, ActionID: xid.New().String(),
			TradingAccountID: harness.identity.AccountID,
			ClientOrderID:    clientOrderID, InstrumentID: options.Symbol,
			Type: exchange.OrderTypeLimit, FillPolicy: exchange.FillPolicyGTC,
			Side: exchange.SideBuy, Quantity: plan.Quantity, LimitPrice: &plan.Price,
			Reason: "real testnet smoke submit",
		},
	)
	if err != nil {
		if local, getErr := harness.store.GetOrderByClientID(
			ctx,
			smokeSpaceID,
			harness.identity.AccountID,
			clientOrderID,
		); getErr == nil {
			state := smokeState{
				Version: 1, Exchange: options.Exchange,
				Environment: exchange.AccountEnvironmentTestnet,
				SpaceID:     smokeSpaceID, AccountID: harness.identity.AccountID,
				LogicalAccountID: harness.identity.LogicalAccountID,
				Symbol:           options.Symbol, BaseAsset: instrument.BaseAsset,
				BaselineBaseTotal: baseline.String(),
				ClientOrderID:     clientOrderID, OrderID: local.OrderID,
				ExchangeOrderID: local.ExchangeOrderID,
			}
			if stateErr := writeState(options.State, state); stateErr != nil {
				return errors.Join(err, stateErr)
			}
		}
		return fmt.Errorf("submit passive testnet order: %w", err)
	}
	if result.Order.ExchangeOrderID == "" {
		return errors.New("Exchange accepted order without Exchange order ID")
	}
	fmt.Printf("%s PASS submit client_order_id=%s exchange_order_id=%s\n",
		options.Exchange, clientOrderID, result.Order.ExchangeOrderID)
	state := smokeState{
		Version: 1, Exchange: options.Exchange,
		Environment: exchange.AccountEnvironmentTestnet,
		SpaceID:     smokeSpaceID, AccountID: harness.identity.AccountID,
		LogicalAccountID: harness.identity.LogicalAccountID,
		Symbol:           options.Symbol, BaseAsset: instrument.BaseAsset,
		BaselineBaseTotal: baseline.String(),
		ClientOrderID:     clientOrderID, OrderID: result.Order.OrderID,
		ExchangeOrderID: result.Order.ExchangeOrderID,
	}
	if err := writeState(options.State, state); err != nil {
		return err
	}
	queried, err := adapter.GetOrder(ctx, options.Symbol, clientOrderID)
	if err != nil {
		return fmt.Errorf("query accepted order by client ID: %w", err)
	}
	if err := validateQueriedOrder(state, queried); err != nil {
		return err
	}
	fmt.Printf("%s PASS query state=%s\n", options.Exchange, queried.Status)
	if err := harness.probe.wait(ctx, clientOrderID); err != nil {
		return fmt.Errorf(
			"wait private order event for %s: %w",
			clientOrderID,
			err,
		)
	}
	fmt.Printf("%s PASS stream client_order_id=%s\n", options.Exchange, clientOrderID)
	if _, err := harness.sync.SyncAccount(ctx, harness.identity.AccountID); err != nil {
		return fmt.Errorf("post-submit account sync convergence: %w", err)
	}
	local, err := harness.store.GetOrder(ctx, smokeSpaceID, result.Order.OrderID)
	if err != nil {
		return err
	}
	if local.ClientOrderID != clientOrderID ||
		local.ExchangeOrderID != result.Order.ExchangeOrderID {
		return errors.New("post-submit local order identity did not converge")
	}
	fmt.Printf("%s PASS sync local_state=%s\n", options.Exchange, local.State)
	return nil
}

func runRecoverPhase(
	ctx context.Context,
	options smokeOptions,
	credential exchange.Credential,
) (err error) {
	state, err := readState(options.State, options.Exchange)
	if err != nil {
		return err
	}
	harness, err := openLiveHarness(ctx, options, credential)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, harness.Close())
	}()
	if harness.identity.AccountID != state.AccountID ||
		harness.identity.LogicalAccountID != state.LogicalAccountID {
		return errors.New("restart state account identity mismatch")
	}
	// LogicalAccount readiness intentionally blocks SUBMIT_UNKNOWN. Restart
	// recovery therefore waits only for the authenticated ExchangeSession first.
	if err := harness.waitSessionReady(ctx); err != nil {
		return err
	}
	local, err := harness.store.GetOrder(ctx, smokeSpaceID, state.OrderID)
	if err != nil {
		return fmt.Errorf("load order after restart: %w", err)
	}
	if local.ClientOrderID != state.ClientOrderID ||
		(state.ExchangeOrderID != "" &&
			local.ExchangeOrderID != state.ExchangeOrderID) {
		return errors.New("persisted local order identity changed across restart")
	}
	local, err = recoverPersistedOrder(ctx, harness, state)
	if err != nil {
		return err
	}
	adapter, err := harness.adapter()
	if err != nil {
		return err
	}
	queried, err := adapter.GetOrder(ctx, state.Symbol, state.ClientOrderID)
	if err != nil {
		return fmt.Errorf("restart query by client ID: %w", err)
	}
	if err := validateQueriedOrder(state, queried); err != nil {
		return err
	}
	if state.ExchangeOrderID == "" {
		if strings.TrimSpace(queried.ExchangeOrderID) == "" {
			return errors.New("unknown submit recovery returned no Exchange order ID")
		}
		state.ExchangeOrderID = queried.ExchangeOrderID
		if err := writeState(options.State, state); err != nil {
			return err
		}
	}
	fmt.Printf("%s PASS restart state=%s exchange_order_id=%s\n",
		options.Exchange, queried.Status, queried.ExchangeOrderID)
	if _, err := harness.sync.SyncAccount(ctx, state.AccountID); err != nil {
		return err
	}
	local, err = harness.store.GetOrder(ctx, smokeSpaceID, state.OrderID)
	if err != nil {
		return err
	}
	if !orderdomain.State(local.State).Terminal() {
		if _, err := harness.operator.CancelOrder(
			ctx,
			operatorapp.CancelOrderCommand{
				SpaceID: smokeSpaceID, ActionID: xid.New().String(),
				OrderID: state.OrderID, Reason: "real testnet smoke cleanup",
			},
		); err != nil {
			return fmt.Errorf("cancel testnet order: %w", err)
		}
	}
	if err := waitLocalTerminal(ctx, harness, state.OrderID); err != nil {
		return err
	}
	queried, err = adapter.GetOrder(ctx, state.Symbol, state.ClientOrderID)
	if err != nil {
		return fmt.Errorf("query terminal testnet order: %w", err)
	}
	if err := validateQueriedOrder(state, queried); err != nil {
		return err
	}
	if !terminalExchangeStatus(queried.Status) {
		return fmt.Errorf("Exchange order remains active after cleanup: %s", queried.Status)
	}
	remaining, err := cleanupTestExposure(ctx, harness, state)
	fmt.Printf(
		"%s remaining_position account=%s asset=%s quantity=%s\n",
		options.Exchange,
		state.AccountID,
		state.BaseAsset,
		remaining,
	)
	if err != nil {
		return err
	}
	if err := os.Remove(options.State); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("%s PASS cleanup terminal_state=%s\n", options.Exchange, queried.Status)
	return nil
}

func recoverPersistedOrder(
	ctx context.Context,
	harness *liveHarness,
	state smokeState,
) (store.OrderRecord, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := harness.store.GetOrder(ctx, smokeSpaceID, state.OrderID)
		if err != nil {
			return store.OrderRecord{}, err
		}
		if current.ClientOrderID != state.ClientOrderID ||
			(state.ExchangeOrderID != "" &&
				current.ExchangeOrderID != "" &&
				current.ExchangeOrderID != state.ExchangeOrderID) {
			return store.OrderRecord{}, errors.New(
				"restart recovery changed the persisted order identity",
			)
		}
		currentState := orderdomain.State(current.State)
		switch currentState {
		case orderdomain.Pending, orderdomain.Submitting, orderdomain.SubmitUnknown:
			if _, submitErr := harness.orders.Submit(
				ctx,
				smokeSpaceID,
				state.OrderID,
			); submitErr != nil {
				latest, getErr := harness.store.GetOrder(
					ctx,
					smokeSpaceID,
					state.OrderID,
				)
				if getErr != nil {
					return store.OrderRecord{}, errors.Join(submitErr, getErr)
				}
				if orderdomain.State(latest.State) == orderdomain.Rejected {
					return store.OrderRecord{}, fmt.Errorf(
						"state-aware restart recovery reached REJECTED: %w",
						submitErr,
					)
				}
				if latestState := orderdomain.State(latest.State); latestState != orderdomain.Submitting &&
					latestState != orderdomain.SubmitUnknown {
					return store.OrderRecord{}, fmt.Errorf(
						"state-aware restart recovery: %w",
						submitErr,
					)
				}
				// The REST outcome is uncertain, so retain the durable unknown
				// state and let the next iteration query by client order ID.
				break
			}
			latest, getErr := harness.store.GetOrder(
				ctx,
				smokeSpaceID,
				state.OrderID,
			)
			if getErr != nil {
				return store.OrderRecord{}, getErr
			}
			latestState := orderdomain.State(latest.State)
			if latestState != orderdomain.Pending &&
				latestState != orderdomain.Submitting &&
				latestState != orderdomain.SubmitUnknown {
				if latestState == orderdomain.Rejected {
					return store.OrderRecord{}, errors.New(
						"state-aware restart recovery reached REJECTED",
					)
				}
				return latest, nil
			}
			// A confirmed missing SUBMIT_UNKNOWN returns to PENDING. The next
			// iteration resubmits with the same client order ID.
		case orderdomain.Rejected:
			return store.OrderRecord{}, errors.New(
				"state-aware restart recovery found a rejected order",
			)
		default:
			return current, nil
		}
		select {
		case <-ctx.Done():
			return store.OrderRecord{}, fmt.Errorf(
				"wait state-aware restart recovery: %w",
				ctx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func waitLocalTerminal(
	ctx context.Context,
	harness *liveHarness,
	orderID string,
) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := harness.sync.SyncAccount(ctx, harness.identity.AccountID); err != nil {
			return err
		}
		current, err := harness.store.GetOrder(ctx, smokeSpaceID, orderID)
		if err != nil {
			return err
		}
		if orderdomain.State(current.State).Terminal() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait local order terminal: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func cleanupTestExposure(
	ctx context.Context,
	harness *liveHarness,
	state smokeState,
) (shared.Decimal, error) {
	if _, err := harness.sync.SyncAccount(ctx, state.AccountID); err != nil {
		return shared.Zero(), err
	}
	account, err := harness.store.GetTradingAccountByID(ctx, state.AccountID)
	if err != nil {
		return shared.Zero(), err
	}
	baseline := mustDecimal(state.BaselineBaseTotal)
	total, err := snapshotAssetTotal(account.Snapshot, state.BaseAsset)
	if err != nil {
		return shared.Zero(), err
	}
	delta := total.Sub(baseline)
	if delta.IsNegative() {
		return delta, errors.New("base balance fell below pre-smoke baseline; refusing to guess cleanup")
	}
	if delta.IsZero() {
		return shared.Zero(), nil
	}
	original, err := harness.store.GetOrder(ctx, smokeSpaceID, state.OrderID)
	if err != nil {
		return delta, err
	}
	filled := mustDecimal(original.FilledQuantity)
	if filled.IsZero() {
		return delta, errors.New("base balance changed without a recorded test fill; refusing cleanup")
	}
	instrument, err := harness.store.GetInstrument(
		ctx,
		string(state.Exchange),
		string(exchange.MarketTypeSpot),
		state.Symbol,
	)
	if err != nil {
		return delta, err
	}
	quantity := delta
	if quantity.Cmp(filled) > 0 {
		quantity = filled
	}
	step := mustDecimal(instrument.ExchangeQuantityStep)
	quantity = floorToStep(quantity, step)
	if quantity.Cmp(mustDecimal(instrument.MinExchangeQuantity)) < 0 {
		return delta, errors.New("test fill is below the Exchange cleanup minimum")
	}
	cleanup, err := harness.operator.PlaceManualOrder(
		ctx,
		operatorapp.ManualOrderCommand{
			SpaceID: smokeSpaceID, ActionID: xid.New().String(),
			TradingAccountID: state.AccountID, ClientOrderID: xid.New().String(),
			InstrumentID: state.Symbol, Type: exchange.OrderTypeMarket,
			Side: exchange.SideSell, Quantity: quantity,
			Reason: "real testnet smoke reverse cleanup",
		},
	)
	if err != nil {
		return delta, fmt.Errorf("reverse test fill: %w", err)
	}
	if err := waitLocalTerminal(ctx, harness, cleanup.Order.OrderID); err != nil {
		return delta, err
	}
	account, err = harness.store.GetTradingAccountByID(ctx, state.AccountID)
	if err != nil {
		return delta, err
	}
	total, err = snapshotAssetTotal(account.Snapshot, state.BaseAsset)
	if err != nil {
		return delta, err
	}
	remaining := total.Sub(baseline)
	if remaining.IsNegative() {
		return remaining, errors.New("reverse cleanup sold below pre-smoke baseline")
	}
	if remaining.Cmp(step) >= 0 {
		return remaining, errors.New("test-generated position remains after reverse cleanup")
	}
	return remaining, nil
}

func terminalExchangeStatus(status exchange.OrderStatus) bool {
	switch status {
	case exchange.OrderStatusFilled,
		exchange.OrderStatusCanceled,
		exchange.OrderStatusPartiallyCanceled,
		exchange.OrderStatusRejected,
		exchange.OrderStatusExpired:
		return true
	default:
		return false
	}
}

func snapshotAssetTotal(
	snapshot store.TradingAccountSnapshot,
	asset string,
) (shared.Decimal, error) {
	for _, balance := range snapshot.Balances {
		if strings.EqualFold(balance.Asset, asset) {
			return shared.ParseDecimal(balance.Total)
		}
	}
	return shared.Zero(), nil
}

type smokeSyncer struct {
	service *accountsync.Service
}

func (s smokeSyncer) SyncAccount(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccount(ctx, accountID)
	return err
}

type smokeInstrumentSource struct {
	store *store.Store
}

func (s smokeInstrumentSource) GetInstrument(
	ctx context.Context,
	exchangeName exchange.Exchange,
	market exchange.MarketType,
	symbol string,
) (exchange.Instrument, error) {
	record, err := s.store.GetInstrument(ctx, string(exchangeName), string(market), symbol)
	if err != nil {
		return exchange.Instrument{}, err
	}
	return exchange.Instrument{
		Exchange: exchangeName, MarketType: market, Symbol: record.Symbol,
		InstrumentID: record.InstrumentID, BaseAsset: record.BaseAsset,
		QuoteAsset: record.QuoteAsset, SettlementAsset: record.SettlementAsset,
		Linear: record.Linear, ContractValue: mustDecimal(record.ContractValue),
		ContractValueAsset:   record.ContractValueAsset,
		ExchangeQuantityStep: mustDecimal(record.ExchangeQuantityStep),
		MinExchangeQuantity:  mustDecimal(record.MinExchangeQuantity),
		PriceTick:            mustDecimal(record.PriceTick),
		MinNotional:          mustDecimal(record.MinNotional),
		Status:               record.Status, ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}, nil
}

type smokePositionSource struct {
	store *store.Store
}

func (s smokePositionSource) GetPosition(
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
			TradingAccountID: tradingAccountID,
			Symbol:           symbol, PositionSide: exchange.PositionSideNet,
		}, nil
	}
	return exchange.Position{
		TradingAccountID: tradingAccountID, Symbol: symbol,
		PositionSide:      exchange.PositionSide(record.PositionSide),
		SignedQuantity:    mustDecimal(record.SignedQuantity),
		EntryPrice:        mustDecimal(record.EntryPrice),
		MarkPrice:         mustDecimal(record.MarkPrice),
		Leverage:          mustDecimal(record.Leverage),
		MarginMode:        exchange.MarginMode(record.MarginMode),
		UsedMargin:        mustDecimal(record.UsedMargin),
		LiquidationPrice:  mustDecimal(record.LiquidationPrice),
		UnrealizedPnL:     mustDecimal(record.UnrealizedPnL),
		RealizedPnL:       mustDecimal(record.RealizedPnL),
		ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}, nil
}

func mustDecimal(raw string) shared.Decimal {
	if strings.TrimSpace(raw) == "" {
		return shared.Zero()
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		panic(err)
	}
	return value
}
