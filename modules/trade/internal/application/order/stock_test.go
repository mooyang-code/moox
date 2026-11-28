package order

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func stockAccount(holding string) store.TradingAccountRecord {
	return store.TradingAccountRecord{
		TradingAccountID: "stock-1", SpaceID: "space-1", Name: "A 股", Exchange: string(exchange.ExchangeAShare),
		Markets: []string{string(exchange.MarketTypeEquity)}, ExecutionMode: string(exchange.ExecutionModePaper),
		Environment: string(exchange.AccountEnvironmentPaper), SettlementAsset: "CNY", Status: string(exchange.AccountStatusEnabled), Ready: true,
		PaperConfig: &store.PaperAccountConfigRecord{InitialBalance: "1000000", MakerFeeRate: "0.00025",
			TakerFeeRate: "0.00025", SlippageBPS: "0"},
		Snapshot: store.TradingAccountSnapshot{Balances: []store.AssetBalance{
			{Wallet: string(exchange.WalletUnified), Asset: "CNY", Available: "1000000", Total: "1000000"},
			{Wallet: string(exchange.WalletUnified), Asset: "600519.XSHG", Available: holding, Total: holding},
		}},
	}
}

func stockInstrumentForOrder() exchange.Instrument {
	return exchange.Instrument{
		Exchange: exchange.ExchangeAShare, MarketType: exchange.MarketTypeEquity, ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
		BaseAsset: "600519.XSHG", QuoteAsset: "CNY", SettlementAsset: "CNY",
		ExchangeQuantityStep: shared.MustDecimal("100"), MinExchangeQuantity: shared.MustDecimal("100"),
		PriceTick: shared.MustDecimal("0.01"), MaxBuyPriceRatio: shared.MustDecimal("1.02"), MinSellPriceRatio: shared.MustDecimal("0.98"),
		Status: "TRADING",
	}
}

type stockQuoteAdapter struct {
	execution.ExecutionAdapter
	quote execution.MarketQuote
}

func (a stockQuoteAdapter) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
	return a.quote, nil
}

type stockQuotes struct{ adapter execution.ExecutionAdapter }

func (s stockQuotes) Adapter(string) (execution.ExecutionAdapter, error) { return s.adapter, nil }

func stockValidator(holding string, quote execution.MarketQuote, now time.Time) Validator {
	return Validator{
		Accounts:        accountEligibilityStub{account: stockAccount(holding)},
		Instruments:     instrumentSourceStub{instrument: stockInstrumentForOrder()},
		Now:             func() time.Time { return now },
		MaxReferenceAge: 10 * time.Second, FeeBufferRate: shared.MustDecimal("0.002"),
		Quotes: stockQuotes{adapter: stockQuoteAdapter{quote: quote}},
	}
}

func stockSpec(side exchange.Side, quantity, limit string, now time.Time) orderdomain.OrderSpec {
	price := shared.MustDecimal(limit)
	return orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: "stock-1", ClientOrderID: "client-1", InstrumentID: "600519.XSHG",
			Type: exchange.OrderTypeLimit, FillPolicy: exchange.FillPolicyIOC, Side: side,
			Quantity: shared.MustDecimal(quantity), LimitPrice: &price,
		},
		ReferencePrice: shared.MustDecimal("10"), ReferencePriceAt: now,
		Owner: orderdomain.OrderOwner{Type: orderdomain.OwnerOperator, OwnerID: "action-1", LogicalAccountID: "logical-1"},
	}
}

func normalQuote(now time.Time) execution.MarketQuote {
	return execution.MarketQuote{
		Bid: shared.MustDecimal("9.99"), Ask: shared.MustDecimal("10"), Last: shared.MustDecimal("10"), SourceTime: now,
		LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9"),
	}
}

