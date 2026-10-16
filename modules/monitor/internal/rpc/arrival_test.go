package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestQueryPipelineArrivalHistoryReportsInvalidAndUnavailable(t *testing.T) {
	req := &monitorpb.QueryPipelineArrivalHistoryReq{SpaceId: "crypto", DatasetId: "dataset_spot_1m", Freq: "1m"}
	rsp, err := New(nil, Options{}).QueryPipelineArrivalHistory(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())

	// 没有采集任务清单就没有交易日历，按参数无效返回，不当作服务故障。
	service := New(nil, Options{HealthView: &healthview.Builder{Facts: &observability.Builder{}}})
	rsp, err = service.QueryPipelineArrivalHistory(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	rsp, err = service.QueryPipelineArrivalHistory(t.Context(), &monitorpb.QueryPipelineArrivalHistoryReq{SpaceId: "crypto", Freq: "1m"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
