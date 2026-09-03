package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestHandlerRoutesTimerToSourceBoundMarketFetch(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "tencent")
	t.Setenv("MOOX_MARKET_FETCH_SOURCE_ID", "stockcn_http")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "dataset_stockcn_equity_kline")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "600000.XSHG")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://storage:11003")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "0")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "1")

	var observed marketfetch.Request
	handler := &Handler{
		NewMarketFetch: func() *marketfetch.Handler {
			return &marketfetch.Handler{
				NewStorage: func(string, string, string) (marketfetch.Storage, error) {
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
}

func TestHandlerRoutesInstrumentTimerModeWithoutKlineSubjects(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_MARKET_FETCH_MODE", "instrument_snapshot")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "stockcn_multi")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_ID", "stockcn")
	t.Setenv("MOOX_MARKET_FETCH_INSTRUMENT_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "dataset_stockcn_instruments")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1d")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://storage:11003")

	var observed marketfetch.Request
	published := false
	handler := &Handler{
		NewMarketFetch: func() *marketfetch.Handler {
			return &marketfetch.Handler{
				NewStorage: func(string, string, string) (marketfetch.Storage, error) {
					return timerStorage{}, nil
				},
				Publish: func(context.Context, marketfetch.Request, proto.Message) error {
					published = true
					return errors.New("instrument canary must not publish completion")
				},
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
		RequestID: "instrument-request",
	})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success)
	require.False(t, published)
	require.Equal(t, domain.BatchKindInstrumentSnapshot, observed.BatchKind)
	require.Len(t, observed.Items, 1)
	require.Equal(t, "instrument", observed.Items[0].DataType)
	require.Equal(t, "dataset_stockcn_instruments", observed.Items[0].SubjectID)
}

func TestHandlerAcceptsInstrumentCanaryActionPayloadInInstrumentMode(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_MARKET_FETCH_MODE", "instrument_snapshot")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://storage:11003")

	var observed marketfetch.Request
	published := false
	handler := &Handler{
		NewMarketFetch: func() *marketfetch.Handler {
			return &marketfetch.Handler{
				NewStorage: func(string, string, string) (marketfetch.Storage, error) {
					return timerStorage{}, nil
				},
				Publish: func(context.Context, marketfetch.Request, proto.Message) error {
					published = true
					return errors.New("instrument canary must not publish completion")
				},
				Execute: func(_ context.Context, request marketfetch.Request, _ marketfetch.Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
					observed = request
					return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
				},
			}
		},
	}
	raw, err := json.Marshal(model.CloudFunctionEvent{
		Action: model.EventActionInstrumentSnapshot, RequestID: "instrument-canary",
		StorageRPCGatewayTarget: "ip://storage:11003",
		Data: map[string]interface{}{
			"batch_id": "instrument-canary", "batch_kind": "instrument_snapshot",
			"space_id": "stockcn", "dataset_id": "dataset_stockcn_instruments", "frequency": "1d",
			"provider": "stockcn_multi", "market_type": "equity",
			"items": []map[string]interface{}{{
				"subject_id": "dataset_stockcn_instruments", "provider": "stockcn_multi",
				"market_type": "equity", "data_type": "instrument",
				"dataset_id": "dataset_stockcn_instruments", "snapshot_at": "2026-09-01T08:00:00Z",
			}},
		},
	})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success)
	require.False(t, published)
	require.Equal(t, domain.BatchKindInstrumentSnapshot, observed.BatchKind)
	require.Equal(t, "dataset_stockcn_instruments", observed.Items[0].DatasetID)
}

func TestHandlerRejectsActionOnlyTimerPayload(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_MARKET_FETCH_MODE", "instrument_snapshot")

	handler := &Handler{NewMarketFetch: func() *marketfetch.Handler { return &marketfetch.Handler{} }}
	raw, err := json.Marshal(model.CloudFunctionEvent{Action: model.EventActionInstrumentSnapshot, RequestID: "action-only"})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.False(t, response.(*model.Response).Success)
	require.Contains(t, response.(*model.Response).Message, "invalid_timer_event")
}

func TestHandlerRejectsTimerActionMismatchedWithConfiguredMode(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_MARKET_FETCH_MODE", "instrument_snapshot")

	handler := &Handler{NewMarketFetch: func() *marketfetch.Handler { return &marketfetch.Handler{} }}
	raw, err := json.Marshal(model.CloudFunctionEvent{
		Type: "Timer", TriggerName: "moox-market-fetch-timer",
		Time: "2026-09-01T08:00:00Z", Message: "market_fetch_timer_v1",
		Action: model.EventActionMarketFetch, RequestID: "mismatched-timer",
	})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.False(t, response.(*model.Response).Success)
	require.Contains(t, response.(*model.Response).Message, "invalid_timer_mode")
}

type timerStorage struct{}

func (timerStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error { return nil }
func (timerStorage) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	return nil
}
