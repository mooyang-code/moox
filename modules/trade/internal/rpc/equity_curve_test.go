package rpc

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

func TestEquityCurveRangePicksBucketBySpan(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	day := (24 * time.Hour).Milliseconds()
	tests := []struct {
		name         string
		from, to     int64
		wantFrom     int64
		wantTo       int64
		wantBucket   time.Duration
		wantAtLeast  time.Duration
		wantCappedTo int
	}{
		{name: "起止为 0 默认最近 30 天、15 分钟桶", wantFrom: now.UnixMilli() - 30*day, wantTo: now.UnixMilli(), wantBucket: 15 * time.Minute},
		{name: "只给终点，起点是终点前 30 天", to: now.UnixMilli() - day, wantFrom: now.UnixMilli() - 31*day, wantTo: now.UnixMilli() - day, wantBucket: 15 * time.Minute},
		{name: "2 天内不降采样（1 分钟桶）", from: now.UnixMilli() - 2*day, wantFrom: now.UnixMilli() - 2*day, wantTo: now.UnixMilli(), wantBucket: time.Minute},
		{name: "超过 30 天按小时", from: now.UnixMilli() - 60*day, wantFrom: now.UnixMilli() - 60*day, wantTo: now.UnixMilli(), wantBucket: time.Hour},
		{name: "跨度太长时放宽桶宽，点数不超过 3000", from: now.UnixMilli() - 400*day, wantFrom: now.UnixMilli() - 400*day, wantTo: now.UnixMilli(), wantAtLeast: time.Hour, wantCappedTo: equityCurveMaxPoints},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to, bucket := equityCurveRange(tt.from, tt.to, now)
			if from != tt.wantFrom || to != tt.wantTo {
				t.Fatalf("起止 = %d..%d，期望 %d..%d", from, to, tt.wantFrom, tt.wantTo)
			}
			if tt.wantBucket > 0 && bucket != tt.wantBucket.Milliseconds() {
				t.Fatalf("桶宽 = %d 毫秒，期望 %v", bucket, tt.wantBucket)
			}
			if tt.wantCappedTo > 0 {
				if bucket < tt.wantAtLeast.Milliseconds() || int((to-from)/bucket)+2 > tt.wantCappedTo {
					t.Fatalf("桶宽 %d 毫秒装不下：最多 %d 点", bucket, tt.wantCappedTo)
				}
			}
		})
	}
}

func openCurveStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tradeStore.Close() })
	account := store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: "account-1", Name: "paper",
		Exchange: string(exchange.ExchangeBinance), Markets: []string{string(exchange.MarketTypeSpot)},
		ExecutionMode: string(exchange.ExecutionModePaper), SettlementAsset: "USDT",
		Status: string(exchange.AccountStatusEnabled), PaperConfig: &store.PaperAccountConfigRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", InitialBalance: "1000",
			MakerFeeRate: "0", TakerFeeRate: "0", SlippageBPS: "0",
		},
	}
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error { return tx.CreateTradingAccount(account) }); err != nil {
		t.Fatal(err)
	}
	return tradeStore
}

// 45 天、每分钟一点：默认只返回最近 30 天，点数不超过 3000，每桶取最后一点，首尾点保留。
func TestConsoleQueryEquityCurveDownsamplesMinutePoints(t *testing.T) {
	tradeStore := openCurveStore(t)
	last := time.Now().Truncate(time.Minute)
	first := last.Add(-45 * 24 * time.Hour)
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for at := first; !at.After(last); at = at.Add(time.Minute) {
			ms := at.UnixMilli()
			if err := tx.UpsertAccountEquityPoint(store.EquityPointRecord{
				SpaceID: "space-1", TradingAccountID: "account-1", BucketTime: ms,
				Equity: strconv.FormatInt(ms, 10), AvailableFunds: "0", UsedMargin: "0", SourceTime: ms,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := &ConsoleServer{Store: tradeStore}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")

	response, err := server.QueryEquityCurve(ctx, &tradepb.QueryEquityCurveReq{TradingAccountId: "account-1"})
	if err != nil || response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
		t.Fatalf("query = %+v err=%v", response, err)
	}
	points := response.GetPoints()
	if len(points) == 0 || len(points) > equityCurveMaxPoints {
		t.Fatalf("点数 = %d", len(points))
	}
	windowStart := time.Now().Add(-equityCurveDefaultSpan).UnixMilli()
	if points[0].GetBucketTime() < windowStart-60_000 || points[0].GetBucketTime() > windowStart+equityCurveQuarterHour.Milliseconds() {
		t.Fatalf("起点应是最近 30 天里的第一点：%d（窗口起点 %d）", points[0].GetBucketTime(), windowStart)
	}
	if got := points[len(points)-1].GetBucketTime(); got != last.UnixMilli() {
		t.Fatalf("最后一点 = %d，期望 %d", got, last.UnixMilli())
	}
	bucket := equityCurveQuarterHour.Milliseconds()
	for i, point := range points[1 : len(points)-1] {
		at := point.GetBucketTime()
		if (at+60_000)/bucket == at/bucket {
			t.Fatalf("第 %d 点 %d 不是所在桶的最后一点", i+1, at)
		}
	}

	// 显式起止：45 天按小时取最后一点，首尾点保留。
	explicit, err := server.QueryEquityCurve(ctx, &tradepb.QueryEquityCurveReq{
		TradingAccountId: "account-1", StartTime: first.UnixMilli(), EndTime: last.UnixMilli(),
	})
	if err != nil || explicit.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
		t.Fatalf("query = %+v err=%v", explicit, err)
	}
	points = explicit.GetPoints()
	if len(points) == 0 || len(points) > equityCurveMaxPoints {
		t.Fatalf("点数 = %d", len(points))
	}
	if points[0].GetBucketTime() != first.UnixMilli() || points[len(points)-1].GetBucketTime() != last.UnixMilli() {
		t.Fatalf("首尾点没有保留：%d..%d", points[0].GetBucketTime(), points[len(points)-1].GetBucketTime())
	}
	hour := time.Hour.Milliseconds()
	for _, point := range points[1 : len(points)-1] {
		if at := point.GetBucketTime(); (at+60_000)/hour == at/hour {
			t.Fatalf("点 %d 不是所在小时桶的最后一点", at)
		}
	}
}
