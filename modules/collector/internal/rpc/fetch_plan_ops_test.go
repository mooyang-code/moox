package rpc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func newReregisterFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	return &Service{persistence: db, taskRepo: db.Tasks()}, db
}

const reregisterPlan = `{"datasets":{` +
	`"ds1|1m":{"dataset_id":"ds1","frequency":"1m","hash":"h1","count":2,"changed":true,"hash_changed":true,"p":100},` +
	`"ds2|1h":{"dataset_id":"ds2","frequency":"1h","hash":"h2","count":3},` +
	`"gone|1m":{"dataset_id":"gone","frequency":"1m","hash":"h3","gone":true}},"functions":{}}`

func applyPlan(t *testing.T, db *store.Store, spaceID, plan string) *store.FetchPlanRecord {
	t.Helper()
	record, err := db.FetchPlans().Create(context.Background(), spaceID, plan, "{}")
	require.NoError(t, err)
	require.NoError(t, db.FetchPlans().Update(context.Background(), record.ID, store.FetchPlanApplied, "{}", ""))
	return record
}

// 清空 Storage 之后：已生效计划里的数据集 hash 被清空（下一轮对账据此重新登记快照），补采作业与水位被重置，
// 水位推到现在而不是留空（留空会从最早的计划起点回补历史）。
func TestReregisterFetchPlanClearsSnapshotsAndResetsBackfill(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()
	applied := applyPlan(t, db, "crypto", reregisterPlan)
	other := applyPlan(t, db, "stockcn", reregisterPlan)
	oldWatermark := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	require.NoError(t, db.Backfill().SetWatermark(ctx, "crypto", "ds1", "1m", oldWatermark))
	require.NoError(t, db.Backfill().SetWatermark(ctx, "stockcn", "ds1", "1m", oldWatermark))
	require.NoError(t, db.Backfill().CreateJob(ctx, store.BackfillJob{JobID: "job-crypto", SpaceID: "crypto", From: oldWatermark, To: oldWatermark.Add(time.Minute)}, []store.BackfillChunk{{DatasetID: "ds1", Frequency: "1m", SubjectID: "BTC", From: oldWatermark, To: oldWatermark.Add(time.Minute), ChunkID: "c1"}}))
	require.NoError(t, db.Backfill().CreateJob(ctx, store.BackfillJob{JobID: "job-stockcn", SpaceID: "stockcn", From: oldWatermark, To: oldWatermark.Add(time.Minute)}, []store.BackfillChunk{{DatasetID: "ds1", Frequency: "1m", SubjectID: "600000", From: oldWatermark, To: oldWatermark.Add(time.Minute), ChunkID: "c2"}}))

	before := time.Now().UTC().Add(-time.Second)
	rsp, err := service.ReregisterFetchPlan(ctx, &pb.ReregisterFetchPlanReq{SpaceId: " crypto "})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, []string{"ds1|1m", "ds2|1h"}, rsp.GetDatasets(), "已退役的数据集不动")
	require.EqualValues(t, 1, rsp.GetBackfillJobs())

	latest, err := db.FetchPlans().LatestApplied(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, applied.ID, latest.ID, "改写的是已生效计划本身，不新建计划")
	content, err := marketfetch.DecodeFetchPlanContent(latest.Plan)
	require.NoError(t, err)
	require.Empty(t, content.Datasets["ds1|1m"].Hash)
	require.Empty(t, content.Datasets["ds2|1h"].Hash)
	require.Equal(t, "h3", content.Datasets["gone|1m"].Hash)
	require.EqualValues(t, 100, content.Datasets["ds1|1m"].P, "其余字段保持原样")

	for _, key := range [][2]string{{"ds1", "1m"}, {"ds2", "1h"}} {
		mark, err := db.Backfill().Watermark(ctx, "crypto", key[0], key[1])
		require.NoError(t, err)
		require.True(t, mark.After(before), "%s 的水位推到现在", key)
	}
	job, chunks, err := db.Backfill().GetJob(ctx, "job-crypto")
	require.NoError(t, err)
	require.Nil(t, job, "该空间的补采作业被清掉")
	require.Empty(t, chunks, "连同分块")
	job, chunks, err = db.Backfill().GetJob(ctx, "job-stockcn")
	require.NoError(t, err)
	require.NotNil(t, job, "别的空间不受影响")
	require.Len(t, chunks, 1)
	mark, err := db.Backfill().Watermark(ctx, "stockcn", "ds1", "1m")
	require.NoError(t, err)
	require.True(t, mark.Equal(oldWatermark))
	untouched, err := marketfetch.DecodeFetchPlanContent(other.Plan)
	require.NoError(t, err)
	require.Equal(t, "h1", untouched.Datasets["ds1|1m"].Hash)
}

