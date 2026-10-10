package marketdata

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestHandlerRoutesTimerToClaimedMarketFetch(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_MARKET_FETCH_BINDING_HASH", "binding-hash")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "storage.local:11003")
	t.Setenv("MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "runtime.local:11003")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "collector-node")
	t.Setenv("MOOX_SCF_FUNCTION_NAME", "function-1")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "0")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "1")

	var observed marketfetch.Request
	period := "2026-09-01T07:59:00Z"
	claimed, err := json.Marshal(marketfetch.Request{
		BatchID: "claimed-batch", BatchKind: domain.BatchKindRealtime, SpaceID: "stockcn", DatasetID: "dataset_stockcn_equity_kline_1m",
		Frequency: "1m", Provider: "tencent", SourceID: "stockcn_http", MarketType: "equity", FunctionName: "function-1",
		GroupID: 0, GroupCount: 1, BindingHash: "binding-hash", RequirePeriodCommit: true,
		Items:   []domain.CollectionItem{{InstanceID: "instance-1", SubjectID: "600000.XSHG", Symbol: "600000", Provider: "tencent", SourceID: "stockcn_http", MarketType: "equity", DataType: "kline", DatasetID: "dataset_stockcn_equity_kline_1m", Frequency: "1m", TargetDataTime: period, SeriesIndex: 0, SeriesHash: "series-hash", ExpectedCount: 1, RequirePeriodCommit: true}},
		Targets: []domain.WriteTarget{{ID: "target-1", SpaceID: "stockcn", InstanceID: "instance-1", TaskID: "task-1", DatasetID: "dataset_stockcn_equity_kline_1m", SeriesIndex: 0, SeriesHash: "series-hash", ExpectedCount: 1, Frequency: "1m", TargetDataTime: period}},
	})
	require.NoError(t, err)
	handler := &Handler{
		NewMarketFetch: func() *marketfetch.Handler {
			return &marketfetch.Handler{
				TimerRuntimeClient: serverlessTimerRuntimeClientFunc(func(context.Context, *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
					return &collectorpb.ClaimTimerBatchRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}, Claimed: true, RequestJson: claimed}, nil
				}),
				NewStorage: func(string, string) (marketfetch.Storage, error) {
					return timerStorage{}, nil
				},
				Publish: func(context.Context, marketfetch.Request, proto.Message) error { return nil },
				Execute: func(_ context.Context, request marketfetch.Request, _ marketfetch.Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
					observed = request
					return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
				},
			}
		},
	}

	raw, err := json.Marshal(model.CloudFunctionEvent{
		Type: "Timer", TriggerName: "moox-market-fetch-timer",
		Time: "2026-09-01T08:00:00Z", Message: "market_fetch_timer_v1",
		RequestID: "request-1",
	})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success)
	require.Equal(t, "tencent", observed.Provider)
	require.Equal(t, "stockcn_http", observed.SourceID)
	require.Equal(t, "stockcn_http", observed.Items[0].SourceID)
	require.Equal(t, "claimed-batch", observed.BatchID)
}

func TestHandlerSchedulerInvokePublishesCompletionOnTimerConfiguredNode(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	// The static binding identifies a managed Timer-capable node; subjects are
	// never the source of execution membership.
	t.Setenv("MOOX_MARKET_FETCH_BINDING_HASH", "binding-hash")
	t.Setenv("MOOX_MARKET_FETCH_MODE", "kline")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://storage-runtime:12004")

	published := 0
	var observed marketfetch.Request
	var observedMarket string
	handler := &Handler{
		NewMarketFetch: func() *marketfetch.Handler {
			return &marketfetch.Handler{
				NewStorage: func(market, _ string) (marketfetch.Storage, error) {
					observedMarket = market
					return timerStorage{}, nil
				},
				Publish: func(_ context.Context, request marketfetch.Request, _ proto.Message) error {
					published++
					observed = request
					return nil
				},
				Execute: func(_ context.Context, request marketfetch.Request, _ marketfetch.Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
					observed = request
					return &marketfetchpb.MarketFetchBatchCompleted{BatchId: request.BatchID, Status: "succeeded"}, nil
				},
			}
		},
	}

	req := marketfetch.Request{
		BatchID: "batch-shared", BatchKind: "realtime", SpaceID: "crypto", DatasetID: "bars-a", Frequency: "1m",
		Provider: "binance", SourceID: "spot_http", MarketType: "spot",
		Items: []domain.CollectionItem{{InstanceID: "instance-1", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline", DatasetID: "bars-a", Frequency: "1m", BarLimit: 1}},
	}
	rawReq, err := json.Marshal(req)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(rawReq, &data))
	raw, err := json.Marshal(model.CloudFunctionEvent{
		Action: model.EventActionMarketFetch, Source: model.EventSourceCollectorScheduler,
		RequestID: "request-shared", Data: data,
	})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success)
	require.Equal(t, 1, published, "durable scheduler invokes must publish BatchCompleted")
	require.Equal(t, "batch-shared", observed.BatchID)
	require.Equal(t, "spot", observedMarket)
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

type serverlessTimerRuntimeClientFunc func(context.Context, *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error)

func (f serverlessTimerRuntimeClientFunc) ClaimTimerBatch(ctx context.Context, req *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
	return f(ctx, req)
}
