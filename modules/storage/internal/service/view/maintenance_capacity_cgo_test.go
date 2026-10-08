//go:build cgo

package view

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/client"
)

// TestSeriesCapacityMaintainerRebuildsWhenOneSeriesExceedsLimit exercises the
// actual maintainer with DuckDB. The active index contains 6,001 bars for A
// and one for B; a 6,000-row limit must trigger an A/B rebuild, and the
// replacement must retain at most the configured 5,000-bar lookback per series.
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
				SpaceId: viewSchema.SpaceID, ViewId: viewSchema.ViewID, DatasetId: viewSchema.PrimaryDatasetID,
				ViewVersion: viewSchema.ViewVersion, Engine: viewSchema.Engine,
				ViewSchemaHash: schemaHash, Columns: columns,
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s: rsp=%v err=%v", indexID, rsp, err)
		}
	}
	prepare("prices-a")
	if err := svc.AttachActiveView(&pb.View{
		SpaceId: "space", ViewId: "prices", DatasetId: "prices",
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
	base, err := time.Parse(time.RFC3339, "2026-08-19T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < 5996; offset += 1000 {
		end := min(offset+1000, 5996)
		rows := make([]*pb.ViewIndexRowWrite, 0, end-offset)
		for index := offset; index < end; index++ {
			rows = append(rows, row("A", base.Add(time.Duration(index)*time.Minute).Format(time.RFC3339)))
		}
		apply("prices-a", rows...)
	}
	if result, err := capacityReader.SeriesCapacity(ctx, "prices-a", 6000); err != nil || result.Exceeded || result.Rows != 0 {
		t.Fatalf("exact threshold capacity=%#v err=%v, want no offender at 6000 rows", result, err)
	}
	apply("prices-a", row("A", base.Add(5996*time.Minute).Format(time.RFC3339)))
	if result, err := capacityReader.SeriesCapacity(ctx, "prices-a", 6000); err != nil || !result.Exceeded || result.Rows != 6001 {
		t.Fatalf("over threshold capacity=%#v err=%v, want 6001 rows with trigger", result, err)
	}
	if result, err := capacityReader.SeriesCapacity(ctx, "prices-a", 3); err != nil || !result.Exceeded || result.SubjectID != "A" || result.Rows != 6001 {
		t.Fatalf("active capacity=%#v err=%v, want A=6001 over limit", result, err)
	}

	metadata := &capacityMaintenanceMetadata{maintenanceMetadata: maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", DatasetId: "prices",
		Engine: "duckdb", ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
		ActiveViewSchemaHash: schemaHash, ActiveColumns: columns, Columns: columns, Freq: "1m", Status: "active",
	}}}
	primaryRows := make([]*pb.TimeSeriesRow, 0, 6002)
	for index := 0; index < 4; index++ {
		primaryRows = append(primaryRows, &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{
			SpaceId: "space", DatasetId: "prices", SubjectId: "A", Freq: "1m",
			DataTime: base.Add(-24*time.Hour + time.Duration(index)*time.Minute).Format(time.RFC3339), SeriesTag: "venue:test",
		}})
	}
	for index := 5996; index >= 0; index-- {
		primaryRows = append(primaryRows, &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{
			SpaceId: "space", DatasetId: "prices", SubjectId: "A", Freq: "1m",
			DataTime: base.Add(time.Duration(index) * time.Minute).Format(time.RFC3339), SeriesTag: "venue:test",
		}})
	}
	primaryRows = append(primaryRows, &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{
		SpaceId: "space", DatasetId: "prices", SubjectId: "B", Freq: "1m", DataTime: base.Format(time.RFC3339), SeriesTag: "venue:test",
	}})
	primary := &capacitySubjectRangeReader{rows: primaryRows}
	svc.SetPrimaryAuth(auth)
	svc.consumerState = func(context.Context) (jetstream.ConsumerState, error) { return jetstream.ConsumerState{}, nil }
	checkNow := base.Add(24 * time.Hour)
	maintainOpts := MaintenanceOptions{
		Metadata: metadata, Primary: &primaryHistoryFieldReader{}, PrimaryRange: primary, OwnerID: "owner", Grace: 0,
		BackfillPageSize: 10000,
		TrimBars:         6000, Bars: 5000,
		CapacityCheckInterval: time.Hour, CapacityCheckJitter: time.Hour,
		capacityCheckJitterSource:   func(time.Duration) time.Duration { return 30 * time.Minute },
		capacityCheckNow:            func() time.Time { return checkNow },
		RebuildMaxPendingConfigured: true, RebuildMaxPending: 0, RebuildIdleChecksConfigured: true, RebuildIdleChecks: 1,
	}
	if err := svc.maintainView(ctx, maintainOpts, auth, metadata.view); err != nil {
		t.Fatalf("initial jittered maintenance: %v", err)
	}
	if metadata.created != nil || metadata.claimedIndex != "" {
		t.Fatalf("capacity scan ran before its initial jitter phase: log=%v index=%q", metadata.created, metadata.claimedIndex)
	}
	checkNow = checkNow.Add(30 * time.Minute)
	if err := svc.maintainView(ctx, maintainOpts, auth, metadata.view); err != nil {
		t.Fatalf("capacity maintenance: %v", err)
	}
	if metadata.created == nil || metadata.created.GetTriggerReason() != pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SERIES_CAPACITY {
		t.Fatalf("audit log=%v, want SERIES_CAPACITY", metadata.created)
	}
	for _, want := range []string{`"subject_id":"A"`, `"frequency":"1m"`, `"series_tag":"venue:test"`, `"observed_bars":6001`, `"trim_bars":6000`, `"bars":5000`} {
		if !strings.Contains(metadata.created.GetDetailsJson(), want) {
			t.Fatalf("capacity audit details missing %s: %s", want, metadata.created.GetDetailsJson())
		}
	}
	if metadata.claimedIndex == "" || metadata.claimedIndex == "prices-a" || !metadata.activated {
		t.Fatalf("replacement was not activated: activated=%v claimed=%q", metadata.activated, metadata.claimedIndex)
	}
	if result, err := capacityReader.SeriesCapacity(ctx, metadata.claimedIndex, 6000); err != nil || result.Exceeded {
		t.Fatalf("replacement still exceeds capacity: %#v err=%v", result, err)
	}
	for _, subject := range []string{"A", "B"} {
		rows, _, err := engine.Query(ctx, metadata.claimedIndex, viewindex.QuerySpec{Selectors: []viewindex.TimeSeriesSelector{{SpaceID: "space", DatasetID: "prices", SubjectID: subject, Freq: "1m"}}, Limit: 6000})
		if err != nil {
			t.Fatalf("query replacement subject=%s: %v", subject, err)
		}
		wantCount := 5000
		if subject == "B" {
			wantCount = 1
		}
		if len(rows) != wantCount {
			t.Fatalf("replacement retained %d rows for subject %s, want %d lookback bars", len(rows), subject, wantCount)
		}
		if subject == "A" {
			oldest, latest := rows[0].GetKey().GetTimeSeries().GetDataTime(), rows[0].GetKey().GetTimeSeries().GetDataTime()
			for _, result := range rows[1:] {
				dataTime := result.GetKey().GetTimeSeries().GetDataTime()
				if dataTime < oldest {
					oldest = dataTime
				}
				if dataTime > latest {
					latest = dataTime
				}
			}
			wantOldest := base.Add(997 * time.Minute).Format(time.RFC3339)
			wantLatest := base.Add(5996 * time.Minute).Format(time.RFC3339)
			if oldest != wantOldest || latest != wantLatest {
				t.Fatalf("replacement subject A range=%s..%s, want latest 5000 bars %s..%s", oldest, latest, wantOldest, wantLatest)
			}
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

type capacitySubjectRangeReader struct {
	rows []*pb.TimeSeriesRow
}

func (r *capacitySubjectRangeReader) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	if len(req.GetSelectors()) != 1 {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: successRetInfo()}, nil
	}
	selector := req.GetSelectors()[0]
	rows := make([]*pb.TimeSeriesRow, 0)
	for _, row := range r.rows {
		key := row.GetKey()
		if key.GetSpaceId() == selector.GetSpaceId() && key.GetDatasetId() == selector.GetDatasetId() && key.GetSubjectId() == selector.GetSubjectId() && key.GetFreq() == selector.GetFreq() {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].GetKey().GetDataTime() > rows[j].GetKey().GetDataTime()
	})
	if perSeries := int(req.GetLatestPerSeries()); perSeries > 0 {
		kept := make(map[string]int)
		latest := rows[:0]
		for _, row := range rows {
			if kept[row.GetKey().GetSeriesTag()] < perSeries {
				kept[row.GetKey().GetSeriesTag()]++
				latest = append(latest, row)
			}
		}
		rows = latest
	}
	limit := int(req.GetPage().GetSize())
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return &pb.ReadTimeSeriesRowsRsp{
		RetInfo: successRetInfo(), Rows: rows,
		PageResult: &pb.PageResult{Page: 1, Size: uint32(limit), Total: uint32(len(rows)), HasMore: false},
	}, nil
}
