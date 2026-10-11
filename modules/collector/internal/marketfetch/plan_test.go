package marketfetch

import (
	"strings"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func testPlan() *Plan {
	return &Plan{
		V:      1,
		Market: PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "binance", SourceID: "spot_http", MarketType: "spot"},
		Fetch:  PlanFetch{Concurrency: 10, RPS: 50, Burst: 50, FeedConcurrency: 10, StaggerMS: 1500},
		Datasets: []PlanDataset{
			{DatasetID: "bars_1m", Frequency: "1m", LatenessS: 15, Copies: []PlanCopy{
				{From: 1000, Hash: "old", Count: 2, Subjects: []PlanSubject{{ID: "BTC-USDT", Index: 0}, {ID: "ETH-USDT", Index: 1}}},
				{From: 2000, Hash: "new", Count: 3, Subjects: []PlanSubject{{ID: "BTC-USDT", Index: 0}, {ID: "ETH-USDT", Index: 2}}},
			}},
			// 另一个数据集写入同一批标的，序号与 hash 各自独立。
			{DatasetID: "bars_1m_b", Frequency: "1m", LatenessS: 30, Copies: []PlanCopy{
				{From: 1000, Hash: "b-hash", Count: 5, Subjects: []PlanSubject{{ID: "BTC-USDT", Index: 4}, {ID: "ETH-USDT", Index: 3}}},
			}},
			{DatasetID: "bars_1h", Frequency: "1h", LatenessS: 15, Copies: []PlanCopy{{From: 1000, Hash: "h", Count: 1, Subjects: []PlanSubject{{ID: "BTC-USDT", Index: 0}}}}},
		},
	}
}

func TestPlanEncodeDecodeRoundTripAndLimits(t *testing.T) {
	encoded, err := EncodePlan(testPlan())
	require.NoError(t, err)
	decoded, err := DecodePlan(encoded)
	require.NoError(t, err)
	require.Equal(t, testPlan(), decoded)

	_, err = DecodePlan("")
	require.Error(t, err)
	_, err = DecodePlan("not base64!")
	require.Error(t, err)
	bad := testPlan()
	bad.V = 2
	_, err = EncodePlan(bad)
	require.Error(t, err)
	bad = testPlan()
	bad.Datasets[0].Copies[1].From = bad.Datasets[0].Copies[0].From
	_, err = EncodePlan(bad)
	require.Error(t, err, "份的生效起点必须严格递增")
	bad = testPlan()
	bad.Datasets[0].Copies[0].Subjects[0].Index = 99
	_, err = EncodePlan(bad)
	require.Error(t, err, "序号必须小于总数")
}

func TestPlanSelectCopyBoundaries(t *testing.T) {
	dataset := testPlan().Datasets[0]
	_, ok := dataset.SelectCopy(time.Unix(999, 0))
	require.False(t, ok, "没有任何一份生效时必须跳过，而不是总选第一份")
	first, ok := dataset.SelectCopy(time.Unix(1000, 0))
	require.True(t, ok)
	require.Equal(t, "old", first.Hash)
	still, _ := dataset.SelectCopy(time.Unix(1999, 0))
	require.Equal(t, "old", still.Hash)
	next, _ := dataset.SelectCopy(time.Unix(2000, 0))
	require.Equal(t, "new", next.Hash, "目标周期等于生效起点时选新份")
}

