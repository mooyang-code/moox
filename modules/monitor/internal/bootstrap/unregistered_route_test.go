package bootstrap

import (
	"context"
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type rejectingAuthorizer struct{}

func (rejectingAuthorizer) IsRegistered(context.Context, string, string) (bool, error) {
	return false, nil
}

// 没有登记部署的进程上报运行指标时被拒收，并记入「未登记」，供健康概览列出。
func TestMetricsRouteRecordsUnregisteredProducer(t *testing.T) {
	runtime := &Runtime{Unregistered: monmetrics.NewUnregisteredProducers()}
	route := metricsObservabilityRoute(&monmetrics.StorageAdapter{}, &monmetrics.MetricMessageStore{}, rejectingAuthorizer{}, nil, runtime, true)
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	err := route(context.Background(),
		&eventpb.EventMessage{SpaceId: monmetrics.InternalMetricSpaceID, EventId: "e1", OccurredAt: timestamppb.New(at)},
		&metricspb.MetricReport{ServiceName: "collector", NodeId: "compute-1", InstanceId: "collector@compute-1", ServiceVersion: "v1"},
	)
	require.ErrorContains(t, err, "unregistered metric producer")
	got := runtime.Unregistered.List(at)
	require.Len(t, got, 1)
	require.Equal(t, monmetrics.UnregisteredProducer{
		ServiceName: "collector", NodeID: "compute-1", InstanceID: "collector@compute-1", Version: "v1", FirstSeenAt: at, LastSeenAt: at,
	}, got[0])
}