// 模拟盘 A 股买单的预留 = 成交额 + AShareFee（佣金最低 5 元、过户费）：和撮合扣费同一个函数。
func TestStockBuyReservationUsesStockFee(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	validation, err := stockValidator("0", normalQuote(now), now).Validate(context.Background(), "space-1", stockSpec(exchange.SideBuy, "100", "10.2", now))
	require.NoError(t, err)
	require.Equal(t, "CNY", validation.ReservedAsset)
	// 成交额 1020：佣金 0.255 不足 5 元按 5 元，过户费 0.0102。
	require.Equal(t, "1025.0102", validation.ReservedQuantity.String())
	require.Equal(t, exchange.RuleAt(exchange.ExchangeAShare, exchange.MarketTypeEquity).BuyCost(shared.MustDecimal("1020"), shared.MustDecimal("0.00025")).String(), validation.ReservedQuantity.String())
}

// 卖出数量要么满足整手规则，要么卖完后余下的持仓满足整手规则（零股一次卖完）。
func TestStockOddLotSellValidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		holding, quantity string
		ok                bool
	}{
		{"150", "150", true},  // 全部卖出，含零股
		{"250", "150", true},  // 卖出零股 50 和一手，余 100
		{"250", "120", false}, // 余 130，零股没有一次卖完
		{"250", "200", true},  // 整手
		{"50", "50", true},
		{"50", "60", false},
	} {
		_, err := stockValidator(tc.holding, normalQuote(now), now).Validate(context.Background(), "space-1", stockSpec(exchange.SideSell, tc.quantity, "10", now))
		if tc.ok {
			require.NoError(t, err, "持仓 %s 卖 %s", tc.holding, tc.quantity)
		} else {
			require.ErrorIs(t, err, ErrQuantityRule, "持仓 %s 卖 %s", tc.holding, tc.quantity)
		}
	}
	// 买入不能是零股。
	_, err := stockValidator("0", normalQuote(now), now).Validate(context.Background(), "space-1", stockSpec(exchange.SideBuy, "150", "10", now))
	require.ErrorIs(t, err, ErrQuantityRule)
}

// 委托价超出涨跌停价拒单（永久错误）；停牌返回 ErrInstrumentHalted（不是永久错误，执行器顺延）。
func TestStockPriceLimitAndHaltValidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, err := stockValidator("0", normalQuote(now), now).Validate(context.Background(), "space-1", stockSpec(exchange.SideBuy, "100", "11.01", now))
	require.ErrorIs(t, err, ErrPriceLimit)
	require.True(t, IsPermanentValidationError(err))
	_, err = stockValidator("1000", normalQuote(now), now).Validate(context.Background(), "space-1", stockSpec(exchange.SideSell, "100", "8.99", now))
	require.ErrorIs(t, err, ErrPriceLimit)
	_, err = stockValidator("0", normalQuote(now), now).Validate(context.Background(), "space-1", stockSpec(exchange.SideBuy, "100", "11", now))
	require.NoError(t, err, "等于涨停价可以下")

	halted := normalQuote(now)
	halted.Halted = true
	_, err = stockValidator("0", halted, now).Validate(context.Background(), "space-1", stockSpec(exchange.SideBuy, "100", "10", now))
	require.True(t, errors.Is(err, ErrInstrumentHalted))
	require.False(t, IsPermanentValidationError(err))
}

// 下单容量：最低 5 元佣金让花费和数量不成正比，容量要收到连费用一起付得起。
func TestStockBuyCapacityCoversMinimumCommission(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	validation := Validation{Account: stockAccount("0"), Instrument: stockInstrumentForOrder(), ReservedQuantity: shared.MustDecimal("1")}
	spec := stockSpec(exchange.SideBuy, "1000", "10", now)
	clamp := func(qty, step, avail string) shared.Decimal {
		return orderPolicyOf(validation, nil).ClampBuyCapacity(execution.CapacityClampRequest{
			Account: validation.Account, Instrument: validation.Instrument,
			OrderType: spec.Type, Side: spec.Side, ReferencePrice: spec.ReferencePrice, LimitPrice: spec.LimitPrice,
			Quantity: shared.MustDecimal(qty), Step: shared.MustDecimal(step), Available: shared.MustDecimal(avail),
		})
	}
	// 1000 股要 10000 + 5 + 0.1；只有 10004 元时减到 900 股（9000 + 5 + 0.09）。
	got := clamp("1000", "100", "10004")
	require.Equal(t, "900", got.String())
	got = clamp("1000", "100", "10005.1")
	require.Equal(t, "1000", got.String())
	got = clamp("100", "100", "1004")
	require.True(t, got.IsZero(), "一手都付不起就是 0")
}

