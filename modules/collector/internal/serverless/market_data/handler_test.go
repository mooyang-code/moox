package marketdata

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/require"
)

func testPlanEnv(t *testing.T) string {
	t.Helper()
	encoded, err := marketfetch.EncodePlan("crypto", &marketfetch.Plan{
		V:      1,
		Market: marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "binance", SourceID: "spot_http", MarketType: "spot"},
		Fetch:  marketfetch.PlanFetch{Concurrency: 10, RPS: 50, Burst: 50, FeedConcurrency: 10},
		Datasets: []marketfetch.PlanDataset{
			{DatasetID: "bars_1m", Frequency: "1m", LatenessS: 15, Copies: []marketfetch.PlanCopy{{From: 1000, Hash: "h1", Count: 2, Subjects: []marketfetch.PlanSubject{{ID: "BTC-USDT", Index: 0}, {ID: "ETH-USDT", Index: 1}}}}},
			{DatasetID: "bars_1h", Frequency: "1h", LatenessS: 15, Copies: []marketfetch.PlanCopy{{From: 1000, Hash: "h2", Count: 1, Subjects: []marketfetch.PlanSubject{{ID: "BTC-USDT", Index: 0}}}}},
		},
	})
	require.NoError(t, err)
	return encoded
}

func timerEvent(at string) json.RawMessage {
	raw, _ := json.Marshal(model.CloudFunctionEvent{Type: "Timer", TriggerName: "moox-market-fetch-timer", Time: at, Message: "market_fetch_timer_v1", RequestID: "request-1"})
	return raw
}

func planHandler(observed *[]marketfetch.Request, now *time.Time) *Handler {
	return &Handler{NewMarketFetch: func() *marketfetch.Handler {
		return &marketfetch.Handler{
			Now:        func() time.Time { return *now },
			NewStorage: func(string) (marketfetch.Storage, error) { return timerStorage{}, nil },
			Execute: func(_ context.Context, request marketfetch.Request, _ marketfetch.Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
				*observed = append(*observed, request)
				return &marketfetchpb.MarketFetchBatchCompleted{BatchId: request.BatchID, Status: "succeeded"}, nil
			},
		}
	}}
}

func TestHandlerTimerBuildsRequestsFromThePlanWithoutClaimOrCompletion(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_SCF_FUNCTION_NAME", "function-1")
	t.Setenv(marketfetch.PlanEnvName, testPlanEnv(t))
	var observed []marketfetch.Request
	now := time.Date(2026, 10, 11, 13, 0, 3, 0, time.UTC)
	handler := planHandler(&observed, &now)

	// 整点：1m 与 1h 同时到期，各执行一次，目标周期是刚收盘的那根 K 线的开始时间。
	response, err := handler.HandleRequest(context.Background(), timerEvent("2026-10-11T13:00:00Z"))
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success, response.(*model.Response).Message)
	require.Len(t, observed, 2)
	byFrequency := map[string]marketfetch.Request{}
	for _, request := range observed {
		byFrequency[request.Frequency] = request
	}
	minute, hour := byFrequency["1m"], byFrequency["1h"]
	require.Equal(t, "2026-10-11T12:59:00Z", minute.Items[0].TargetDataTime)
	require.Equal(t, "2026-10-11T12:00:00Z", hour.Items[0].TargetDataTime)
	require.True(t, minute.RequirePeriodCommit)
	require.True(t, minute.PlanRequest)
	require.Len(t, minute.Items, 2)
	require.Len(t, minute.Targets, 2)
	require.Equal(t, "h1", minute.Targets[0].SeriesHash)
	require.EqualValues(t, 2, minute.Targets[0].ExpectedCount)
	require.Len(t, hour.Items, 1)

	// 非整点：只有 1m 到期。
	observed = nil
	now = time.Date(2026, 10, 11, 13, 1, 8, 0, time.UTC)
	response, err = handler.HandleRequest(context.Background(), timerEvent("2026-10-11T13:01:07Z"))
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success)
	require.Len(t, observed, 1)
	require.Equal(t, "1m", observed[0].Frequency)
	require.Equal(t, "2026-10-11T13:00:00Z", observed[0].Items[0].TargetDataTime)
}

