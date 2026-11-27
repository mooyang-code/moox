package order

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func hkAccount(holding string) tradingaccount.Account {
	return tradingaccount.Account{
		ID: "hk-1", SpaceID: "space-1", Name: "港股", Exchange: exchange.ExchangeHKEX,
		Markets: []exchange.MarketType{exchange.MarketTypeEquity}, ExecutionMode: exchange.ExecutionModePaper,
		Environment: exchange.AccountEnvironmentPaper, SettlementAsset: "HKD", Status: exchange.AccountStatusEnabled, Ready: true,
		Paper: &tradingaccount.PaperConfig{InitialBalance: shared.MustDecimal("1000000"), MakerFeeRate: shared.MustDecimal("0.0003"),
			TakerFeeRate: shared.MustDecimal("0.0003"), SlippageBPS: shared.Zero()},
		Snapshot: exchange.AccountSnapshot{Balances: []exchange.AssetBalance{
			{Wallet: exchange.WalletUnified, Asset: "HKD", Available: shared.MustDecimal("1000000"), Total: shared.MustDecimal("1000000")},
			{Wallet: exchange.WalletUnified, Asset: "00005.XHKG", Available: shared.MustDecimal(holding), Total: shared.MustDecimal(holding)},
		}},
	}
}

// hkInstrumentForOrder 是 00005：每手 400 股。
func hkInstrumentForOrder() exchange.Instrument {
	return exchange.Instrument{
		Exchange: exchange.ExchangeHKEX, MarketType: exchange.MarketTypeEquity, ExchangeSymbol: "00005.XHKG", InstrumentID: "00005.XHKG",
		BaseAsset: "00005.XHKG", QuoteAsset: "HKD", SettlementAsset: "HKD",
		ExchangeQuantityStep: shared.MustDecimal("400"), MinExchangeQuantity: shared.MustDecimal("400"),
		PriceTick: shared.MustDecimal(exchange.HKMinPriceTick), Status: "TRADING",
	}
}

func hkValidator(holding string, quote execution.MarketQuote, now time.Time) Validator {
	return Validator{
		Accounts:        accountEligibilityStub{account: hkAccount(holding)},
		Instruments:     instrumentSourceStub{instrument: hkInstrumentForOrder()},
		Now:             func() time.Time { return now },
		MaxReferenceAge: 10 * time.Second, FeeBufferRate: shared.MustDecimal("0.002"),
		Quotes: stockQuotes{adapter: stockQuoteAdapter{quote: quote}},
	}
}

func hkSpec(side exchange.Side, quantity, limit string, now time.Time) orderdomain.OrderSpec {
	price := shared.MustDecimal(limit)
	return orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: "hk-1", ClientOrderID: "client-1", InstrumentID: "00005.XHKG",
			Type: exchange.OrderTypeLimit, FillPolicy: exchange.FillPolicyIOC, Side: side,
			Quantity: shared.MustDecimal(quantity), LimitPrice: &price,
		},
		ReferencePrice: shared.MustDecimal("138.5"), ReferencePriceAt: now,
		Owner: orderdomain.OrderOwner{Type: orderdomain.OwnerOperator, OwnerID: "action-1", LogicalAccountID: "logical-1"},
	}
}

func hkOrderQuote(now time.Time) execution.MarketQuote {
	return execution.MarketQuote{Bid: shared.MustDecimal("138.4"), Ask: shared.MustDecimal("138.5"), Last: shared.MustDecimal("138.5"), SourceTime: now}
}

// 港股模拟盘买单的预留 = 成交额 + 港股费用（和撮合扣费同一个函数），币种 HKD。
func TestHKBuyReservationUsesHKFee(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	validation, err := hkValidator("0", hkOrderQuote(now), now).Validate(context.Background(), "space-1", hkSpec(exchange.SideBuy, "400", "139", now))
	require.NoError(t, err)
	require.Equal(t, "HKD", validation.ReservedAsset)
	// 成交额 55600：佣金 16.68 + 印花税 ceil(55.6)=56 + 交易费 3.1414 + 征费 1.5012、0.0834 + 交收费 2.3352。
	require.Equal(t, "55679.7412", validation.ReservedQuantity.String())
	require.Equal(t, exchange.RuleAt(exchange.ExchangeHKEX, exchange.MarketTypeEquity).BuyCost(shared.MustDecimal("55600"), shared.MustDecimal("0.0003")).String(), validation.ReservedQuantity.String())
}

