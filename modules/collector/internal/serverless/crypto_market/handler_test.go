package cryptomarket

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/packages/clsreporter"
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
