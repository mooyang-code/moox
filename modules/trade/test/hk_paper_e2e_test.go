package test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	equityapp "github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/hkex"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// hkTargetDelivery 编码一份策略发给组合 logicalID 的权重目标（生产里由 Strategy 经 EventBus 发来）。
func hkTargetDelivery(t *testing.T, now time.Time, logicalID, targetID string, sequence int64, instrumentID, weight string) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	bar := now.Add(time.Duration(sequence-10) * time.Millisecond)
	encoded, err := registry.Encode(
		events.LogicalAccountTargetWeightRequested,
		&tradeeventpb.LogicalAccountTargetWeightRequested{
			TargetId: targetID, InstanceId: "dual_ma_hk_5m", SessionId: "session-hk", SessionStartedAt: 1_000, StrategyId: "default_dual_ma_hk_5m",
			LogicalAccountId: logicalID,
			Targets:          []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: instrumentID, MarketType: "equity", TargetWeight: weight}},
			BarEndTime:       bar.UnixMilli(), ValidUntil: bar.Add(time.Hour).UnixMilli(),
		},
		events.PublishOptions{EventID: targetID, OccurredAt: now, SpaceID: testSpace, SubjectID: logicalID},
	)
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{RawData: raw, Subject: encoded.Subject, RawMessageID: targetID, ContentType: events.ContentType}
}

