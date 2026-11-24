package tradingstatus

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	tags    []*pb.Tag
	members map[string][]*pb.TagMember // 标签 → active 成员
	written map[string][]*pb.TagMemberTradingStatus
}

func (f *fakeStore) ListTags(context.Context) ([]*pb.Tag, error) { return f.tags, nil }
func (f *fakeStore) ListTagMembers(_ context.Context, _, tagID, status string) ([]*pb.TagMember, error) {
	if status != "active" {
		return nil, fmt.Errorf("status = %q", status)
	}
	return f.members[tagID], nil
}
func (f *fakeStore) SetTagMemberTradingStatus(_ context.Context, _, tagID string, items []*pb.TagMemberTradingStatus) (int, error) {
	if f.written == nil {
		f.written = map[string][]*pb.TagMemberTradingStatus{}
	}
	f.written[tagID] = append(f.written[tagID], items...)
	for _, item := range items {
		for _, member := range f.members[tagID] {
			if member.GetSubject().GetSubjectId() == item.GetSubjectId() {
				member.TradingStatus = item.GetTradingStatus()
			}
		}
	}
	return len(items), nil
}

func tag(space, id, marketType string) *pb.Tag {
	return &pb.Tag{SpaceId: space, TagId: id, Mode: "auto", Attributes: map[string]string{"market_type": marketType}}
}

func members(pairs ...string) []*pb.TagMember {
	out := make([]*pb.TagMember, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, &pb.TagMember{Status: "active", TradingStatus: pairs[i+1], Subject: &pb.Subject{SubjectId: pairs[i]}})
	}
	return out
}

type fixedSource struct {
	out map[string]string
	err error
}

func (s fixedSource) Check(context.Context, []string) (map[string]string, error) { return s.out, s.err }

func writtenPairs(items []*pb.TagMemberTradingStatus) map[string]string {
	out := map[string]string{}
	for _, item := range items {
		out[item.GetSubjectId()] = item.GetTradingStatus()
	}
	return out
}

// 同一空间、同一 market_type 的每个标签都写上；只写有变化的；来源没给的保留原值；别的市场、别的空间的标签不碰；
// 复牌写回 trading。
func TestRunnerWritesChangedMembersOfEveryTagOfTheMarket(t *testing.T) {
	store := &fakeStore{
		tags: []*pb.Tag{tag("stockcn", "cn_hs300", "equity"), tag("stockcn", "cn_canary", "equity"), tag("crypto", "binance_spot", "spot")},
		members: map[string][]*pb.TagMember{
			"cn_hs300":     members("600000.XSHG", "trading", "601059.XSHG", "trading", "000001.XSHE", "suspended", "000002.XSHE", "trading"),
			"cn_canary":    members("601059.XSHG", ""),
			"binance_spot": members("BTC-USDT", "trading"),
		},
	}
	source := fixedSource{out: map[string]string{"600000.XSHG": "trading", "601059.XSHG": "delisted", "000001.XSHE": "trading"}}
	runner := &Runner{Store: store, Jobs: []Job{{SpaceID: "stockcn", MarketType: "equity", Cron: "5 9 * * *", Timezone: "Asia/Shanghai", Source: source}}}
	require.NoError(t, runner.Run(context.Background(), "stockcn", ""))
	require.Equal(t, map[string]string{"601059.XSHG": "delisted", "000001.XSHE": "trading"}, writtenPairs(store.written["cn_hs300"]))
	require.Equal(t, map[string]string{"601059.XSHG": "delisted"}, writtenPairs(store.written["cn_canary"]))
	require.NotContains(t, store.written, "binance_spot")

	// 再跑一次：没有变化，不写。
	store.written = nil
	require.NoError(t, runner.Run(context.Background(), "stockcn", ""))
	require.Empty(t, store.written)
}