// 还有未生效的计划时不动手：它生效之前已生效计划不能改写；没有已生效计划同样拒绝。
func TestReregisterFetchPlanRefusesWithoutAppliedPlanOrWithAnOpenPlan(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()

	rsp, err := service.ReregisterFetchPlan(ctx, &pb.ReregisterFetchPlanReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = service.ReregisterFetchPlan(ctx, &pb.ReregisterFetchPlanReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())

	applied := applyPlan(t, db, "crypto", reregisterPlan)
	_, err = db.FetchPlans().Create(ctx, "crypto", reregisterPlan, "{}")
	require.NoError(t, err)
	rsp, err = service.ReregisterFetchPlan(ctx, &pb.ReregisterFetchPlanReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "未生效")
	latest, err := db.FetchPlans().LatestApplied(ctx, "crypto")
	require.NoError(t, err)
	content, err := marketfetch.DecodeFetchPlanContent(latest.Plan)
	require.NoError(t, err)
	require.Equal(t, applied.ID, latest.ID)
	require.Equal(t, "h1", content.Datasets["ds1|1m"].Hash, "拒绝时不改计划")
}

// 运行状态：数据集健康决定总体等级；计划下发失败是空间级的异常。
func TestGetCollectorStatusSummarizesPlanAndDatasetHealth(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()
	applyPlan(t, db, "crypto", reregisterPlan)
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "t1", TaskName: "Binance 现货 K 线 1m", DataType: "kline", ResultDatasetID: "ds1", ResultViewID: "view_ds1"}))

	rsp, err := service.GetCollectorStatus(ctx, &pb.GetCollectorStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.NotNil(t, rsp.GetApplied())
	require.Nil(t, rsp.GetOpen())
	require.Len(t, rsp.GetDatasets(), 1, "只列出正在采集（已登记生效点）的数据集")
	require.Equal(t, "ds1", rsp.GetDatasets()[0].GetDatasetId())
	require.Equal(t, "Binance 现货 K 线 1m", rsp.GetDatasets()[0].GetDatasetName(), "展示名取所属采集任务名")
	require.Equal(t, "error", rsp.GetLevel(), "从未巡检过，落后远超阈值")
	require.Contains(t, rsp.GetReasons()[0], "Binance 现货 K 线 1m：自动巡检已停")

	require.NoError(t, db.Backfill().SetWatermark(ctx, "crypto", "ds1", "1m", time.Now().UTC()))
	rsp, err = service.GetCollectorStatus(ctx, &pb.GetCollectorStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, "ok", rsp.GetLevel(), rsp.GetReasons())
	require.Empty(t, rsp.GetReasons())
	require.NotEmpty(t, rsp.GetDatasets()[0].GetInspectedUntil())

	record, err := db.FetchPlans().Create(ctx, "crypto", reregisterPlan, "{}")
	require.NoError(t, err)
	require.NoError(t, db.FetchPlans().Update(ctx, record.ID, store.FetchPlanPending, "{}", "deploy failed"))
	rsp, err = service.GetCollectorStatus(ctx, &pb.GetCollectorStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.NotNil(t, rsp.GetOpen())
	require.Equal(t, "error", rsp.GetLevel())
	require.Contains(t, rsp.GetReasons()[0], "下发失败：deploy failed")

	invalid, err := service.GetCollectorStatus(ctx, &pb.GetCollectorStatusReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, invalid.GetRetInfo().GetCode())
}

// 函数详情：每个数据集上本函数负责的标的取当前生效的那份快照。
func TestPlanFunctionsListSubjectsPerDataset(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	options := marketfetch.PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-nanjing"}}, DefaultLatenessS: 15, Fetch: marketfetch.PlanFetch{Concurrency: 5}}
	desired, err := marketfetch.BuildDesiredState(options, []marketfetch.TaskGroup{{
		Provider: "binance", SourceID: "binance", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m",
		Subjects: []string{"BTC-USDT", "ETH-USDT"},
	}}, nil)
	require.NoError(t, err)
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return base.Unix(), nil })
	require.NoError(t, err)
	raw, err := json.Marshal(content)
	require.NoError(t, err)

	functions, err := planFunctions(&store.FetchPlanRecord{ID: 1, SpaceID: "crypto", Status: store.FetchPlanApplied, Plan: string(raw), Progress: "{}"})
	require.NoError(t, err)
	require.Len(t, functions, 1)
	function := functions[0]
	require.Equal(t, []string{"ds|1m"}, function.GetDatasets())
	require.EqualValues(t, 2, function.GetSubjectCount())
	require.Len(t, function.GetDatasetSubjects(), 1)
	detail := function.GetDatasetSubjects()[0]
	require.Equal(t, "ds|1m", detail.GetDataset())
	require.ElementsMatch(t, []string{"BTC-USDT", "ETH-USDT"}, detail.GetSubjects())
	require.Equal(t, base.Format(time.RFC3339), detail.GetEffectiveFrom())
}
