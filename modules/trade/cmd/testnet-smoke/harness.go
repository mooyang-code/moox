package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/fillreduce"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/modules/trade/internal/wiring"
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
	registry := newSmokeRegistry()
	manager := &traderuntime.Manager{
		Accounts: database, PollInterval: 250 * time.Millisecond,
		RetryMin: 250 * time.Millisecond, RetryMax: 2 * time.Second,
	}
	accounts := &accountapp.Service{
		Store:              database,
		SessionState:       manager,
		Exchanges:          registry,
		LiveTradingEnabled: false,
	}
	// SymbolConfig 与生产一致：合约首次开仓前把全仓和账户配置的杠杆（冒烟固定 1 倍）设置到交易所。
	orders := &orderapp.Service{
		Store: database, Adapters: manager, SymbolConfig: manager,
		UnknownLookupWindow: 5 * time.Second,
		Validator: orderapp.Validator{
			Accounts:         accounts,
			Instruments:      wiring.InstrumentSource{Store: database},
			Positions:        wiring.PositionSource{Store: database},
			MaxReferenceAge:  30 * time.Second,
			MaxChildNotional: options.MaxNotional,
			MaxLeverage:      shared.MustDecimal("20"),
			FeeBufferRate:    shared.MustDecimal("0.002"),
			Quotes:           manager,
		},
	}
	syncService := &accountsync.Service{
		Store: database, Adapters: manager, SessionState: manager,
		Fills: &fillreduce.Reducer{Store: database}, Orders: orders,
	}
	probe := newPrivateOrderProbe()
	syncer := wiring.AccountSyncer{Service: syncService}
	orders.Syncer = syncer
	manager.NewSession = func(record store.TradingAccountRecord) (traderuntime.ManagedSession, error) {
		if record.Environment != string(exchange.AccountEnvironmentTestnet) ||
			record.ExecutionMode != string(exchange.ExecutionModeLive) ||
			!slices.Equal(record.Markets, smokeMarketStrings(options.Exchange, options.MarketType)) ||
			record.Exchange != string(options.Exchange) {
			return nil, errors.New("测试网冒烟会话配置偏离预期的 TESTNET LIVE 账户")
		}
		adapter, err := registry.Bind(smokeAccountConfig(record), credential)
		if err != nil {
			return nil, err
		}
		adapter = newProbingAdapter(adapter, probe)
		accountEvents := execution.AccountEventsOf(adapter)
		if accountEvents == nil {
			return nil, errors.New("测试网适配器未提供私有账户事件流")
		}
		return &traderuntime.ExchangeSession{
			Account: record, Adapter: adapter, MarketData: adapter, AccountEvents: accountEvents, Sync: syncService,
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
			Exchanges: registry,
			Store:     database, Orders: orders, Syncer: syncer,
			Prices: targetapp.ExchangePriceSource{Adapters: manager, Exchanges: registry},
		},
		logical: &logicalapp.Service{
			Store: database, Syncer: syncer,
			Prices:         targetapp.ExchangePriceSource{Adapters: manager, Exchanges: registry},
			MaxSnapshotAge: 2 * time.Minute,
		},
		probe:  probe,
		cancel: cancel, done: done,
	}
	return harness, nil
}

func newSmokeRegistry() *execution.Registry {
	registry := execution.NewRegistry()
	wiring.RegisterCryptoExchanges(registry)
	return registry
}