// 定时按 (now-1m, now] 窗口触发，cron 用任务的时区。
func TestRunnerRunsDueJobsByCronWindow(t *testing.T) {
	calls := 0
	source := sourceFunc(func(context.Context, []string) (map[string]string, error) {
		calls++
		return map[string]string{"A": "trading"}, nil
	})
	store := &fakeStore{tags: []*pb.Tag{tag("stockcn", "t", "equity")}, members: map[string][]*pb.TagMember{"t": members("A", "trading")}}
	runner := &Runner{Store: store, Jobs: []Job{{SpaceID: "stockcn", MarketType: "equity", Cron: "5 9,12 * * 1-5", Timezone: "Asia/Shanghai", Source: source}}}
	monday := time.Date(2026, 11, 23, 1, 5, 30, 0, time.UTC) // 北京时间周一 09:05:30
	require.NoError(t, runner.RunDueAt(context.Background(), monday))
	require.NoError(t, runner.RunDueAt(context.Background(), monday.Add(time.Minute)))
	require.NoError(t, runner.RunDueAt(context.Background(), monday.Add(3*time.Hour)))    // 12:05:30
	require.NoError(t, runner.RunDueAt(context.Background(), monday.Add(5*24*time.Hour))) // 周六
	require.Equal(t, 2, calls)
}

type sourceFunc func(context.Context, []string) (map[string]string, error)

func (f sourceFunc) Check(ctx context.Context, subjects []string) (map[string]string, error) {
	return f(ctx, subjects)
}

// 整体维护保护：一个正常交易的都没有，或本轮新变成不可交易的超过当前可交易的一半，整轮不写；长期停牌的成员不进比例。
func TestRunnerSkipsTheRoundDuringExchangeMaintenance(t *testing.T) {
	run := func(current []*pb.TagMember, found map[string]string) map[string][]*pb.TagMemberTradingStatus {
		store := &fakeStore{tags: []*pb.Tag{tag("crypto", "binance_spot", "spot")}, members: map[string][]*pb.TagMember{"binance_spot": current}}
		runner := &Runner{Store: store, Jobs: []Job{{SpaceID: "crypto", MarketType: "spot", Cron: "*/10 * * * *", Timezone: "UTC", Source: fixedSource{out: found}}}}
		require.NoError(t, runner.Run(context.Background(), "crypto", "spot"))
		return store.written
	}
	require.Empty(t, run(members("A", "trading", "B", "trading"), map[string]string{"A": "suspended", "B": "suspended"}), "全部暂停")
	require.Empty(t, run(members("A", "trading", "B", "trading", "C", "trading"), map[string]string{"A": "suspended", "B": "suspended", "C": "trading"}), "过半新变暂停")
	require.Equal(t, map[string]string{"A": "suspended"}, writtenPairs(run(members("A", "trading", "B", "trading", "C", "trading"), map[string]string{"A": "suspended", "B": "trading", "C": "trading"})["binance_spot"]))
	// 已经长期停牌的很多，这一轮只新停一只：照常写。
	written := run(members("A", "trading", "B", "trading", "S1", "suspended", "S2", "suspended", "S3", "suspended"),
		map[string]string{"A": "suspended", "B": "trading", "S1": "suspended", "S2": "suspended", "S3": "suspended"})
	require.Equal(t, map[string]string{"A": "suspended"}, writtenPairs(written["binance_spot"]))
}

type fakeTencent struct {
	quotes map[string]cnstock.Quote
	fail   map[string]bool // 批次第一只 → 这一批失败
	calls  [][]string
}

func (f *fakeTencent) answer(batch []string) (map[string]cnstock.Quote, error) {
	f.calls = append(f.calls, batch)
	if f.fail[batch[0]] {
		return nil, errors.New("timeout")
	}
	out := map[string]cnstock.Quote{}
	for _, id := range batch {
		if q, ok := f.quotes[id]; ok {
			out[id] = q
		}
	}
	return out, nil
}
func (f *fakeTencent) Snapshot(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
	return f.answer(ids)
}
func (f *fakeTencent) SnapshotHK(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
	return f.answer(ids)
}
func (f *fakeTencent) StatusUS(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
	return f.answer(ids)
}

