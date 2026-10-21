package replay

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newService(t *testing.T) *Service {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	return &Service{Store: tradeStore, Now: func() time.Time { return now }}
}

func q(mark, exec string) Quote { return Quote{Mark: mark, Exec: exec} }

func buy(replayID string, period int64, weight string, quotes map[string]Quote) AdvanceRequest {
	return AdvanceRequest{ReplayID: replayID, Period: period, Quotes: quotes, Decision: Decision{Kind: KindUpdateTarget, Targets: []Target{{InstrumentID: "A", TargetWeight: weight}}}}
}

// 目标权重按成交价换算成数量，费用从现金里扣；权益按估值价计算；先卖后买；跳过的周期只估值不撮合。
func TestAdvanceRebalancesAndMarksToMarket(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	session, err := svc.CreateSession(ctx, CreateRequest{ReplayID: "r1", InitialCash: "100000", FeeRate: "0.001"})
	require.NoError(t, err)
	require.True(t, session.Created)

	first, err := svc.Advance(ctx, buy("r1", 1000, "0.5", map[string]Quote{"A": q("100", "100")}))
	require.NoError(t, err)
	require.Equal(t, "100000", first.EquityBefore)
	require.Len(t, first.Orders, 1)
	require.Equal(t, "BUY", first.Orders[0].Side)
	require.Equal(t, "500", first.Orders[0].FilledQuantity, "0.5 × 100000 ÷ 100 = 500")
	require.Equal(t, "49950", first.Cash, "500 × 100 × 1.001 = 50050 从现金里扣")
	require.Equal(t, "99950", first.Equity, "期末权益 = 现金 + 持仓估值，差额就是费用")
	require.Equal(t, "50", first.Fees)

	skip, err := svc.Advance(ctx, AdvanceRequest{ReplayID: "r1", Period: 2000, Decision: Decision{Kind: KindSkip}, Quotes: map[string]Quote{"A": q("110", "")}})
	require.NoError(t, err)
	require.Empty(t, skip.Orders)
	require.Equal(t, "104950", skip.Equity, "跳过的周期只估值：49950 + 500 × 110")

	// 把仓位降到 0.25：先卖出，按新成交价换算。
	third, err := svc.Advance(ctx, buy("r1", 3000, "0.25", map[string]Quote{"A": q("110", "110")}))
	require.NoError(t, err)
	require.Len(t, third.Orders, 1)
	require.Equal(t, "SELL", third.Orders[0].Side)
	require.Equal(t, "FILLED", third.Orders[0].Status)
	got, err := svc.GetSession(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, int64(3000), got.LastPeriod)
	require.Contains(t, got.Positions, "A")

	// 空目标集合表示清仓。
	flat, err := svc.Advance(ctx, AdvanceRequest{ReplayID: "r1", Period: 4000, Decision: Decision{Kind: KindUpdateTarget}, Quotes: map[string]Quote{"A": q("120", "120")}})
	require.NoError(t, err)
	require.Empty(t, flat.Holdings)
	require.Len(t, flat.Orders, 1)

	periods, err := svc.Periods(ctx, "r1", 0, 0)
	require.NoError(t, err)
	require.Len(t, periods, 4)
	require.Equal(t, int64(1000), periods[0].Period)
}