// 港股模拟盘从头到尾：建账户（场所 HKEX、按 HKD 结算）→ 策略的权重目标 → 换算成整手数量 → 模拟订单（限价对齐到价位表）
// → 成交（港股费用）→ 持仓 → 流水；当日买入当日可卖（T+0）；时段外的目标留到开盘；取不到每手股数的标的拒绝并写明原因。
func TestHKPaperTargetToOrderFillPositionLedger(t *testing.T) {
	ctx := spacecontext.WithSpaceID(context.Background(), testSpace)
	now := time.Now().UTC()
	clock := func() time.Time { return now }

	// 行情与标的：腾讯快照的替身。00700 每手 100 股、00005 每手 400 股；09988 取不到每手股数，不进标的表。
	quotes := func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
		all := map[string]cnstock.Quote{
			"00700.XHKG": {InstrumentID: "00700.XHKG", Last: "376.000", PrevClose: "390.400", Bid: "375.800", Ask: "376.000", Time: now, LotSize: 100},
			"00005.XHKG": {InstrumentID: "00005.XHKG", Last: "138.500", PrevClose: "140.500", Bid: "138.400", Ask: "138.500", Time: now, LotSize: 400},
			"09988.XHKG": {InstrumentID: "09988.XHKG", Last: "85.450", PrevClose: "90.150", Bid: "85.400", Ask: "85.450", Time: now},
		}
		result := map[string]cnstock.Quote{}
		for _, id := range ids {
			if quote, ok := all[id]; ok {
				result[id] = quote
			}
		}
		return result, nil
	}
	market := hkex.NewMarketFrom(quotes, func(context.Context) ([]string, error) {
		return []string{"00700.XHKG", "00005.XHKG", "09988.XHKG"}, nil
	}, nil)
	market.Now = clock
	require.NoError(t, market.RefreshList(ctx))

	// 场所登记：与 bootstrap 的港股一项相同，只是交易时段换成可控的开关。
	var open atomic.Bool
	registry := execution.NewRegistry()
	registry.Register(execution.ExchangeDescriptor{
		Exchange:         exchange.ExchangeHKEX,
		OrderTypes:       []exchange.OrderType{exchange.OrderTypeLimit},
		FillPolicies:     []exchange.FillPolicy{exchange.FillPolicyGTC, exchange.FillPolicyIOC},
		Markets:          []exchange.MarketType{exchange.MarketTypeEquity},
		SettlementAssets: []string{hkex.SettlementAsset},
		LiveUnavailable:  "港股只支持模拟盘，没有实盘下单通道",
		Session:          fakeSession{open: &open},
		SessionHours:     hkex.SessionHours,
		InstrumentScope:  hkex.InstrumentScope,
	}, func(exchange.AccountConfig) (execution.PublicMarketData, error) {
		return stockfeed.NewAdapter(market), nil
	}, nil)

	path := filepathForTest(t)
	db, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// 建账户：港股只开股票、按 HKD 结算；结算资产不对在建账户时就拒绝。
	logicalSrv := &rpc.LogicalAccountServer{Store: db, LogicalAccounts: &logicalapp.Service{Store: db}}
	console := &rpc.ConsoleServer{
		Store:                db,
		AccountServer:        &rpc.AccountServer{Store: db, Logical: logicalSrv, Accounts: &accountapp.Service{Store: accountapp.Repository{Store: db}, Exchanges: registry}},
		LogicalAccountServer: logicalSrv,
	}
	request := &tradepb.CreateTradingAccountReq{
		Name: "港股模拟", LogicalAccountName: "港股模拟组合", Exchange: "hkex",
		Markets: []string{"equity"}, SettlementAsset: "CNY",
		Paper:       &tradepb.PaperConfig{InitialBalance: "1000000", MakerFeeRate: "0.0003", TakerFeeRate: "0.0003", SlippageBps: "0"},
		ControlMode: tradepb.ControlMode_CONTROL_MODE_STRATEGY,
	}
	rejected, err := console.CreateTradingAccount(ctx, request)
	require.NoError(t, err)
	require.NotEqual(t, tradepb.ErrorCode_SUCCESS, rejected.GetRetInfo().GetCode())
	require.Contains(t, rejected.GetRetInfo().GetMsg(), "结算资产只能是 HKD")
	request.SettlementAsset = "HKD"
	created, err := console.CreateTradingAccount(ctx, request)
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo())
	require.Equal(t, "hkex", created.GetAccount().GetExchange())
	logicalID, accountID := created.GetLogicalAccount().GetLogicalAccountId(), created.GetAccount().GetTradingAccountId()

	// 标的入库（生产里由会话启动时写入）。
	instruments, err := market.LoadInstruments(ctx)
	require.NoError(t, err)
	require.Len(t, instruments, 2, "09988 取不到每手股数，不收录")
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		for _, instrument := range instruments {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: string(instrument.Exchange), Environment: "PRODUCTION", MarketType: string(instrument.MarketType),
				ExchangeSymbol: instrument.ExchangeSymbol, InstrumentID: instrument.InstrumentID,
				BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, SettlementAsset: instrument.SettlementAsset,
				ContractValue: "0", ExchangeQuantityStep: instrument.ExchangeQuantityStep.String(),
				MinExchangeQuantity: instrument.MinExchangeQuantity.String(), PriceTick: instrument.PriceTick.String(),
				MinNotional: "0", MaxBuyPriceRatio: "0", MinSellPriceRatio: "0", Status: instrument.Status, ExchangeUpdatedAt: now.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		return nil
	}))

	accountRecord, err := db.GetTradingAccount(ctx, testSpace, accountID)
	require.NoError(t, err)
	adapter := &paperexec.Adapter{Store: db, Account: accountRecord, MarketData: stockfeed.NewAdapter(market), Exchanges: registry, Now: clock}
	_, err = adapter.LoadInstruments(ctx)
	require.NoError(t, err)
	f := buildFixture(db, path, newFakeExchange(exchange.MarketTypeEquity), adapter)
	f.orders.Now, f.orders.Validator.Now = clock, clock
	f.orders.Validator.Quotes = adapterSource{adapter: adapter}
	f.reducer.Now = clock
	f.sync.Now = func() time.Time { return now.Add(time.Second) }
	syncAccount := func() store.TradingAccountRecord {
		_, err := f.sync.SyncAccount(ctx, accountID)
		require.NoError(t, err)
		account, err := db.GetTradingAccountByID(ctx, accountID)
		require.NoError(t, err)
		return account
	}
	syncAccount()
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation(testSpace, logicalID, "ACTIVE", "")
	}))

	prices := targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: adapter}, Exchanges: registry, Now: clock}
	equity := &equityapp.Service{Store: db, Adapters: adapterSource{adapter: adapter}, Exchanges: registry, Now: clock, SourceMaxAge: time.Minute}
	require.NoError(t, equity.SampleAccount(ctx, accountID))
	options := eventconsumer.TargetOptions{Store: db, Now: clock, Exchanges: registry,
		WeightResolver: &targetapp.WeightResolver{Store: db, Prices: prices, Equity: equity, Exchanges: registry, Now: clock}}
	executor := &targetapp.Executor{Store: db, Orders: f.orders, Prices: prices, Exchanges: registry, Now: clock}
	converge := func() store.LogicalAccountTargetRecord {
		_, err := executor.Converge(ctx, testSpace, logicalID)
		require.NoError(t, err)
		current, err := db.GetLogicalAccountTarget(ctx, testSpace, logicalID)
		require.NoError(t, err)
		return current
	}
	match := func() {
		matcher := &paperexec.Matcher{
			Store: db, Reducer: f.reducer, Exchanges: registry, Now: clock,
			Refresh: func(ctx context.Context, id string) error {
				_, err := f.sync.SyncAccount(ctx, id)
				return err
			},
			DecideContext: (&paperexec.Decider{Store: db, Adapters: adapterSource{adapter: adapter}, Exchanges: registry, Now: clock}).Decide,
		}
		require.NoError(t, matcher.Scan(ctx))
	}
	orders := func() []store.OrderRecord {
		records, _, err := db.ListOrders(ctx, testSpace, store.OrderQuery{TradingAccountID: accountID, Limit: 10})
		require.NoError(t, err)
		return records
	}
	balance := func(account store.TradingAccountRecord, asset string) store.AssetBalance {
		for _, item := range account.Snapshot.Balances {
			if item.Asset == asset {
				return item
			}
		}
		return store.AssetBalance{Asset: asset, Available: "0", Total: "0"}
	}

	// 取不到每手股数的标的：目标被拒，原因写明标的范围，不猜每手股数。
	unknown := eventconsumer.HandleTarget(ctx, hkTargetDelivery(t, now, logicalID, "target-unknown-lot", 1, "09988.XHKG", "0.1"), options)
	require.NotEqual(t, jetstream.ACK, unknown.Decision)
	require.ErrorContains(t, unknown.Err, "没有启用的成员支持标的 09988.XHKG")
	require.ErrorContains(t, unknown.Err, "港股模拟盘只收录恒生指数成分里取得到每手股数的标的")

	// 策略目标：00700 占权益 10%。权益 100 万港元、现价 376 → 265.96 股，按每手 100 股向下取整到 200 股。
	accepted := eventconsumer.HandleTarget(ctx, hkTargetDelivery(t, now, logicalID, "target-buy", 2, "00700.XHKG", "0.1"), options)
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)

	// 时段外：目标保持等待，不下单；开盘后再下。
	current := converge()
	require.Equal(t, targetapp.StatusPending, current.Status)
	require.Equal(t, execution.ReasonOutOfSession, current.LastError)
	require.Empty(t, orders())

	open.Store(true)
	converge()
	placed := orders()
	require.Len(t, placed, 1)
	buy := placed[0]
	require.Equal(t, "hkex", buy.Exchange)
	require.Equal(t, "equity", buy.MarketType)
	require.Equal(t, "00700.XHKG", buy.ExchangeSymbol)
	require.Equal(t, "BUY", buy.Side)
	require.Equal(t, "200", buy.Quantity, "按每手 100 股取整")
	require.Equal(t, "LIMIT", buy.OrderType)
	require.Equal(t, "IOC", buy.TimeInForce)
	require.NotNil(t, buy.LimitPrice)
	limit := decimal(*buy.LimitPrice)
	require.True(t, limit.Cmp(decimal("376")) >= 0)
	require.True(t, limit.Div(exchange.HKPriceTick(limit)).IsInteger(), "限价 %s 要落在价位表上（200～500 港元一档 0.2）", limit)

	// 成交：按卖一 376 成交 200 股，费用按港股规则（佣金 22.56 + 印花税 76 + 交易费 4.2488 + 征费 2.0304、0.1128 + 交收费 3.1584）。
	match()
	buy = orders()[0]
	require.Equal(t, "FILLED", buy.State)
	require.Equal(t, "200", buy.FilledQuantity)
	fills, _, err := db.ListFills(ctx, testSpace, store.FillQuery{TradingAccountID: accountID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, fills, 1)
	require.Equal(t, "376", decimal(fills[0].Price).String())
	require.Equal(t, "108.1104", decimal(fills[0].Fee).String())
	require.Equal(t, "HKD", fills[0].FeeAsset)

	// 持仓与现金：200 股当日就可卖（T+0）；现金 = 1000000 − 75200 − 108.1104。
	account := syncAccount()
	holding := balance(account, "00700.XHKG")
	require.Equal(t, "200", decimal(holding.Total).String())
	require.Equal(t, "200", decimal(holding.Available).String(), "港股 T+0：当日买入可卖")
	require.Equal(t, "924691.8896", decimal(balance(account, "HKD").Total).String())
	current = converge()
	require.Equal(t, targetapp.StatusConverged, current.Status, "不足一手的差额视为已满足")
	require.Len(t, orders(), 1)

	// 当日卖出：目标权重 0，同一天直接卖出 200 股，不顺延。
	sold := eventconsumer.HandleTarget(ctx, hkTargetDelivery(t, now, logicalID, "target-sell", 3, "00700.XHKG", "0"), options)
	require.Equal(t, jetstream.ACK, sold.Decision, sold.Err)
	current = converge()
	require.Empty(t, current.BlockedTargets, "T+0 没有“当日买入，次日可卖”的顺延")
	placed = orders()
	require.Len(t, placed, 2)
	var sell store.OrderRecord
	for _, order := range placed {
		if order.Side == "SELL" {
			sell = order
		}
	}
	require.Equal(t, "200", sell.Quantity)
	sellLimit := decimal(*sell.LimitPrice)
	require.True(t, sellLimit.Div(exchange.HKPriceTick(sellLimit)).IsInteger(), "卖出限价 %s 要落在价位表上", sellLimit)
	match()
	account = syncAccount()
	require.True(t, decimal(balance(account, "00700.XHKG").Total).IsZero())
	// 卖出按买一 375.8 成交：成交额 75160，费用 22.548 + 76 + 4.24654 + 2.02932 + 0.11274 + 3.15672 = 108.09332（港股印花税买卖都收）。
	require.Equal(t, "999743.79628", decimal(balance(account, "HKD").Total).String())

	// 流水：两笔成交各一条手续费，币种 HKD。
	page, err := adapter.ListLedger(ctx, exchange.LedgerQuery{Start: now.Add(-time.Hour), End: now.Add(time.Hour), Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	fees := map[string]bool{}
	for _, entry := range page.Entries {
		require.Equal(t, exchange.LedgerFee, entry.Type)
		require.Equal(t, "HKD", entry.Asset)
		require.Equal(t, exchange.MarketTypeEquity, entry.Market)
		require.Equal(t, "00700.XHKG", entry.ExchangeSymbol)
		fees[entry.Amount.String()] = true
	}
	require.Equal(t, map[string]bool{"-108.1104": true, "-108.09332": true}, fees)
}