// 腾讯快照：退市优先于停牌，其余正常；每 60 只一批，失败的批次只丢这一批；上游不认识的不在结果里。
func TestTencentSourceMapsStatusesBatchByBatch(t *testing.T) {
	ids := make([]string, 0, 130)
	quotes := map[string]cnstock.Quote{}
	for i := 0; i < 130; i++ {
		id := fmt.Sprintf("%06d.XSHG", 600000+i)
		ids = append(ids, id)
		quotes[id] = cnstock.Quote{InstrumentID: id}
	}
	quotes[ids[1]] = cnstock.Quote{Halted: true}
	quotes[ids[2]] = cnstock.Quote{Delisted: true, Halted: true}
	delete(quotes, ids[3])
	client := &fakeTencent{quotes: quotes, fail: map[string]bool{ids[60]: true}}
	out, err := TencentSource{Client: client, Market: MarketCN}.Check(context.Background(), ids)
	require.Error(t, err, "有一批失败要报出来")
	require.Len(t, client.calls, 3)
	require.Equal(t, Trading, out[ids[0]])
	require.Equal(t, Suspended, out[ids[1]])
	require.Equal(t, Delisted, out[ids[2]])
	require.NotContains(t, out, ids[3], "上游不认识：保留原值")
	require.NotContains(t, out, ids[60], "失败的一批：保留原值")
	require.Equal(t, Trading, out[ids[120]], "失败的批次之后照常")
}

// 币安：TRADING 优先（在下架名单里也不标）；永续交割、结算记退市；现货非 TRADING 且下架时间已过记退市（PAPER_ONLY 由查询排除）；
// 其余非 TRADING 记停牌；列表里没有的不在结果里。
func TestBinanceSourceAppliesTheFixedOrder(t *testing.T) {
	list := func(items ...marketdata.Instrument) InstrumentLister {
		return func(context.Context) ([]marketdata.Instrument, error) { return items, nil }
	}
	delisted := func(context.Context, time.Time) (map[string]bool, error) {
		return map[string]bool{"GONEUSDT": true, "BACKUSDT": true}, nil
	}
	spot := BinanceSource{MarketType: "spot", Delisted: delisted, List: list(
		marketdata.Instrument{SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT", TradingStatus: "TRADING"},
		marketdata.Instrument{SubjectID: "BACK-USDT", ProviderSymbol: "BACKUSDT", TradingStatus: "TRADING"},
		marketdata.Instrument{SubjectID: "GONE-USDT", ProviderSymbol: "GONEUSDT", TradingStatus: "BREAK"},
		marketdata.Instrument{SubjectID: "HALT-USDT", ProviderSymbol: "HALTUSDT", TradingStatus: "BREAK"},
	)}
	out, err := spot.Check(context.Background(), []string{"BTC-USDT", "BACK-USDT", "GONE-USDT", "HALT-USDT", "MISSING-USDT"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"BTC-USDT": Trading, "BACK-USDT": Trading, "GONE-USDT": Delisted, "HALT-USDT": Suspended}, out)

	swap := BinanceSource{MarketType: "swap", Delisted: delisted, List: list(
		marketdata.Instrument{SubjectID: "BTC-USDT", TradingStatus: "TRADING"},
		marketdata.Instrument{SubjectID: "DLV-USDT", TradingStatus: "DELIVERING"},
		marketdata.Instrument{SubjectID: "SET-USDT", TradingStatus: "SETTLING"},
		marketdata.Instrument{SubjectID: "PEN-USDT", TradingStatus: "PENDING_TRADING"},
		marketdata.Instrument{SubjectID: "GONE-USDT", ProviderSymbol: "GONEUSDT", TradingStatus: "BREAK"},
	)}
	out, err = swap.Check(context.Background(), []string{"BTC-USDT", "DLV-USDT", "SET-USDT", "PEN-USDT", "GONE-USDT"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"BTC-USDT": Trading, "DLV-USDT": Delisted, "SET-USDT": Delisted, "PEN-USDT": Suspended, "GONE-USDT": Suspended}, out,
		"下架名单只用于现货")
}
