package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestViewKeepDurationChangeDoesNotRequireABRebuild(t *testing.T) {
	existing := &pb.View{PrimaryDatasetId: "prices", DatasetIds: []string{"prices"}, Engine: "duckdb", KeepDuration: "24h"}
	next := &pb.View{PrimaryDatasetId: "prices", DatasetIds: []string{"prices"}, Engine: "duckdb", KeepDuration: "168h"}
	if viewIndexShapeChanged(existing, next) {
		t.Fatal("keep_duration-only change must not trigger an A/B rebuild")
	}
}

func TestRequestViewRebuildAdvancesDesiredRevisionWithoutChangingActiveIndex(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	before, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestViewRebuild(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if requested.GetDesiredViewRevision() != before.GetDesiredViewRevision()+1 {
		t.Fatalf("desired revision = %d, want %d", requested.GetDesiredViewRevision(), before.GetDesiredViewRevision()+1)
	}
	if requested.GetActiveIndexId() != before.GetActiveIndexId() || requested.GetActiveViewRevision() != before.GetActiveViewRevision() {
		t.Fatalf("manual rebuild changed active state: before=%v after=%v", before, requested)
	}
	if got := requested.GetAttributes()["moox.manual_rebuild_revision"]; got != fmt.Sprint(requested.GetDesiredViewRevision()) {
		t.Fatalf("manual rebuild marker = %q, want revision %d", got, requested.GetDesiredViewRevision())
	}
	again, err := store.RequestViewRebuild(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if again.GetDesiredViewRevision() != requested.GetDesiredViewRevision() {
		t.Fatalf("duplicate request advanced desired revision = %d, want idempotent revision %d", again.GetDesiredViewRevision(), requested.GetDesiredViewRevision())
	}
}

func TestRequestViewRebuildUsesAuthoritativeRevisionColumn(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	if _, err := store.db.ExecContext(ctx, `
		UPDATE t_views SET c_desired_view_revision = c_desired_view_revision + 3
		WHERE c_space_id = ? AND c_view_id = ?
	`, "space", "source-view"); err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestViewRebuild(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if requested.GetDesiredViewRevision() != 5 {
		t.Fatalf("desired revision = %d, want authoritative revision + 1 = 5", requested.GetDesiredViewRevision())
	}
}

func TestViewPeriodDatasetStateInsertIdempotencyAndConflict(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	occurredAt := timestamppb.New(time.Date(2026, time.August, 9, 1, 2, 3, 0, time.UTC))
	item := &pb.ViewPeriodDatasetState{
		SpaceId: "space", ViewId: "source-view", DatasetId: "prices", Frequency: "1m", PeriodTime: 1786237200,
		EventId: "period-prices-1", Status: "degraded", SubjectIds: []string{"ETH-USDT", "BTC-USDT", "BTC-USDT"},
		FailedSubjects: []string{"ETH-USDT"}, OccurredAt: occurredAt,
	}

	created, err := store.UpsertViewPeriodDatasetState(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if created.GetUpdatedAt() == nil || created.GetUpdatedAt().CheckValid() != nil {
		t.Fatalf("created updated_at = %v", created.GetUpdatedAt())
	}
	if got := strings.Join(created.GetSubjectIds(), ","); got != "BTC-USDT,ETH-USDT" {
		t.Fatalf("created subject_ids = %q", got)
	}

	idempotent, err := store.UpsertViewPeriodDatasetState(ctx, item)
	if err != nil {
		t.Fatalf("idempotent upsert: %v", err)
	}
	if idempotent.GetEventId() != created.GetEventId() ||
		!idempotent.GetOccurredAt().AsTime().Equal(created.GetOccurredAt().AsTime()) ||
		!idempotent.GetUpdatedAt().AsTime().Equal(created.GetUpdatedAt().AsTime()) {
		t.Fatalf("idempotent state changed: first=%v second=%v", created, idempotent)
	}

	conflict := cloneViewPeriodDatasetState(item)
	conflict.EventId = "period-prices-2"
	if _, err := store.UpsertViewPeriodDatasetState(ctx, conflict); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting upsert error = %v", err)
	}
}

func TestListViewPeriodDatasetStatesReturnsEveryDataset(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	occurredAt := timestamppb.New(time.Date(2026, time.August, 9, 1, 2, 3, 0, time.UTC))
	for _, datasetID := range []string{"prices", "fundamentals"} {
		if _, err := store.UpsertViewPeriodDatasetState(ctx, &pb.ViewPeriodDatasetState{
			SpaceId: "space", ViewId: "source-view", DatasetId: datasetID, Frequency: "1m", PeriodTime: 1786237200,
			EventId: "period-" + datasetID, Status: "complete", SubjectIds: []string{"BTC-USDT"}, OccurredAt: occurredAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	items, err := store.ListViewPeriodDatasetStates(ctx, "space", "source-view", "1m", 1786237200)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].GetDatasetId() != "fundamentals" || items[1].GetDatasetId() != "prices" {
		t.Fatalf("listed datasets = %v", items)
	}
}

func TestUpsertViewMergesPartialColumnsAndReplaceViewColumnsReplaces(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.Columns = []*pb.ViewColumn{{SpaceId: "space", ViewId: "source-view", ColumnName: "prices.close"}, {SpaceId: "space", ViewId: "source-view", ColumnName: "prices.old"}}
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatal(err)
	}
	view.Columns = []*pb.ViewColumn{{SpaceId: "space", ViewId: "source-view", ColumnName: "prices.close"}}
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatal(err)
	}
	columns, _, err := store.ListViewColumns(ctx, "space", "source-view", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 {
		t.Fatalf("columns after partial update = %v", columns)
	}
	view.Columns = view.Columns[:1]
	if _, err := store.ReplaceViewColumns(ctx, view); err != nil {
		t.Fatal(err)
	}
	columns, _, err = store.ListViewColumns(ctx, "space", "source-view", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 1 || columns[0].GetColumnName() != "prices.close" {
		t.Fatalf("columns after replacement = %v", columns)
	}
	view.Columns = []*pb.ViewColumn{}
	if _, err := store.ReplaceViewColumns(ctx, view); err != nil {
		t.Fatal(err)
	}
	columns, _, err = store.ListViewColumns(ctx, "space", "source-view", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 0 {
		t.Fatalf("columns after empty replacement = %v", columns)
	}
	stored, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if stored.GetAttributes()["moox.columns_explicit"] != "true" {
		t.Fatalf("empty replacement lost explicit-column marker: attrs=%v", stored.GetAttributes())
	}
	stored.Attributes = nil
	stored.Description = "ordinary metadata update"
	if _, err := store.UpsertView(ctx, stored); err != nil {
		t.Fatal(err)
	}
	stored, err = store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if stored.GetAttributes()["moox.columns_explicit"] != "true" {
		t.Fatalf("ordinary update cleared explicit-column marker: attrs=%v", stored.GetAttributes())
	}
}

func TestUpsertViewRejectsEngineAndPrimaryChangeWithActiveIndex(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.ActiveIndexId = "source-a"
	view.Engine = "duckdb"
	view.PrimaryDatasetId = "prices"
	raw, err := marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE t_views SET c_active_index_id = ?, c_engine = ?, c_primary_dataset_id = ?, c_attrs_json = ? WHERE c_space_id = ? AND c_view_id = ?`, "source-a", "duckdb", "prices", raw, "space", "source-view"); err != nil {
		t.Fatal(err)
	}
	view, err = store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.Engine = "bleve"
	if _, err := store.UpsertView(ctx, view); err == nil || !strings.Contains(err.Error(), "engine change is unsupported") {
		t.Fatalf("engine change error = %v", err)
	}
	view.Engine = "duckdb"
	view.PrimaryDatasetId = "fundamentals"
	if _, err := store.UpsertView(ctx, view); err == nil || !strings.Contains(err.Error(), "primary dataset change is unsupported") {
		t.Fatalf("primary change error = %v", err)
	}
	unchanged, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.GetActiveIndexId() != "source-a" || unchanged.GetEngine() != "duckdb" || unchanged.GetPrimaryDatasetId() != "prices" {
		t.Fatalf("active view mutated after rejected update: %v", unchanged)
	}
}

func TestGetAndListViewsExposePersistedTimestamps(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)

	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if view.GetCreatedAt() == "" || view.GetUpdatedAt() == "" {
		t.Fatalf("GetView timestamps = created %q updated %q", view.GetCreatedAt(), view.GetUpdatedAt())
	}

	views, _, err := store.ListViews(ctx, "space", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) == 0 || views[0].GetCreatedAt() == "" || views[0].GetUpdatedAt() == "" {
		t.Fatalf("ListViews timestamps = %v", views)
	}
}

func TestViewSyncPointInsertIdempotencyConflictAndMissingDatasets(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	appliedAt := timestamppb.New(time.Date(2026, time.August, 9, 1, 2, 3, 0, time.UTC))
	item := &pb.ViewSyncPoint{
		SpaceId: "space", ViewId: "source-view", DatasetId: "prices", RequestId: "import-1",
		SyncPointId: "sync-prices-1", AppliedAt: appliedAt,
	}

	created, err := store.RecordViewSyncPoint(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if created.GetSyncPointId() != item.GetSyncPointId() || !created.GetAppliedAt().AsTime().Equal(appliedAt.AsTime()) {
		t.Fatalf("created sync point = %v", created)
	}

	idempotent, err := store.RecordViewSyncPoint(ctx, &pb.ViewSyncPoint{
		SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(), DatasetId: item.GetDatasetId(), RequestId: item.GetRequestId(),
		SyncPointId: item.GetSyncPointId(), AppliedAt: timestamppb.New(appliedAt.AsTime().Add(time.Minute)),
	})
	if err != nil {
		t.Fatalf("idempotent record: %v", err)
	}
	if !idempotent.GetAppliedAt().AsTime().Equal(created.GetAppliedAt().AsTime()) {
		t.Fatalf("idempotent applied_at changed: first=%v second=%v", created.GetAppliedAt(), idempotent.GetAppliedAt())
	}

	conflict := &pb.ViewSyncPoint{
		SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(), DatasetId: item.GetDatasetId(), RequestId: item.GetRequestId(),
		SyncPointId: "sync-prices-2", AppliedAt: appliedAt,
	}
	if _, err := store.RecordViewSyncPoint(ctx, conflict); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting sync point error = %v", err)
	}

	if _, err := store.RecordViewSyncPoint(ctx, &pb.ViewSyncPoint{
		SpaceId: "space", ViewId: "source-view", DatasetId: "fundamentals", RequestId: "import-1",
		SyncPointId: "sync-fundamentals-1", AppliedAt: appliedAt,
	}); err != nil {
		t.Fatal(err)
	}
	missing, err := store.MissingViewSyncPointDatasets(ctx, "space", "source-view", "import-1", []string{"news", "prices", "fundamentals", "news"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "news" {
		t.Fatalf("missing datasets = %v, want [news]", missing)
	}
}

func TestViewPeriodRetentionDeletesOnlyOlderProjections(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	oldAt := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	newAt := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
	for _, item := range []*pb.ViewPeriodDatasetState{
		{SpaceId: "space", ViewId: "source-view", DatasetId: "prices", Frequency: "1m", PeriodTime: 1785600000, EventId: "old", Status: "complete", OccurredAt: timestamppb.New(oldAt)},
		{SpaceId: "space", ViewId: "source-view", DatasetId: "prices", Frequency: "1m", PeriodTime: 1786291200, EventId: "new", Status: "complete", OccurredAt: timestamppb.New(newAt)},
	} {
		if _, err := store.UpsertViewPeriodDatasetState(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE t_view_period_dataset_states SET c_updated_at = ? WHERE c_event_id = 'old'`, oldAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE t_view_period_dataset_states SET c_updated_at = ? WHERE c_event_id = 'new'`, newAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordViewSyncPoint(ctx, &pb.ViewSyncPoint{SpaceId: "space", ViewId: "source-view", DatasetId: "prices", RequestId: "old", SyncPointId: "old", AppliedAt: timestamppb.New(oldAt)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordViewSyncPoint(ctx, &pb.ViewSyncPoint{SpaceId: "space", ViewId: "source-view", DatasetId: "prices", RequestId: "new", SyncPointId: "new", AppliedAt: timestamppb.New(newAt)}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	if deleted, err := store.DeleteViewPeriodDatasetStatesBefore(ctx, cutoff); err != nil || deleted != 1 {
		t.Fatalf("delete period states deleted=%d err=%v, want 1", deleted, err)
	}
	if deleted, err := store.DeleteViewSyncPointsBefore(ctx, cutoff); err != nil || deleted != 1 {
		t.Fatalf("delete sync points deleted=%d err=%v, want 1", deleted, err)
	}
}

func openViewPeriodTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node")
	createTestDataset(t, ctx, store, "prices", "node")
	createTestDataset(t, ctx, store, "fundamentals", "node")
	if _, err := store.UpsertView(ctx, &pb.View{
		SpaceId: "space", ViewId: "source-view", Name: "Source view", PrimaryDatasetId: "prices",
		DatasetIds: []string{"prices", "fundamentals"}, KeepDuration: "24h",
	}); err != nil {
		t.Fatal(err)
	}
	return store
}
