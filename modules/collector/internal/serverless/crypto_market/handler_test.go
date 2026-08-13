package cryptomarket

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/clsreporter"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tencentyun/scf-go-lib/functioncontext"
)

type recordingReporter struct {
	entries  []clsreporter.Entry
	flushed  bool
	flushCtx context.Context
}

func (r *recordingReporter) Report(entry clsreporter.Entry) { r.entries = append(r.entries, entry) }
func (r *recordingReporter) Flush(ctx context.Context) error {
	r.flushed = true
	r.flushCtx = ctx
	return nil
}

func TestHandlerRejectsUnsupportedActionAndFlushesReporter(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", spaceID)
	reporter := &recordingReporter{}
	handler := &Handler{NewReporter: func() (clsreporter.Reporter, time.Duration, error) {
		return reporter, 800 * time.Millisecond, nil
	}}
	raw, err := json.Marshal(model.CloudFunctionEvent{Action: "unsupported", RequestID: "request-1"})
	require.NoError(t, err)
	ctx := functioncontext.NewContext(context.Background(), &functioncontext.FunctionContext{FunctionName: "crypto-fetcher", TencentcloudRegion: "ap-singapore"})
	response, err := handler.HandleRequest(ctx, raw)
	require.NoError(t, err)
	assert.False(t, response.(*model.Response).Success)
	assert.True(t, reporter.flushed)
	_, hasDeadline := reporter.flushCtx.Deadline()
	assert.True(t, hasDeadline)
}

func TestHandlerSkipsReporterFlushAfterInvocationCancellation(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", spaceID)
	reporter := &recordingReporter{}
	handler := &Handler{NewReporter: func() (clsreporter.Reporter, time.Duration, error) {
		return reporter, 3 * time.Second, nil
	}}
	raw, err := json.Marshal(model.CloudFunctionEvent{Action: "unsupported", RequestID: "request-1"})
	require.NoError(t, err)
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx := functioncontext.NewContext(parent, &functioncontext.FunctionContext{FunctionName: "crypto-fetcher", TencentcloudRegion: "ap-singapore"})
	response, err := handler.HandleRequest(ctx, raw)
	require.NoError(t, err)
	assert.False(t, response.(*model.Response).Success)
	assert.False(t, reporter.flushed)
}

func TestHandlerKeepsFinalResponseReserveBeforeReporterFlush(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", spaceID)
	reporter := &recordingReporter{}
	handler := &Handler{NewReporter: func() (clsreporter.Reporter, time.Duration, error) {
		return reporter, 3 * time.Second, nil
	}}
	raw, err := json.Marshal(model.CloudFunctionEvent{Action: "unsupported", RequestID: "request-1"})
	require.NoError(t, err)
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ctx := functioncontext.NewContext(parent, &functioncontext.FunctionContext{FunctionName: "crypto-fetcher", TencentcloudRegion: "ap-singapore"})
	response, err := handler.HandleRequest(ctx, raw)
	require.NoError(t, err)
	assert.False(t, response.(*model.Response).Success)
	assert.False(t, reporter.flushed)
}

func TestStaticFieldsReporterPreservesInvocationIdentity(t *testing.T) {
	recorder := &recordingReporter{}
	reporter := staticFieldsReporter{Reporter: recorder, Fields: map[string]string{"function_name": "crypto-fetcher", "region": "ap-singapore"}}
	reporter.Report(clsreporter.Entry{Fields: map[string]string{"event_type": "market_fetch_item", "region": "request-region"}})
	require.Len(t, recorder.entries, 1)
	assert.Equal(t, "crypto-fetcher", recorder.entries[0].Fields["function_name"])
	assert.Equal(t, "ap-singapore", recorder.entries[0].Fields["region"])
}

func TestHandlerRequiresTimerIdentityForEnvironmentBackedMarketFetch(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", spaceID)
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "BTC-USDT")
	reporter := &recordingReporter{}
	handler := &Handler{NewReporter: func() (clsreporter.Reporter, time.Duration, error) {
		return reporter, 0, nil
	}}
	raw, err := json.Marshal(model.CloudFunctionEvent{Type: "Timer", TriggerName: "wrong", Message: "market_fetch_timer_v1"})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	assert.False(t, response.(*model.Response).Success)
	assert.Contains(t, response.(*model.Response).Message, "invalid_timer_event")
}

func TestValidateTimerEventAcceptsCloudTriggerContract(t *testing.T) {
	assert.NoError(t, validateTimerEvent(model.CloudFunctionEvent{
		Type: "Timer", TriggerName: timerTriggerName, Message: timerTriggerMessage, Time: "2026-08-04T01:02:00Z",
	}))
}

type timerStorageStub struct{}

func (timerStorageStub) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error { return nil }
func (timerStorageStub) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	return nil
}

func TestTimerMarketFetchHandlerE2E(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", spaceID)
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "binance")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "spot")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "bars")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "BTC-USDT")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"BTC-USDT":"BTCUSDT"}`)
	t.Setenv("MOOX_MARKET_FETCH_ASSIGNMENT_HASH", "assignment")
	t.Setenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON", `{"api.binance.com":["203.0.113.1"],"fapi.binance.com":["203.0.113.2"]}`)
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://127.0.0.1:11003")
	var captured marketfetch.Request
	handler := &Handler{
		NewReporter: func() (clsreporter.Reporter, time.Duration, error) { return clsreporter.Noop(), 0, nil },
		NewMarketFetch: func() *marketfetch.Handler {
			return &marketfetch.Handler{
				NewStorage: func(string, string, string) (marketfetch.Storage, error) { return timerStorageStub{}, nil },
				Execute: func(_ context.Context, request marketfetch.Request, _ marketfetch.Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
					captured = request
					return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
				},
			}
		},
	}
	raw, err := json.Marshal(model.CloudFunctionEvent{Type: "Timer", TriggerName: timerTriggerName, Message: timerTriggerMessage, Time: "2026-08-04T01:02:00Z"})
	require.NoError(t, err)
	response, err := handler.HandleRequest(context.Background(), raw)
	require.NoError(t, err)
	require.True(t, response.(*model.Response).Success)
	require.Equal(t, domain.BatchKindRealtime, captured.BatchKind)
	require.Len(t, captured.Items, 1)
	require.Equal(t, "BTCUSDT", captured.Items[0].Symbol)
	require.Equal(t, []string{"203.0.113.1"}, captured.DNSRoutes["api.binance.com"].IPs)
}
