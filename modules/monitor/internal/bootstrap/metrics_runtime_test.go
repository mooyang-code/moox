package bootstrap

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/client"
)

type ingestHistory struct{ writes int }

func (h *ingestHistory) UpsertFields(context.Context, *storagepb.PrimaryUpsertFieldsReq, ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	h.writes++
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &commonpb.RetInfo{}}, nil
}

func TestReporterExpectationWithNoProbeUsesStableDeploymentIdentity(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	query := monmetrics.NewQueryService(messages, nil)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	for i := range catalog.Components {
		if catalog.Components[i].ID == "factor-mgr" {
			catalog.Components[i].Health = servicecatalog.Health{Kind: "none"}
		}
	}
	now := time.Now().UTC()
	snapshot := domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts:      []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}},
		Placements: []domain.TopologyPlacement{{HostID: "control", ComponentID: "factor-mgr", Status: "enabled"}},
	}
	repos := manager.Repositories()
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}, repos, nil)
	require.NoError(t, run(t.Context()))
	const id = "reporter:control:factor-mgr"
	results, err := repos.Results.Recent(t.Context(), "mooxsys", id, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Success, "registration creates a never-reported expectation without a probe")
	require.NoError(t, messages.TouchService(t.Context(), &eventpb.EventMessage{OccurredAt: timestamppb.New(now)}, &metricspb.MetricReport{ServiceName: "factor-mgr", NodeId: "control", InstanceId: "factor-mgr@control", BootId: "boot-1"}))
	require.NoError(t, run(t.Context()))
	results, err = repos.Results.Recent(t.Context(), "mooxsys", id, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.True(t, results[0].Success, "the first report resolves the same check identity")
	checks, err := repos.Checks.List(t.Context(), store.ListChecksOptions{Source: domain.CheckSourceObservability, Page: store.Page{PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, checks, 1)
	snapshot.Placements[0].Status = "disabled"
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	check, err := repos.Checks.Get(t.Context(), "mooxsys", id)
	require.NoError(t, err)
	require.False(t, check.Enabled)
	results, err = repos.Results.Recent(t.Context(), "mooxsys", id, 10)
	require.NoError(t, err)
	require.Len(t, results, 2, "disable does not fabricate a successful report")
	snapshot.Placements[0].Status = "enabled"
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	check, err = repos.Checks.Get(t.Context(), "mooxsys", id)
	require.NoError(t, err)
	require.True(t, check.Enabled, "reenabled deployments restore reporter checks")
	results, err = repos.Results.Recent(t.Context(), "mooxsys", id, 10)
	require.NoError(t, err)
	require.Len(t, results, 3)
	snapshot.Placements = nil
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	check, err = repos.Checks.Get(t.Context(), "mooxsys", id)
	require.NoError(t, err)
	require.False(t, check.Enabled, "deleting a placement retires its reporter check")

}

func (*ingestHistory) ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	panic("history read is not part of ingestion")
}

func TestMetricsRouteAcceptsUnregisteredReportsWithoutBypassingValidation(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	query := monmetrics.NewQueryService(messages, nil)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = manager.Repositories().Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now}, nil)
	require.NoError(t, err)
	history := &ingestHistory{}
	storage := monmetrics.NewStorageAdapter(history, nil, config.MetricsStorageConfig{SpaceID: "mooxsys", DatasetID: "metrics", Frequency: "1m"})
	route := metricsObservabilityRoute(storage, messages, nil, nil, true)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	raw := []byte("# TYPE moox_collector_kline_resample_claims_total gauge\nmoox_collector_kline_resample_claims_total 1\n")
	sum := sha256.Sum256(raw)
	report := &metricspb.MetricReport{
		ServiceName: "collector", InstanceId: "collector@control", NodeId: "control", BootId: "boot-1",
		Snapshot: &metricspb.MetricSnapshot{SchemaVersion: 1, Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT,
			Compression: metricspb.Compression_COMPRESSION_NONE, Data: raw, UncompressedSha256: sum[:], SampleCount: 1},
	}
	encode := func(id string, observed time.Time, report *metricspb.MetricReport) events.EncodedEvent {
		t.Helper()
		encoded, err := registry.Encode(events.ObservabilityMetricsSnapshotReported, report, events.PublishOptions{EventID: id, SpaceID: "mooxsys", SubjectID: report.ServiceName + "/" + report.InstanceId, OccurredAt: observed})
		require.NoError(t, err)
		encoded.Payload, err = proto.Marshal(encoded.Message)
		require.NoError(t, err)
		return encoded
	}
	ingest := func(encoded events.EncodedEvent) error {
		t.Helper()
		message, payload, err := events.DecodeRaw(registry, encoded.Payload, encoded.Subject, encoded.Message.GetEventId(), events.ContentType)
		require.NoError(t, err)
		return route(t.Context(), message, payload.(*metricspb.MetricReport))
	}
	encoded := encode("unregistered", now, report)
	require.NoError(t, ingest(encoded))
	require.NoError(t, ingest(encoded))
	require.Equal(t, 1, history.writes, "redelivery stays idempotent")
	got, err := (monitorobservability.Builder{Metrics: query, Topology: manager.Repositories().Topology, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.Unregistered, 1)
	require.Equal(t, "collector", got.Unregistered[0].ServiceName)
	_, _, err = events.DecodeRaw(registry, encoded.Payload, encoded.Subject+".wrong", encoded.Message.GetEventId(), events.ContentType)
	require.ErrorContains(t, err, "subject mismatch")
	_, _, err = events.DecodeRaw(registry, encoded.Payload, encoded.Subject, "wrong-id", events.ContentType)
	require.ErrorContains(t, err, "does not match")
	invalid := proto.Clone(report).(*metricspb.MetricReport)
	invalid.Snapshot.Data = []byte("corrupted")
	require.ErrorContains(t, ingest(encode("invalid-snapshot", now, invalid)), "checksum mismatch")
	require.Equal(t, 1, history.writes, "invalid snapshots never reach history")
	duplicate, err := messages.IsDuplicate(t.Context(), "invalid-snapshot")
	require.NoError(t, err)
	require.False(t, duplicate)
	old := proto.Clone(report).(*metricspb.MetricReport)
	old.ServiceName, old.InstanceId = "archive", "archive@control"
	require.NoError(t, ingest(encode("old-report", now.Add(-time.Hour), old)))
	rows, _, err := query.Catalog().ListServicesAt(t.Context(), "", 0, 10, now)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.ServiceName == "archive" {
			require.True(t, row.IsStale, "committing delayed history cannot revive an old reporter")
			require.WithinDuration(t, now.Add(-time.Hour), row.LastSeenAt, time.Millisecond)
		}
	}
}