func TestPlanBuildRequestsDueFrequenciesAndTargets(t *testing.T) {
	plan := testPlan()
	// 13:00:00 整点：1m 与 1h 都到期；目标周期是刚收盘那根的开始时间。
	scheduled := time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)
	requests, err := plan.BuildRequests("crypto", "fn-1", "req-1", scheduled, nil)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	byFrequency := map[string]PlannedRequest{}
	for _, request := range requests {
		byFrequency[request.Frequency] = request
	}
	minute := byFrequency["1m"]
	require.Equal(t, time.Date(2026, 10, 11, 12, 59, 0, 0, time.UTC), minute.Period)
	require.Equal(t, scheduled.Add(15*time.Second), minute.StorageDeadline, "截止 = 收盘 + 最小的允许迟到时间")
	require.Len(t, minute.Request.Items, 2, "同一标的的同一频率只抓一次")
	require.Len(t, minute.Request.Targets, 4, "每个目标数据集各一条写入目标")
	require.Equal(t, 15*time.Second, minute.StorageDeadline.Sub(scheduled))
	require.Equal(t, 1, int(minute.Request.Items[0].BarLimit))
	require.True(t, minute.Request.WriteIDByContent)
	require.NoError(t, minute.Request.validate())
	identities := map[string]string{}
	for _, target := range minute.Request.Targets {
		identities[target.DatasetID+"/"+target.InstanceID] = target.SeriesHash
		if target.DatasetID == "bars_1m_b" && strings.HasSuffix(target.InstanceID, "BTC-USDT") {
			require.EqualValues(t, 4, target.SeriesIndex, "各数据集各自编号")
			require.Equal(t, "b-hash", target.SeriesHash)
		}
	}
	require.Equal(t, "new", identities["bars_1m/1m|BTC-USDT"], "周期晚于第二份的生效起点时使用新份")
	hour := byFrequency["1h"]
	require.Equal(t, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), hour.Period)

	// 非整点：只有 1m 到期，秒数被截断到分钟。
	requests, err = plan.BuildRequests("crypto", "fn-1", "req-2", time.Date(2026, 10, 11, 13, 1, 7, 0, time.UTC), nil)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(t, "1m", requests[0].Frequency)
	require.Equal(t, time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC), requests[0].Period)
}

func TestPlanBuildRequestsUsesOldCopyBeforeEffectiveStart(t *testing.T) {
	plan := testPlan()
	plan.Datasets = plan.Datasets[:1]
	// 目标周期 1999（对齐到分钟前的旧份）：用旧份的 hash。
	period := time.Unix(1980, 0).UTC() // 1980 = 33 分钟整，周期开始时间
	requests, err := plan.BuildRequests("crypto", "fn", "r", period.Add(time.Minute), nil)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	for _, target := range requests[0].Request.Targets {
		require.Equal(t, "old", target.SeriesHash)
	}
}

func TestPlanStaggerIsDeterministicAndBounded(t *testing.T) {
	plan := testPlan()
	first := plan.StaggerDelay("function-a")
	require.Equal(t, first, plan.StaggerDelay("function-a"))
	require.LessOrEqual(t, first, 1500*time.Millisecond)
	plan.Fetch.StaggerMS = 0
	require.Zero(t, plan.StaggerDelay("function-a"))
}

func TestPlanExchangeSymbol(t *testing.T) {
	plan := testPlan()
	plan.SymbolExceptions = map[string]string{"1000PEPE-USDT": "1000PEPEUSDT"}
	require.Equal(t, "BTCUSDT", plan.ExchangeSymbol("BTC-USDT"))
	require.Equal(t, "1000PEPEUSDT", plan.ExchangeSymbol("1000PEPE-USDT"))
}

func TestWriteEventIDFollowsContent(t *testing.T) {
	req := Request{BatchID: "fn|1m|100", WriteIDByContent: true}
	rowA := &storagepb.RowFieldUpsert{Key: &storagepb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "A", Freq: "1m", DataTime: "2026-10-11T12:00:00Z"}}}}
	rowB := &storagepb.RowFieldUpsert{Key: &storagepb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "B", Freq: "1m", DataTime: "2026-10-11T12:00:00Z"}}}}
	one := writeEventID(req, []*storagepb.RowFieldUpsert{rowA})
	require.Equal(t, one, writeEventID(req, []*storagepb.RowFieldUpsert{rowA}), "同一内容的重试复用同一个写入 ID")
	two := writeEventID(req, []*storagepb.RowFieldUpsert{rowA, rowB})
	require.NotEqual(t, one, two, "内容变化（重复触发时多拿到了标的）必须换新写入 ID")
	require.Equal(t, two, writeEventID(req, []*storagepb.RowFieldUpsert{rowB, rowA}), "与行顺序无关")
	req.WriteIDByContent = false
	require.Equal(t, "fn|1m|100", writeEventID(req, []*storagepb.RowFieldUpsert{rowA}), "旧路径的写入 ID 不变")
}