// T+1：模拟盘卖单的资金检查在写事务里按余额表算，当日买入的部分要扣掉，手工卖单也卖不了当天买的。
func TestPaperFundsSubtractTodayBuysForStockSell(t *testing.T) {
	now := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC) // 上海 10:00
	for _, tc := range []struct {
		name     string
		tradedAt time.Time
		wantErr  bool
	}{
		{"当日买入不能卖", now.Add(-15 * time.Minute), true},
		{"昨天买入可以卖", exchange.T1Since(now).Add(-time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
				if err := tx.UpsertInstrument(store.InstrumentRecord{
					Exchange: "ashare", MarketType: "equity", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
					BaseAsset: "600519.XSHG", QuoteAsset: "CNY", SettlementAsset: "CNY",
					ExchangeQuantityStep: "100", MinExchangeQuantity: "100", PriceTick: "0.01", Status: "TRADING",
				}); err != nil {
					return err
				}
				return tx.CreateTradingAccount(store.TradingAccountRecord{
					SpaceID: "space-1", TradingAccountID: "stock-1", Name: "A 股", Exchange: "ashare", Markets: []string{"equity"},
					ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "CNY", Status: "ENABLED",
					PaperConfig: &store.PaperAccountConfigRecord{SpaceID: "space-1", TradingAccountID: "stock-1",
						InitialBalance: "1000000", MakerFeeRate: "0.00025", TakerFeeRate: "0.00025", SlippageBPS: "0"},
				})
			}))
			_, err = db.GetPaperBalanceSnapshot(context.Background(), "space-1", "stock-1")
			require.NoError(t, err)
			require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
				if err := tx.CreateOrder(store.OrderRecord{
					SpaceID: "space-1", TradingAccountID: "stock-1", OrderID: "bought", ClientOrderID: "bought", ExchangeOrderID: "bought",
					Exchange: "ashare", MarketType: "equity", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
					OrderType: "MARKET", Side: "BUY", Quantity: "100", FilledQuantity: "0", ReferencePrice: "10", State: "OPEN",
					OwnerType: "EXTERNAL", OwnerID: "external-1", Version: 1,
				}); err != nil {
					return err
				}
				_, err := tx.InsertFill(store.FillRecord{
					SpaceID: "space-1", FillID: "fill-1", ExchangeTradeID: "trade-1", OrderID: "bought", ExchangeOrderID: "bought",
					TradingAccountID: "stock-1", Exchange: "ashare", MarketType: "equity", InstrumentID: "600519.XSHG",
					ExchangeSymbol: "600519.XSHG", Side: "BUY", Price: "10", Quantity: "100", Fee: "5.01", FeeAsset: "CNY",
					SettlementAsset: "CNY", RealizedPnL: "0", TradedAt: tc.tradedAt.UnixMilli(),
				})
				return err
			}))
			validation := Validation{
				Account: stockAccount("100"), Instrument: stockInstrumentForOrder(),
				ReservedAsset: "600519.XSHG", ReservedQuantity: shared.MustDecimal("100"),
			}
			err = db.Transaction(context.Background(), func(tx *store.Tx) error {
				return checkLocalLedgerFunds(tx, validation, shared.Zero(), validation.ReservedQuantity, "", "", now)
			})
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInsufficientFunds)
			} else {
				require.NoError(t, err)
			}
			// 小额买入扣了最低佣金后余额仍为正。
			balances, err := db.GetPaperBalanceSnapshot(context.Background(), "space-1", "stock-1")
			require.NoError(t, err)
			require.Equal(t, "998994.99", balances.Totals["CNY"].String())
		})
	}
}

