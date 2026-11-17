package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// 新接上报的组件（egress-proxy、callback-dispatcher）用 packages/report 生成的真实快照，Monitor 的入库路由照常收下，就绪判断落进库：
// 快照被永久拒收时组件会一直显示停止上报、又没有报错指向原因。
func TestRealReportsFromNewReportersAreAccepted(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	route := metricsObservabilityRoute(messages, nil, &Runtime{}, true)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)

	for _, tc := range []struct{ module, service string }{{"egress", "egress-proxy"}, {"callbackdispatcher", "callback-dispatcher"}} {
		handler, err := report.NewHandlerWithRegistry(report.Config{Module: tc.module, ServiceName: tc.service, InstanceID: tc.service + "@control", NodeID: "control", BootID: "boot-" + tc.service}, prometheus.NewRegistry())
		require.NoError(t, err)
		snapshot, err := handler.BuildSnapshot()
		require.NoError(t, err)
		metricReport := &metricspb.MetricReport{ServiceName: tc.service, InstanceId: tc.service + "@control", NodeId: "control", BootId: "boot-" + tc.service,
			Sequence: 1, Snapshot: snapshot, Ready: proto.Bool(false), NotReadyReason: "启动尚未完成"}
		encoded, err := registry.Encode(events.ObservabilityMetricsSnapshotReported, metricReport, events.PublishOptions{EventID: "boot-" + tc.service + "-1", SpaceID: events.SystemSpaceID, SubjectID: tc.service + "/" + tc.service + "@control", OccurredAt: time.Now().UTC()})
		require.NoError(t, err)
		encoded.Payload, err = proto.Marshal(encoded.Message)
		require.NoError(t, err)
		message, payload, err := events.DecodeRaw(registry, encoded.Payload, encoded.Subject, encoded.Message.GetEventId(), events.ContentType)
		require.NoError(t, err)
		require.NoError(t, route(context.Background(), message, payload.(*metricspb.MetricReport)), tc.service)
	}
	rows, _, err := monmetrics.NewCatalog(messages).ListServicesAt(t.Context(), "", 0, 10, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Equal(t, monmetrics.ReadinessNotReady, row.Readiness, row.ServiceName)
		require.Equal(t, "启动尚未完成", row.NotReadyReason)
		require.Equal(t, "control", row.NodeID)
	}
}