func TestHandlerTimerRejectsMissingOrInvalidScheduledTime(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv(marketfetch.PlanEnvName, testPlanEnv(t))
	var observed []marketfetch.Request
	now := time.Date(2026, 10, 11, 13, 0, 3, 0, time.UTC)
	handler := planHandler(&observed, &now)
	for _, at := range []string{"", "not-a-time"} {
		response, err := handler.HandleRequest(context.Background(), timerEvent(at))
		require.NoError(t, err)
		require.False(t, response.(*model.Response).Success)
		require.Contains(t, response.(*model.Response).Message, "invalid_timer_event")
	}
	require.Empty(t, observed, "非法计划时间不得回退到当前时间执行")
}

func TestHandlerTimerSkipsDatasetsWhoseFirstCopyHasNotStarted(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_SCF_FUNCTION_NAME", "function-1")
	t.Setenv(marketfetch.PlanEnvName, testPlanEnv(t))
	var observed []marketfetch.Request
	now := time.Date(2026, 10, 11, 13, 0, 3, 0, time.UTC)
	handler := planHandler(&observed, &now)
	// 计划的生效起点是 1970-01-01T00:16:40Z（1000 秒），更早的周期没有可用的份。
	response, err := handler.HandleRequest(context.Background(), timerEvent("1970-01-01T00:16:00Z"))
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success)
	require.Equal(t, "no_work", response.(*model.Response).Message)
	require.Empty(t, observed)
}

func TestHandlerTimerExpiredEventIsNotFetched(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_SCF_FUNCTION_NAME", "function-1")
	t.Setenv(marketfetch.PlanEnvName, testPlanEnv(t))
	var observed []marketfetch.Request
	now := time.Date(2026, 10, 11, 13, 1, 20, 0, time.UTC) // 13:01 的周期截止是 13:01:15
	handler := planHandler(&observed, &now)
	response, err := handler.HandleRequest(context.Background(), timerEvent("2026-10-11T13:01:00Z"))
	require.NoError(t, err)
	require.False(t, response.(*model.Response).Success)
	require.Empty(t, observed, "迟到到截止之后的事件不再抓取，数据交给补采")
}

func TestHandlerManualInvokeWritesRowsOnly(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	var observed marketfetch.Request
	handler := &Handler{NewMarketFetch: func() *marketfetch.Handler {
		return &marketfetch.Handler{
			NewStorage: func(string) (marketfetch.Storage, error) { return timerStorage{}, nil },
			Execute: func(_ context.Context, request marketfetch.Request, _ marketfetch.Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
				observed = request
				return &marketfetchpb.MarketFetchBatchCompleted{BatchId: request.BatchID, Status: "succeeded"}, nil
			},
		}
	}}
	req := marketfetch.Request{
		BatchID: "backfill-1", BatchKind: domain.BatchKindGapRepair, SpaceID: "crypto", DatasetID: "bars-a", Frequency: "1m",
		Provider: "binance", SourceID: "spot_http", MarketType: "spot",
		Items: []domain.CollectionItem{{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline", DatasetID: "bars-a", Frequency: "1m", StartTime: "2026-10-11T00:00:00Z", BarLimit: 100}},
	}
	rawReq, err := json.Marshal(req)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(rawReq, &data))
	raw, err := json.Marshal(model.CloudFunctionEvent{Action: model.EventActionMarketFetch, Source: "moox_backfill", RequestID: "request-backfill", Data: data})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success)
	require.Equal(t, "backfill-1", observed.BatchID)
}

func TestHandlerRejectsRemovedInstrumentSnapshotAction(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")

	handler := &Handler{NewMarketFetch: func() *marketfetch.Handler { return &marketfetch.Handler{} }}
	raw, err := json.Marshal(model.CloudFunctionEvent{Action: model.EventAction("instrument_snapshot"), RequestID: "action-only"})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.False(t, response.(*model.Response).Success)
	require.Contains(t, response.(*model.Response).Message, "unknown_event_type")
}

type timerStorage struct{}

func (timerStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error { return nil }