// 整手：数量必须是每手股数（400）的整数倍；卖出时零股一次卖完可以；停牌拒单；港股没有涨跌停，限价多高都不因此被拒。
func TestHKQuantityAndHaltValidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ctx := context.Background()
	_, err := hkValidator("0", hkOrderQuote(now), now).Validate(ctx, "space-1", hkSpec(exchange.SideBuy, "100", "139", now))
	require.ErrorIs(t, err, ErrQuantityRule, "A 股的一手 100 股在 00005 上不是整手")
	_, err = hkValidator("0", hkOrderQuote(now), now).Validate(ctx, "space-1", hkSpec(exchange.SideBuy, "600", "139", now))
	require.ErrorIs(t, err, ErrQuantityRule)
	_, err = hkValidator("0", hkOrderQuote(now), now).Validate(ctx, "space-1", hkSpec(exchange.SideBuy, "800", "139", now))
	require.NoError(t, err)
	// 持有 450 股：一次卖完（含 50 股零股）可以；只卖 30 股不行（余下 420 股既不是 0 也不满足整手规则）。
	_, err = hkValidator("450", hkOrderQuote(now), now).Validate(ctx, "space-1", hkSpec(exchange.SideSell, "450", "138", now))
	require.NoError(t, err)
	_, err = hkValidator("450", hkOrderQuote(now), now).Validate(ctx, "space-1", hkSpec(exchange.SideSell, "30", "138", now))
	require.ErrorIs(t, err, ErrQuantityRule)

	halted := hkOrderQuote(now)
	halted.Halted = true
	_, err = hkValidator("0", halted, now).Validate(ctx, "space-1", hkSpec(exchange.SideBuy, "400", "139", now))
	require.ErrorIs(t, err, ErrInstrumentHalted)
	_, err = hkValidator("0", hkOrderQuote(now), now).Validate(ctx, "space-1", hkSpec(exchange.SideBuy, "400", "200", now))
	require.NotErrorIs(t, err, ErrPriceLimit, "港股没有涨跌停价")
}

// 下单容量：最低佣金和印花税取整让花费和数量不成正比，容量要收到连费用一起付得起。
func TestHKBuyCapacityCoversMinimumFees(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	validation := Validation{Account: hkAccount("0"), Instrument: hkInstrumentForOrder(), ReservedQuantity: shared.MustDecimal("1")}
	spec := hkSpec(exchange.SideBuy, "800", "10", now)
	// 800 股 × 10 = 8000：佣金 2.4→3 + 印花税 8 + 0.452 + 0.216 + 0.012 + 交收费 0.336 = 12.016，共 8012.016。
	got := stockBuyCapacity(validation, spec, shared.MustDecimal("800"), shared.MustDecimal("400"), shared.MustDecimal("8012.015"))
	require.Equal(t, "400", got.String())
	got = stockBuyCapacity(validation, spec, shared.MustDecimal("800"), shared.MustDecimal("400"), shared.MustDecimal("8012.016"))
	require.Equal(t, "800", got.String())
}

// T+0：港股模拟盘卖单的资金检查不扣当日买入，当天买的当天能卖。
func TestHKPaperFundsDoNotLockTodayBuys(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "hkex", MarketType: "equity", ExchangeSymbol: "00005.XHKG", InstrumentID: "00005.XHKG",
			BaseAsset: "00005.XHKG", QuoteAsset: "HKD", SettlementAsset: "HKD",
			ExchangeQuantityStep: "400", MinExchangeQuantity: "400", PriceTick: "0.001", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "hk-1", Name: "港股", Exchange: "hkex", Markets: []string{"equity"},
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "HKD", Status: "ENABLED",
			PaperConfig: &store.PaperAccountConfigRecord{SpaceID: "space-1", TradingAccountID: "hk-1",
				InitialBalance: "1000000", MakerFeeRate: "0.0003", TakerFeeRate: "0.0003", SlippageBPS: "0"},
		})
	}))
	_, err = db.GetPaperBalanceSnapshot(context.Background(), "space-1", "hk-1")
	require.NoError(t, err)
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "hk-1", OrderID: "bought", ClientOrderID: "bought", ExchangeOrderID: "bought",
			Exchange: "hkex", MarketType: "equity", ExchangeSymbol: "00005.XHKG", InstrumentID: "00005.XHKG",
			OrderType: "MARKET", Side: "BUY", Quantity: "400", FilledQuantity: "0", ReferencePrice: "138.5", State: "OPEN",
			OwnerType: "EXTERNAL", OwnerID: "external-1", Version: 1,
		}); err != nil {
			return err
		}
		_, err := tx.InsertFill(store.FillRecord{
			SpaceID: "space-1", FillID: "fill-1", ExchangeTradeID: "trade-1", OrderID: "bought", ExchangeOrderID: "bought",
			TradingAccountID: "hk-1", Exchange: "hkex", MarketType: "equity", InstrumentID: "00005.XHKG",
			ExchangeSymbol: "00005.XHKG", Side: "BUY", Price: "138.5", Quantity: "400", Fee: "79.2", FeeAsset: "HKD",
			SettlementAsset: "HKD", RealizedPnL: "0", TradedAt: time.Now().UnixMilli(),
		})
		return err
	}))
	bought, err := db.PaperT1Bought(context.Background(), "space-1", "hk-1", exchange.T1Since(time.Now()).UnixMilli())
	require.NoError(t, err)
	require.Empty(t, bought, "港股不是 T+1，不统计当日买入")
	validation := Validation{
		Account: hkAccount("400"), Instrument: hkInstrumentForOrder(),
		ReservedAsset: "00005.XHKG", ReservedQuantity: shared.MustDecimal("400"),
	}
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		return checkPaperFunds(tx, validation, shared.Zero(), validation.ReservedQuantity, "", "", time.Now())
	}))
}