// A 股模拟盘的 Capacity 与 Place 计算 T+1 当日买入锁定时必须使用注入时钟 s.Now，而不是系统时钟 time.Now()。
func TestStockPaperT1UsesInjectedClock(t *testing.T) {
	day1 := time.Date(2020, 6, 1, 2, 0, 0, 0, time.UTC) // 上海 2020-06-01 10:00
	day2 := day1.Add(24 * time.Hour)                    // 上海 2020-06-02 10:00
	current := day1

	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "ashare", MarketType: "equity", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
			BaseAsset: "600519.XSHG", QuoteAsset: "CNY", SettlementAsset: "CNY",
			ExchangeQuantityStep: "100", MinExchangeQuantity: "100", PriceTick: "0.01", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "stock-1", Name: "A 股", Exchange: "ashare", Markets: []string{"equity"},
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "CNY", Status: "ENABLED",
			PaperConfig: &store.PaperAccountConfigRecord{SpaceID: "space-1", TradingAccountID: "stock-1",
				InitialBalance: "1000000", MakerFeeRate: "0.00025", TakerFeeRate: "0.00025", SlippageBPS: "0"},
		})
	}))
	_, err = db.GetPaperBalanceSnapshot(context.Background(), "space-1", "stock-1")
	require.NoError(t, err)
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "stock-1", OrderID: "bought", ClientOrderID: "bought", ExchangeOrderID: "bought",
			Exchange: "ashare", MarketType: "equity", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
			OrderType: "MARKET", Side: "BUY", Quantity: "100", FilledQuantity: "0", ReferencePrice: "10", State: "OPEN",
			OwnerType: "EXTERNAL", OwnerID: "external-1", Version: 1,
		}); err != nil {
			return err
		}
		_, err := tx.InsertFill(store.FillRecord{
			SpaceID: "space-1", FillID: "fill-1", ExchangeTradeID: "trade-1", OrderID: "bought", ExchangeOrderID: "bought",
			TradingAccountID: "stock-1", Exchange: "ashare", MarketType: "equity", InstrumentID: "600519.XSHG",
			ExchangeSymbol: "600519.XSHG", Side: "BUY", Price: "10", Quantity: "100", Fee: "5.01", FeeAsset: "CNY",
			SettlementAsset: "CNY", RealizedPnL: "0", TradedAt: day1.Add(-10 * time.Minute).UnixMilli(),
		})
		return err
	}))

	adapter := &stockQuoteAdapter{quote: normalQuote(day1)}
	svc := &Service{
		Store:    db,
		Adapters: errorBoundarySource{adapter: adapter},
		Now:      func() time.Time { return current },
		Validator: Validator{
			Accounts:        accountEligibilityStub{account: stockAccount("100")},
			Instruments:     instrumentSourceStub{instrument: stockInstrumentForOrder()},
			Now:             func() time.Time { return current },
			MaxReferenceAge: 10 * time.Second,
			Quotes:          stockQuotes{adapter: adapter},
		},
	}

	// 同日（day1，尽管远早于系统真实时间）：按注入时钟仍属当日买入，可卖容量为 0 且 Place 报资金不足。
	spec := stockSpec(exchange.SideSell, "100", "10", day1)
	spec.Owner = orderdomain.OrderOwner{Type: orderdomain.OwnerExternal, OwnerID: "ext-1"}
	capDay1, err := svc.Capacity(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.True(t, capDay1.IsZero(), "day1 当日买入应被 T+1 锁定，实际可卖=%s", capDay1)
	_, err = svc.Place(context.Background(), "space-1", spec)
	require.ErrorIs(t, err, ErrInsufficientFunds)

	// 推进注入时钟到次日（day2）：T+1 解锁，可卖 100 股且 Place 成功。
	current = day2
	adapter.quote = normalQuote(day2)
	specDay2 := stockSpec(exchange.SideSell, "100", "10", day2)
	specDay2.ClientOrderID = "stock-sell-day2"
	specDay2.Owner = orderdomain.OrderOwner{Type: orderdomain.OwnerExternal, OwnerID: "ext-2"}
	capDay2, err := svc.Capacity(context.Background(), "space-1", specDay2)
	require.NoError(t, err)
	require.Equal(t, "100", capDay2.String())
	_, err = svc.Place(context.Background(), "space-1", specDay2)
	require.NoError(t, err)
}
