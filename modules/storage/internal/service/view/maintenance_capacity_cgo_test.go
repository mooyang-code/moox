//go:build cgo

package view

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// TestSeriesCapacityMaintainerRebuildsWhenOneSeriesExceedsLimit exercises the
// actual maintainer with DuckDB. The active index contains four bars for A and
// one for B; a capacity limit of three must trigger an A/B rebuild, and the
// replacement must retain at most the configured one-bar lookback per series.
func TestSeriesCapacityMaintainerRebuildsWhenOneSeriesExceedsLimit(t *testing.T) {
	ctx := context.Background()
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range svc.engines {
		if closer, ok := engine.(interface{ Close() error }); ok {
			defer closer.Close()
		}
	}
	engine := svc.engines["duckdb"]
	if engine == nil {
		t.Fatal("New did not open the DuckDB engine")
	}
	capacityReader, ok := engine.(viewindex.SeriesCapacityReader)
	if !ok {
		t.Fatal("DuckDB engine does not implement SeriesCapacityReader")
	}

	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	viewSchema := viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "prices",
		ViewVersion: 1, Engine: "duckdb",
	}
	schemaHash := viewindex.HashViewIndexSchema(viewSchema)
	columns := []*pb.ViewColumn(nil)
	prepare := func(indexID string) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
			AuthInfo: auth, IndexId: indexID,
			Schema: &pb.ViewIndexSchema{
				SpaceId: viewSchema.SpaceID, ViewId: viewSchema.ViewID, PrimaryDatasetId: viewSchema.PrimaryDatasetID,
				DatasetIds: []string{"prices"}, ViewVersion: viewSchema.ViewVersion, Engine: viewSchema.Engine,
				ViewSchemaHash: schemaHash, Columns: columns,
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s: rsp=%v err=%v", indexID, rsp, err)
		}
	}
	prepare("prices-a")
	if err := svc.AttachActiveView(&pb.View{
		SpaceId: "space", ViewId: "prices", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"},
		Engine: "duckdb", ActiveIndexId: "prices-a", ActiveViewRevision: 1, ActiveViewSchemaHash: schemaHash,
		Status: "active",
	}); err != nil {
		t.Fatal(err)
	}

	row := func(subject, at string) *pb.ViewIndexRowWrite {
		return &pb.ViewIndexRowWrite{Key: &pb.ViewIndexRowKey{RowKey: &pb.RowKey{
			SpaceId: "space", DatasetId: "prices",
			Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: at, SeriesTag: "venue:test"}},
		}}}
	}
	apply := func(indexID string, rows ...*pb.ViewIndexRowWrite) {
		t.Helper()
		rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: indexID, Batch: &pb.ViewIndexWriteBatch{
			ViewRevision: 1, ViewSchemaHash: schemaHash, WriteMode: "LIVE_WRITE", RowWrites: rows,
		}})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("apply %s: rsp=%v err=%v", indexID, rsp, err)
		}
	}
	apply("prices-a",
		row("A", "2026-08-18T00:00:00Z"), row("A", "2026-08-18T00:01:00Z"),
		row("A", "2026-08-18T00:02:00Z"), row("A", "2026-08-18T00:03:00Z"),
		row("B", "2026-08-18T00:00:00Z"))
	if result, err := capacityReader.SeriesCapacity(ctx, "prices-a", 3); err != nil || !result.Exceeded || result.SubjectID != "A" || result.Rows != 4 {
		t.Fatalf("active capacity=%#v err=%v, want A=4 over limit", result, err)
	}

	metadata := &capacityMaintenanceMetadata{maintenanceMetadata: maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"},
		Engine: "duckdb", ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
		ActiveViewSchemaHash: schemaHash, ActiveColumns: columns, Columns: columns, FilterJson: `{"freq":"1m"}`, KeepDuration: "24h", Status: "active",
	}}}
	primary := &primaryHistoryRangeReader{rows: []*pb.TimeSeriesRow{
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "prices", SubjectId: "A", Freq: "1m", DataTime: "2026-08-18T00:03:00Z", SeriesTag: "venue:test"}},
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "prices", SubjectId: "A", Freq: "1m", DataTime: "2026-08-18T00:02:00Z", SeriesTag: "venue:test"}},
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "prices", SubjectId: "A", Freq: "1m", DataTime: "2026-08-18T00:01:00Z", SeriesTag: "venue:test"}},
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "prices", SubjectId: "A", Freq: "1m", DataTime: "2026-08-18T00:00:00Z", SeriesTag: "venue:test"}},
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "prices", SubjectId: "B", Freq: "1m", DataTime: "2026-08-18T00:00:00Z", SeriesTag: "venue:test"}},
	}}
	svc.SetPrimaryAuth(auth)
	svc.consumerState = func(context.Context) (jetstream.ConsumerState, error) { return jetstream.ConsumerState{}, nil }
	if err := svc.maintainView(ctx, MaintenanceOptions{
		Metadata: metadata, Primary: &primaryHistoryFieldReader{}, PrimaryRange: primary, OwnerID: "owner", Grace: 0,
		MaxPeriodsPerSeries: 3, RebuildLookbackPeriods: map[string]uint64{"default": 1},
		RebuildMaxPendingConfigured: true, RebuildMaxPending: 0, RebuildIdleChecksConfigured: true, RebuildIdleChecks: 1,
	}, auth, metadata.view); err != nil {
		t.Fatalf("capacity maintenance: %v", err)
	}
	if metadata.created == nil || metadata.created.GetTriggerReason() != pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SERIES_CAPACITY {
		t.Fatalf("audit log=%v, want SERIES_CAPACITY", metadata.created)
	}
	for _, want := range []string{`"subject_id":"A"`, `"frequency":"1m"`, `"series_tag":"venue:test"`, `"observed_periods":4`, `"max_periods_per_series":3`, `"rebuild_lookback_periods":1`} {
		if !strings.Contains(metadata.created.GetDetailsJson(), want) {
			t.Fatalf("capacity audit details missing %s: %s", want, metadata.created.GetDetailsJson())
		}
	}
	if metadata.claimedIndex == "" || metadata.claimedIndex == "prices-a" || !metadata.activated {
		t.Fatalf("replacement was not activated: activated=%v claimed=%q", metadata.activated, metadata.claimedIndex)
	}
	if result, err := capacityReader.SeriesCapacity(ctx, metadata.claimedIndex, 3); err != nil || result.Exceeded {
		t.Fatalf("replacement still exceeds capacity: %#v err=%v", result, err)
	}
	for _, subject := range []string{"A", "B"} {
		rows, _, err := engine.Query(ctx, metadata.claimedIndex, viewindex.QuerySpec{Selectors: []viewindex.TimeSeriesSelector{{SpaceID: "space", DatasetID: "prices", SubjectID: subject, Freq: "1m"}}, Limit: 100})
		if err != nil {
			t.Fatalf("query replacement subject=%s: %v", subject, err)
		}
		if len(rows) != 1 {
			t.Fatalf("replacement retained %d rows for subject %s, want exactly one lookback bar", len(rows), subject)
		}
		wantTime := "2026-08-18T00:00:00Z"
		if subject == "A" {
			wantTime = "2026-08-18T00:03:00Z"
		}
		if got := rows[0].GetKey().GetTimeSeries().GetDataTime(); got != wantTime {
			t.Fatalf("replacement subject %s retained %s, want latest lookback bar %s", subject, got, wantTime)
		}
	}
	var terminal bool
	for _, log := range metadata.updated {
		if log.GetResult() == pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED {
			terminal = true
		}
	}
	if !terminal {
		t.Fatalf("capacity build did not emit a successful terminal log: %+v", metadata.updated)
	}
}
