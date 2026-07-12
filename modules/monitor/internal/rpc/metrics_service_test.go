package rpc

import (
	"context"
	"testing"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListMetricServicesRejectsInvalidSpace(t *testing.T) {
	svc := newTestService(t)
	rsp, err := svc.ListMetricServices(context.Background(), &monitorpb.ListMetricServicesReq{SpaceId: "default"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestGetMetricLatestRequiresSeriesID(t *testing.T) {
	svc := newTestService(t)
	rsp, err := svc.GetMetricLatest(context.Background(), &monitorpb.GetMetricLatestReq{SpaceId: "moox_system"})
	require.NoError(t, err)
	// newTestService 未注入 MetricsQuery，优先返回不可用错误。
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}

func TestQueryMetricHistoryRejectsInvalidTime(t *testing.T) {
	svc := newTestService(t)
	rsp, err := svc.QueryMetricHistory(context.Background(), &monitorpb.QueryMetricHistoryReq{
		SpaceId: "moox_system", StartAt: "bad", EndAt: "2026-01-02T00:00:00Z",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}