// 同一期重发返回同一条记录；内容不同则冲突；缺价不记录任何内容；乱序被拒绝。
func TestAdvanceIsIdempotentAndRejectsBadInput(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	_, err := svc.CreateSession(ctx, CreateRequest{ReplayID: "r2"})
	require.NoError(t, err)
	req := buy("r2", 1000, "0.5", map[string]Quote{"A": q("100", "100")})
	first, err := svc.Advance(ctx, req)
	require.NoError(t, err)
	again, err := svc.Advance(ctx, req)
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Equal(t, first.Equity, again.Equity)

	changed := buy("r2", 1000, "0.4", map[string]Quote{"A": q("100", "100")})
	_, err = svc.Advance(ctx, changed)
	require.ErrorIs(t, err, ErrConflict)

	// 缺成交价：报错，且这一期没有被记录（修好后可以重发）。
	missing := buy("r2", 2000, "0.5", map[string]Quote{"A": q("100", "")})
	_, err = svc.Advance(ctx, missing)
	require.ErrorIs(t, err, ErrMissingQuote)
	session, err := svc.GetSession(ctx, "r2")
	require.NoError(t, err)
	require.Equal(t, int64(1000), session.LastPeriod)
	// 持有的标的缺估值价同样报错。
	_, err = svc.Advance(ctx, AdvanceRequest{ReplayID: "r2", Period: 2000, Decision: Decision{Kind: KindSkip}, Quotes: map[string]Quote{}})
	require.ErrorIs(t, err, ErrMissingQuote)

	_, err = svc.Advance(ctx, buy("r2", 500, "0.5", map[string]Quote{"A": q("100", "100")}))
	require.ErrorIs(t, err, ErrConflict, "早于已推进周期的请求被拒绝")
	_, err = svc.Advance(ctx, AdvanceRequest{ReplayID: "r2", Period: 3000, Decision: Decision{Kind: "NOPE"}})
	require.ErrorIs(t, err, ErrInvalidRequest)
	_, err = svc.Advance(ctx, buy("r2", 3000, "-0.5", map[string]Quote{"A": q("100", "100")}))
	require.ErrorIs(t, err, ErrInvalidRequest, "回放账本只做多现货")
	_, err = svc.Advance(ctx, buy("nope", 1000, "0.5", map[string]Quote{"A": q("100", "100")}))
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

// 资金不足：买单按可用现金部分成交，现金为零时拒绝。
func TestAdvanceLimitsBuysByCash(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	_, err := svc.CreateSession(ctx, CreateRequest{ReplayID: "r3", InitialCash: "1000", FeeRate: "0", SlippageBPS: "0"})
	require.NoError(t, err)
	res, err := svc.Advance(ctx, AdvanceRequest{ReplayID: "r3", Period: 1000, Quotes: map[string]Quote{"A": q("10", "10"), "B": q("10", "10")},
		Decision: Decision{Kind: KindUpdateTarget, Targets: []Target{{InstrumentID: "A", TargetWeight: "0.8"}, {InstrumentID: "B", TargetWeight: "0.8"}}}})
	require.NoError(t, err)
	require.Len(t, res.Orders, 2)
	require.Equal(t, "FILLED", res.Orders[0].Status)
	require.Equal(t, "PARTIAL", res.Orders[1].Status, "第二个标的只剩 200 现金：部分成交")
	require.Equal(t, "20", res.Orders[1].FilledQuantity)
	require.Equal(t, "0", res.Cash)
}

// 保持标的的数量不动；滑点让买价更高、卖价更低；结束后不能再推进，清理后会话消失。
func TestHoldSlippageCloseAndDelete(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	_, err := svc.CreateSession(ctx, CreateRequest{ReplayID: "r4", InitialCash: "10000", FeeRate: "0", SlippageBPS: "100"})
	require.NoError(t, err)
	first, err := svc.Advance(ctx, buy("r4", 1000, "0.5", map[string]Quote{"A": q("100", "100")}))
	require.NoError(t, err)
	require.Equal(t, "101", first.Orders[0].Price, "买价 = 成交价 × (1 + 100bps)")
	held, err := svc.Advance(ctx, AdvanceRequest{ReplayID: "r4", Period: 2000, Quotes: map[string]Quote{"A": q("100", "100")},
		Decision: Decision{Kind: KindUpdateTarget, Targets: []Target{{InstrumentID: "A", Hold: true}}}})
	require.NoError(t, err)
	require.Empty(t, held.Orders, "保持标的不调仓")
	require.Len(t, held.Holdings, 1)

	require.NoError(t, svc.Close(ctx, "r4"))
	require.NoError(t, svc.Close(ctx, "r4"), "重复结束不报错")
	_, err = svc.Advance(ctx, buy("r4", 3000, "0.5", map[string]Quote{"A": q("100", "100")}))
	require.ErrorIs(t, err, ErrConflict)

	deleted, err := svc.Delete(ctx, "r4")
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = svc.GetSession(ctx, "r4")
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	orders, err := svc.Store.ListReplayOrders(ctx, "r4", 0)
	require.NoError(t, err)
	require.Empty(t, orders, "清理会话同时清掉订单")
}