func smokeAccountConfig(record store.TradingAccountRecord) exchange.AccountConfig {
	return exchange.AccountConfig{
		TradingAccountID: record.TradingAccountID,
		Exchange:         exchange.Exchange(record.Exchange),
		Markets:          wiring.MarketTypes(record.Markets),
		ExecutionMode:    exchange.ExecutionMode(record.ExecutionMode),
		AccountMode:      exchange.AccountMode(record.AccountMode),
		Environment:      exchange.AccountEnvironment(record.Environment),
		SettlementAsset:  record.SettlementAsset,
		MarginMode:       exchange.MarginMode(record.MarginMode),
	}
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
				h.logical.Store,
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

func (h *liveHarness) adapter() (execution.ExecutionAdapter, error) {
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
	instrumentRecord, err := harness.store.GetInstrumentInEnvironment(
		ctx,
		string(options.Exchange), string(exchange.AccountEnvironmentTestnet),
		string(options.MarketType), options.ExchangeSymbol,
	)
	if err != nil {
		return err
	}
	instrument := wiring.InstrumentFromRecord(options.Exchange, options.MarketType, instrumentRecord)
	adapter, err := harness.adapter()
	if err != nil {
		return err
	}
	priceSource, ok := adapter.(execution.ReferencePriceSource)
	if !ok {
		return errors.New("testnet adapter has no reference price source")
	}
	quote, err := priceSource.GetReferencePrice(ctx, exchange.NewSymbolKey(options.MarketType, options.ExchangeSymbol))
	if err != nil {
		return err
	}
	plan, err := planPassiveBuy(
		quote.Price,
		instrument.PriceTick,
		instrument.ExchangeQuantityStep,
		instrument.MinExchangeQuantity,
		instrument.MinNotional,
		instrument.ContractValue,
		options.MaxNotional,
	)
	if err != nil {
		return err
	}
	baseline, err := harness.exposure(ctx, harness.identity.AccountID, instrumentRecord)
	if err != nil {
		return err
	}
	clientOrderID := xid.New().String()
	result, err := harness.operator.PlaceOrder(
		ctx,
		operatorapp.ManualOrderCommand{
			SpaceID: smokeSpaceID, ActionID: xid.New().String(), LogicalAccountID: harness.identity.LogicalAccountID,
			TradingAccountID: harness.identity.AccountID,
			ClientOrderID:    clientOrderID, InstrumentID: instrument.InstrumentID,
			Type: exchange.OrderTypeLimit, FillPolicy: exchange.FillPolicyGTC,
			Side: exchange.SideBuy, PositionSide: exchange.RuleFor(options.MarketType).PositionSide,
			Quantity: plan.Quantity, LimitPrice: &plan.Price,
			Reason: "real testnet smoke submit",
		},
	)
	if err != nil {
		if local, getErr := harness.store.GetOrderByClientID(
			ctx,
			smokeSpaceID,
			harness.identity.AccountID,
			instrument.MarketType,
			clientOrderID,
		); getErr == nil {
			state := smokeState{
				Version: 1, Exchange: options.Exchange,
				Environment: exchange.AccountEnvironmentTestnet, MarketType: options.MarketType,
				SpaceID: smokeSpaceID, AccountID: harness.identity.AccountID,
				LogicalAccountID: harness.identity.LogicalAccountID,
				InstrumentID:     instrument.InstrumentID, ExchangeSymbol: instrument.ExchangeSymbol,
				BaseAsset:         instrument.BaseAsset,
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
		Environment: exchange.AccountEnvironmentTestnet, MarketType: options.MarketType,
		SpaceID: smokeSpaceID, AccountID: harness.identity.AccountID,
		LogicalAccountID: harness.identity.LogicalAccountID,
		InstrumentID:     instrument.InstrumentID, ExchangeSymbol: instrument.ExchangeSymbol,
		BaseAsset:         instrument.BaseAsset,
		BaselineBaseTotal: baseline.String(),
		ClientOrderID:     clientOrderID, OrderID: result.Order.OrderID,
		ExchangeOrderID: result.Order.ExchangeOrderID,
	}
	if err := writeState(options.State, state); err != nil {
		return err
	}
	queried, err := adapter.GetOrder(ctx, exchange.NewSymbolKey(options.MarketType, options.ExchangeSymbol), clientOrderID)
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
	state, err := readState(options.State, options.Exchange, options.MarketType)
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
	queried, err := adapter.GetOrder(ctx, exchange.NewSymbolKey(state.MarketType, state.ExchangeSymbol), state.ClientOrderID)
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
	queried, err = adapter.GetOrder(ctx, exchange.NewSymbolKey(state.MarketType, state.ExchangeSymbol), state.ClientOrderID)
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
	instrumentRecord, err := harness.store.GetInstrumentByIDForAccount(
		ctx, smokeSpaceID, state.AccountID, state.InstrumentID,
	)
	if err != nil {
		return shared.Zero(), err
	}
	baseline := mustDecimal(state.BaselineBaseTotal)
	total, err := harness.exposure(ctx, state.AccountID, instrumentRecord)
	if err != nil {
		return shared.Zero(), err
	}
	delta := total.Sub(baseline)
	swap := state.MarketType == exchange.MarketTypeSwap
	original, err := harness.store.GetOrder(ctx, smokeSpaceID, state.OrderID)
	if err != nil {
		return delta, err
	}
	quantity, err := planCleanup(swap, baseline, total, mustDecimal(original.FilledQuantity))
	if err != nil {
		return delta, err
	}
	if quantity.IsZero() {
		return shared.Zero(), nil
	}
	instrument := instrumentRecord
	// 数量步长和最小下单量是交易所单位（OKX 永续是张），换算成基础资产数量后再和基础资产数量比。
	contractValue := mustDecimal(instrument.ContractValue)
	if contractValue.Cmp(shared.Zero()) <= 0 {
		contractValue = shared.MustDecimal("1")
	}
	step := mustDecimal(instrument.ExchangeQuantityStep).Mul(contractValue)
	quantity = shared.FloorToStep(quantity, step)
	if quantity.Cmp(mustDecimal(instrument.MinExchangeQuantity).Mul(contractValue)) < 0 {
		return delta, errors.New("test fill is below the Exchange cleanup minimum")
	}
	cleanup, err := harness.operator.PlaceOrder(
		ctx,
		operatorapp.ManualOrderCommand{
			SpaceID: smokeSpaceID, ActionID: xid.New().String(), LogicalAccountID: state.LogicalAccountID,
			TradingAccountID: state.AccountID, ClientOrderID: xid.New().String(),
			InstrumentID: state.InstrumentID, Type: exchange.OrderTypeMarket,
			Side: exchange.SideSell, PositionSide: exchange.RuleFor(state.MarketType).PositionSide, Quantity: quantity,
			Reason: "real testnet smoke reverse cleanup",
		},
	)
	if err != nil {
		return delta, fmt.Errorf("reverse test fill: %w", err)
	}
	if err := waitLocalTerminal(ctx, harness, cleanup.Order.OrderID); err != nil {
		return delta, err
	}
	total, err = harness.exposure(ctx, state.AccountID, instrumentRecord)
	if err != nil {
		return delta, err
	}
	remaining := total.Sub(baseline)
	if remaining.IsNegative() {
		return remaining, errors.New("reverse cleanup sold below pre-smoke baseline")
	}
	if remaining.Abs().Cmp(step) >= 0 {
		return remaining, errors.New("test-generated position remains after reverse cleanup")
	}
	return remaining, nil
}

// exposure 是测试标的当前的敞口，单位是基础资产数量：现货取基础资产余额，合约取持仓数量（带符号）。
// 数据来自最近一次账户同步，调用前由调用方同步。
func (h *liveHarness) exposure(ctx context.Context, accountID string, instrument store.InstrumentRecord) (shared.Decimal, error) {
	account, err := h.store.GetTradingAccountByID(ctx, accountID)
	if err != nil {
		return shared.Zero(), err
	}
	if exchange.RuleFor(instrument.MarketType).PositionFromBalance {
		return snapshotAssetTotal(account.Snapshot, instrument.BaseAsset)
	}
	record, found, err := h.store.GetPosition(
		ctx, account.SpaceID, accountID,
		exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol), string(exchange.PositionSideNet),
	)
	if err != nil || !found {
		return shared.Zero(), err
	}
	return shared.ParseDecimal(record.SignedQuantity)
}

// planCleanup 决定清理单的数量（基础资产数量，零表示不需要清理）。清理只回收测试买单自己成交的部分：
//   - 现货余额和合约持仓都不能低于基线：不知道少的是谁卖的、谁平的，不猜，拒绝清理，不去动别人的仓位；
//   - 余额或持仓变了却没有测试单的成交记录，拒绝；
//   - 合约清理只减多头：当前持仓必须是多头，数量不超过当前持仓，不会反手开空。
func planCleanup(swap bool, baseline, total, filled shared.Decimal) (shared.Decimal, error) {
	delta := total.Sub(baseline)
	if delta.IsNegative() {
		return shared.Zero(), errors.New("exposure fell below pre-smoke baseline; refusing to guess cleanup")
	}
	if delta.IsZero() {
		return shared.Zero(), nil
	}
	if swap && total.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), errors.New("swap position is not long after the test fill; refusing to open a short during cleanup")
	}
	if filled.IsZero() {
		return shared.Zero(), errors.New("exposure changed without a recorded test fill; refusing cleanup")
	}
	quantity := delta
	if quantity.Cmp(filled) > 0 {
		quantity = filled
	}
	if swap && quantity.Cmp(total) > 0 {
		quantity = total
	}
	return quantity, nil
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
