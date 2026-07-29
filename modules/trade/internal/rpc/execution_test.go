package rpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

type rpcAccountEligibility struct {
	account exchangeaccount.Account
}

func (s rpcAccountEligibility) ExecutionEligibility(
	context.Context,
	string,
) (exchangeaccount.Account, error) {
	return s.account, nil
}

type rpcInstrumentSource struct {
	instrument exchange.Instrument
}

func (s rpcInstrumentSource) GetInstrument(
	context.Context,
	exchange.Exchange,
	exchange.MarketType,
	string,
) (exchange.Instrument, error) {
	return s.instrument, nil
}

type rpcAdapterSource struct {
	adapter exchange.Adapter
}

func (s rpcAdapterSource) Adapter(string) (exchange.Adapter, error) {
	return s.adapter, nil
}

type rpcPriceSource struct {
	quote targetapp.Quote
}

func (s rpcPriceSource) LatestPrice(context.Context, string, string) (targetapp.Quote, error) {
	return s.quote, nil
}

type rpcAdapter struct {
	placed      exchange.OrderRequest
	cancelCalls int
}

func (a *rpcAdapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (a *rpcAdapter) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return nil, nil
}
func (a *rpcAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{}, nil
}
func (a *rpcAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}
func (a *rpcAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return nil, nil
}
func (a *rpcAdapter) ListRecentFills(
	context.Context,
	string,
	string,
) ([]exchange.Fill, string, error) {
	return nil, "", nil
}
func (a *rpcAdapter) GetOrder(context.Context, string, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (a *rpcAdapter) PlaceOrder(
	_ context.Context,
	request exchange.OrderRequest,
) (exchange.Order, error) {
	a.placed = request
	return exchange.Order{ExchangeOrderID: "exchange-order-1"}, nil
}
func (a *rpcAdapter) CancelOrder(context.Context, string, string) (exchange.Order, error) {
	a.cancelCalls++
	return exchange.Order{}, nil
}
func (a *rpcAdapter) SetLeverage(context.Context, string, shared.Decimal) error {
	return nil
}
func (a *rpcAdapter) SetMarginMode(context.Context, string, exchange.MarginMode) error {
	return nil
}
func (a *rpcAdapter) SubscribePrivate(context.Context, exchange.EventHandler) error {
	return nil
}

type rpcSyncer struct{}

func (rpcSyncer) SyncAccount(context.Context, string) error { return nil }

func TestExecutionRPCRejectsMissingSpace(t *testing.T) {
	response, err := (&ExecutionServer{}).GetOrder(
		context.Background(),
		&tradepb.GetOrderReq{OrderId: "order-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_NO_PERMISSION {
		t.Fatalf("ret_info = %+v", response.GetRetInfo())
	}
}

func TestOrderConversionPreservesMarketSwapFields(t *testing.T) {
	record := store.OrderRecord{
		OrderID: "order-1", ExchangeAccountID: "account-1",
		Exchange: "OKX", MarketType: "SWAP", Symbol: "BTC-USDT-SWAP",
		OrderType: "MARKET", TimeInForce: "", Side: "SELL",
		PositionSide: "NET", Quantity: "2", ReferencePrice: "60000",
		ReferencePriceAt: 1000, ReduceOnly: true, Source: "RPC",
		State: "OPEN", FilledQuantity: "0", AveragePrice: "0",
		ReservedQuantity: "0", RemainingReservedQuantity: "0",
	}
	converted := orderToPB(record)
	if converted.GetExchange() != tradepb.Exchange_EXCHANGE_OKX ||
		converted.GetMarketType() != tradepb.MarketType_MARKET_TYPE_SWAP ||
		converted.GetOrderType() != tradepb.OrderType_ORDER_TYPE_MARKET ||
		converted.GetPositionSide() != tradepb.PositionSide_POSITION_SIDE_NET ||
		!converted.GetReduceOnly() ||
		converted.LimitPrice != nil {
		t.Fatalf("converted order = %+v", converted)
	}
}

func TestPlaceOrderSubmitsMarketFieldsThroughOneOrderSpec(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateExchangeAccount(store.ExchangeAccountRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
			Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "LIVE",
			CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			Status: "ENABLED", Ready: true, SyncSymbols: []string{"BTCUSDT"},
			LeverageSettings: store.LeverageSettings{},
			FillCursors:      store.FillCursors{},
			Snapshot: store.ExchangeAccountSnapshot{Balances: []store.AssetBalance{{
				Asset: "USDT", Available: "1000", Total: "1000",
			}}},
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			InstrumentID: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.001",
			PriceTick: "0.01", MinNotional: "5", Status: "TRADING",
			ExchangeUpdatedAt: now.UnixMilli(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	account := exchangeaccount.Account{
		ID: "account-1", SpaceID: "space-1", Name: "main",
		Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSpot,
		ExecutionMode:      exchange.ExecutionModeLive,
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		Status: exchange.AccountStatusEnabled, Ready: true,
		SyncSymbols: []string{"BTCUSDT"},
		Snapshot: exchange.AccountSnapshot{Balances: []exchange.AssetBalance{{
			Asset: "USDT", Available: shared.MustDecimal("1000"),
		}}},
	}
	instrument := exchange.Instrument{
		Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSpot,
		Symbol: "BTCUSDT", InstrumentID: "BTCUSDT", BaseAsset: "BTC",
		QuoteAsset: "USDT", SettlementAsset: "USDT",
		ExchangeQuantityStep: shared.MustDecimal("0.001"),
		PriceTick:            shared.MustDecimal("0.01"),
		MinNotional:          shared.MustDecimal("5"), Status: "TRADING",
	}
	adapter := &rpcAdapter{}
	orders := &orderapp.Service{
		Store: tradeStore, Adapters: rpcAdapterSource{adapter: adapter},
		NewOrderID: func() string { return "order-1" }, Now: func() time.Time { return now },
		Validator: orderapp.Validator{
			Accounts:    rpcAccountEligibility{account: account},
			Instruments: rpcInstrumentSource{instrument: instrument},
			Now:         func() time.Time { return now }, MaxReferenceAge: time.Second,
		},
	}
	handler := &ExecutionServer{
		Orders: orders, Store: tradeStore,
		Prices: rpcPriceSource{quote: targetapp.Quote{
			Price: shared.MustDecimal("100"), UpdatedAt: now,
		}},
	}
	response, err := handler.PlaceOrder(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.PlaceOrderReq{
			ExchangeAccountId: "account-1", ClientOrderId: "client-1",
			Symbol: "BTCUSDT", OrderType: tradepb.OrderType_ORDER_TYPE_MARKET,
			Side: tradepb.OrderSide_ORDER_SIDE_BUY, Quantity: "1", ReduceOnly: true,
			Source: "TARGET", StrategyExecutionId: "forged",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		response.GetOrder().GetOrderType() != tradepb.OrderType_ORDER_TYPE_MARKET ||
		adapter.placed.OrderType != exchange.OrderTypeMarket ||
		adapter.placed.ReduceOnly ||
		adapter.placed.LimitPrice != nil {
		t.Fatalf("place response = %+v, adapter request = %+v", response, adapter.placed)
	}
}

func TestManualOrderRPCCannotSetReducePositionOnly(t *testing.T) {
	// The full request-path assertion lives in
	// TestPlaceOrderSubmitsMarketFieldsThroughOneOrderSpec.
	t.Run("caller flag ignored", TestPlaceOrderSubmitsMarketFieldsThroughOneOrderSpec)
}

func TestSubmitTargetUsesSharedSubmissionPath(t *testing.T) {
	now := time.Now().UTC()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateExchangeAccount(store.ExchangeAccountRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
			Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "LIVE",
			CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			Status: "ENABLED", Ready: true, SyncSymbols: []string{"BTCUSDT"},
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			InstrumentID: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.001",
			PriceTick: "0.01", Status: "TRADING",
			ExchangeUpdatedAt: now.UnixMilli(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	wakes := 0
	handler := &ExecutionServer{
		Store: tradeStore,
		Targets: targetapp.Submission{
			Store: tradeStore, Now: func() time.Time { return now },
			Wake: func() { wakes++ },
		},
	}
	response, err := handler.SubmitTarget(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.SubmitTargetReq{
			EventId: "execution-1", ExecutionId: "execution-1",
			StrategyRunId: "run-1", ExecutionBindingId: "binding-1",
			ExchangeAccountId: "account-1", CommandSequence: 1,
			NotAfter: now.Add(time.Minute).UnixMilli(), DataRevision: "revision-1",
			Targets: []*tradepb.TargetPosition{{
				InstrumentId: "BTCUSDT", Symbol: "BTCUSDT", TargetQuantity: "1",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		response.GetExecution().GetStatus() != targetapp.StatusRunning ||
		wakes != 1 {
		t.Fatalf("submit target response = %+v, wakes = %d", response, wakes)
	}
	stored, err := tradeStore.GetTargetExecution(
		context.Background(),
		"space-1",
		"execution-1",
	)
	if err != nil || stored.ExecutionBindingID != "binding-1" {
		t.Fatalf("stored target = %+v, err = %v", stored, err)
	}
}

func TestCancelOrderRecoversCancelingStateWithoutInvalidTransition(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateExchangeAccount(store.ExchangeAccountRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
			Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "LIVE",
			CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			Status: "ENABLED",
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			InstrumentID: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			ExchangeQuantityStep: "0.001", PriceTick: "0.01", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "order-1",
			ExchangeAccountID: "account-1", ClientOrderID: "client-1",
			ExchangeOrderID: "exchange-order-1", Exchange: "BINANCE",
			MarketType: "SPOT", Symbol: "BTCUSDT", OrderType: "MARKET",
			Side: "BUY", Quantity: "1", ReferencePrice: "100",
			Source: "RPC", State: "CANCELING", Version: 3,
		})
	}); err != nil {
		t.Fatal(err)
	}
	adapter := &rpcAdapter{}
	handler := &ExecutionServer{
		Store: tradeStore,
		Orders: &orderapp.Service{
			Store: tradeStore, Adapters: rpcAdapterSource{adapter: adapter},
			Syncer: rpcSyncer{},
		},
	}
	response, err := handler.CancelOrder(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.CancelOrderReq{OrderId: "order-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		response.GetOrder().GetState() != "CANCELING" ||
		adapter.cancelCalls != 1 {
		t.Fatalf("cancel response = %+v, calls = %d", response, adapter.cancelCalls)
	}
}
