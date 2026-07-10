package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestListDatasetSubjectsPagesInSQL(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)

	seedDatasetSubjects(t, ctx, store, "s1", "s2", "s3")
	if _, err := store.db.ExecContext(ctx, `UPDATE t_dataset_subjects SET c_attrs_json = '{bad json' WHERE c_subject_id = 's3'`); err != nil {
		t.Fatalf("corrupt third row: %v", err)
	}

	items, page, err := store.ListDatasetSubjects(ctx, "space", "dataset", "", &pb.Page{Page: 1, Size: 2})
	if err != nil {
		t.Fatalf("ListDatasetSubjects page 1: %v", err)
	}
	if got := len(items); got != 2 {
		t.Fatalf("len(items) = %d, want 2", got)
	}
	if page.GetTotal() != 3 || !page.GetHasMore() {
		t.Fatalf("page = %+v, want total=3 has_more=true", page)
	}
}

func TestListSubjectSymbolsPagesInSQL(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)

	seedSubjectSymbols(t, ctx, store, "s1", "s2", "s3")
	if _, err := store.db.ExecContext(ctx, `UPDATE t_subject_symbols SET c_attrs_json = '{bad json' WHERE c_subject_id = 's3'`); err != nil {
		t.Fatalf("corrupt third row: %v", err)
	}

	items, page, err := store.ListSubjectSymbols(ctx, "space", "", "source", "", &pb.Page{Page: 1, Size: 2})
	if err != nil {
		t.Fatalf("ListSubjectSymbols page 1: %v", err)
	}
	if got := len(items); got != 2 {
		t.Fatalf("len(items) = %d, want 2", got)
	}
	if page.GetTotal() != 3 || !page.GetHasMore() {
		t.Fatalf("page = %+v, want total=3 has_more=true", page)
	}
}

func TestUpsertViewColumnBumpsVersionAndPreemptsBuild(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 1
	view.ActiveViewVersion = 1
	view.ActiveIndexId = testIndexA
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	claim := claimBuildReq("owner-1", "build-1", 1)
	claim.ExpectedActiveIndexId = testIndexA
	if _, _, err := store.ClaimViewIndexBuild(ctx, claim); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}

	_, err := store.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "spot_kline_1m_view",
		ColumnName: "volume",
		OriginId:   "binance_spot_kline.volume",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	if err != nil {
		t.Fatalf("UpsertViewColumn: %v", err)
	}

	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetViewVersion() != 2 {
		t.Fatalf("view version = %d, want 2", got.GetViewVersion())
	}
	if got.GetIndexBuild() != nil {
		t.Fatalf("index build = %+v, want preempted", got.GetIndexBuild())
	}
	if got.GetActiveIndexId() != testIndexA {
		t.Fatalf("active index changed to %q", got.GetActiveIndexId())
	}
}

func TestUpsertViewShapeChangeAndBuildPreemptionAreAtomic(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.RetentionWindow = "24h"
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 1)); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_view_build_delete BEFORE DELETE ON t_view_index_builds
		BEGIN SELECT RAISE(ABORT, 'blocked build delete'); END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	changed := sqliteTestView("crypto", "spot_kline_1m_view")
	changed.RetentionWindow = "48h"
	if _, err := store.UpsertView(ctx, changed); err == nil {
		t.Fatal("shape change succeeded despite build-delete failure")
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_view_build_delete`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetRetentionWindow() != "24h" || got.GetViewVersion() != 1 || got.GetIndexBuild().GetBuildId() != "build-1" {
		t.Fatalf("partial shape preemption persisted: %+v", got)
	}
}

func TestUpsertViewColumnAndBuildPreemptionAreAtomic(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 1)); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_view_build_delete BEFORE DELETE ON t_view_index_builds
		BEGIN SELECT RAISE(ABORT, 'blocked build delete'); END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	column := &pb.ViewColumn{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", ColumnName: "volume",
		OriginId: "binance_spot_kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	if _, err := store.UpsertViewColumn(ctx, column); err == nil {
		t.Fatal("column upsert succeeded despite build-delete failure")
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_view_build_delete`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	columns, _, err := store.ListViewColumns(ctx, "crypto", "spot_kline_1m_view", nil)
	if err != nil {
		t.Fatalf("ListViewColumns: %v", err)
	}
	for _, got := range columns {
		if got.GetColumnName() == "volume" {
			t.Fatalf("partial column persisted: %+v", got)
		}
	}
	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetViewVersion() != 1 || got.GetIndexBuild().GetBuildId() != "build-1" {
		t.Fatalf("partial column preemption persisted: %+v", got)
	}
}

func TestClaimViewIndexBuildHasSingleOwner(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}

	build, resumed, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2))
	if err != nil {
		t.Fatalf("first ClaimViewIndexBuild: %v", err)
	}
	if resumed || build.GetState() != pb.ViewIndexBuild_PREPARING {
		t.Fatalf("first claim = resumed %v state %v", resumed, build.GetState())
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-2", "build-2", 2)); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("second claim error = %v, want conflict", err)
	}

	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetIndexBuild().GetBuildId() != "build-1" || got.GetIndexBuild().GetOwnerId() != "owner-1" {
		t.Fatalf("index build = %+v, want first owner", got.GetIndexBuild())
	}
}

func TestClaimViewIndexBuildFencesActivePointer(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ActiveIndexId = testIndexA
	view.ActiveViewVersion = 1
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	wrong := claimBuildReq("owner-1", "build-1", 1)
	wrong.ExpectedActiveIndexId = testIndexB
	if _, _, err := store.ClaimViewIndexBuild(ctx, wrong); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("claim with stale active pointer error = %v, want conflict", err)
	}
	right := claimBuildReq("owner-1", "build-1", 1)
	right.ExpectedActiveIndexId = testIndexA
	if _, _, err := store.ClaimViewIndexBuild(ctx, right); err != nil {
		t.Fatalf("claim with current active pointer: %v", err)
	}
}

func TestExpiredViewIndexBuildLeaseResumesSameBuild(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2)); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	now = now.Add(91 * time.Second)
	req := claimBuildReq("owner-2", "build-1", 2)
	build, resumed, err := store.ClaimViewIndexBuild(ctx, req)
	if err != nil {
		t.Fatalf("resume claim: %v", err)
	}
	if !resumed || build.GetOwnerId() != "owner-2" || build.GetBuildId() != "build-1" {
		t.Fatalf("resumed build = %+v resumed=%v", build, resumed)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-3", "build-2", 2)); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("different build takeover error = %v, want conflict", err)
	}
}

func TestExpiredViewIndexBuildLeaseUsesChronologicalTextOrdering(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2)); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// RFC3339Nano omits zero fractional seconds. Comparing those strings in
	// SQLite puts "...00Z" after "...00.5Z", despite the opposite time order.
	now = now.Add(90*time.Second + 500*time.Millisecond)
	build, resumed, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-2", "build-1", 2))
	if err != nil {
		t.Fatalf("resume claim after fractional boundary: %v", err)
	}
	if !resumed || build.GetOwnerId() != "owner-2" {
		t.Fatalf("resumed build = %+v resumed=%v", build, resumed)
	}
}

func TestUpdateAndActivateViewIndexUseCAS(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	view.ActiveViewVersion = 1
	view.ActiveIndexId = testIndexA
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}

	claim := claimBuildReq("owner-1", "build-1", 2)
	claim.ExpectedActiveIndexId = testIndexA
	if _, _, err := store.ClaimViewIndexBuild(ctx, claim); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	wrongOwner := updateBuildReq("owner-2", pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING)
	if _, err := store.UpdateViewIndexBuild(ctx, wrongOwner); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("wrong-owner update error = %v, want conflict", err)
	}
	building := updateBuildReq("owner-1", pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING)
	building.CursorJson = `{"cursor":"page-1"}`
	building.CoverageStart = "2026-07-09T00:00:00Z"
	building.CoverageEnd = "2026-07-10T00:00:00Z"
	building.EntriesWritten = 25
	if _, err := store.UpdateViewIndexBuild(ctx, building); err != nil {
		t.Fatalf("PREPARING -> BUILDING: %v", err)
	}
	if got, _ := store.GetView(ctx, "crypto", "spot_kline_1m_view"); got.GetActiveIndexId() != testIndexA {
		t.Fatalf("active index before switch = %q, want %q", got.GetActiveIndexId(), testIndexA)
	}
	catchingUp := updateBuildReq("owner-1", pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP)
	if _, err := store.UpdateViewIndexBuild(ctx, catchingUp); err != nil {
		t.Fatalf("BUILDING -> CATCHING_UP: %v", err)
	}
	ready := updateBuildReq("owner-1", pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_READY)
	ready.CoverageStart = building.CoverageStart
	ready.CoverageEnd = building.CoverageEnd
	ready.EntriesWritten = 25
	if _, err := store.UpdateViewIndexBuild(ctx, ready); err != nil {
		t.Fatalf("CATCHING_UP -> READY: %v", err)
	}
	activated, err := store.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: "owner-1",
	})
	if err != nil {
		t.Fatalf("ActivateViewIndex: %v", err)
	}
	if activated.GetActiveIndexId() != testIndexB || activated.GetActiveViewVersion() != 2 {
		t.Fatalf("active after switch = %q/%d", activated.GetActiveIndexId(), activated.GetActiveViewVersion())
	}
	if activated.GetIndexBuild() != nil || activated.GetActiveSchemaHash() != "schema-2" {
		t.Fatalf("activated metadata = %+v", activated)
	}
	if len(activated.GetActiveColumns()) != 1 || activated.GetActiveColumns()[0].GetColumnName() != "close" {
		t.Fatalf("active columns = %+v", activated.GetActiveColumns())
	}
}

func TestActivateViewIndexRequiresLiveLease(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2)); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	for _, transition := range []*pb.UpdateViewIndexBuildReq{
		updateBuildReq("owner-1", pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING),
		updateBuildReq("owner-1", pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP),
		updateBuildReq("owner-1", pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_READY),
	} {
		if _, err := store.UpdateViewIndexBuild(ctx, transition); err != nil {
			t.Fatalf("UpdateViewIndexBuild %s: %v", transition.GetNextState(), err)
		}
	}
	now = now.Add(91 * time.Second)
	if _, err := store.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: "owner-1",
	}); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("expired owner activation error = %v, want conflict", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-2", "build-1", 2)); err != nil {
		t.Fatalf("take over ready build: %v", err)
	}
	if _, err := store.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: "owner-2",
	}); err != nil {
		t.Fatalf("new owner activation: %v", err)
	}
}

func TestFailViewIndexBuildRequiresLiveLease(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2)); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	if _, err := store.UpdateViewIndexBuild(ctx, updateBuildReq("owner-1", pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING)); err != nil {
		t.Fatalf("UpdateViewIndexBuild: %v", err)
	}
	now = now.Add(91 * time.Second)
	if _, err := store.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: "owner-1", Error: "late failure",
	}); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("expired owner failure error = %v, want conflict", err)
	}
}

func TestUpsertViewCannotOverwriteActiveIndexState(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ActiveIndexId = testIndexA
	view.ActiveViewVersion = 1
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("initial UpsertView: %v", err)
	}
	view.ActiveIndexId = testIndexB
	view.ActiveViewVersion = 99
	view.ActiveSchemaHash = "forged"
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("second UpsertView: %v", err)
	}
	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetActiveIndexId() != testIndexA || got.GetActiveViewVersion() != 1 || got.GetActiveSchemaHash() != "" {
		t.Fatalf("active state overwritten by UpsertView: %+v", got)
	}
}

const (
	testIndexA = "view_s63727970746f_v73706f745f6b6c696e655f316d5f76696577_a"
	testIndexB = "view_s63727970746f_v73706f745f6b6c696e655f316d5f76696577_b"
)

func claimBuildReq(ownerID string, buildID string, version uint64) *pb.ClaimViewIndexBuildReq {
	return &pb.ClaimViewIndexBuildReq{
		SpaceId:               "crypto",
		ViewId:                "spot_kline_1m_view",
		BuildId:               buildID,
		IndexId:               testIndexB,
		Engine:                "duckdb",
		TargetViewVersion:     version,
		ExpectedActiveIndexId: "",
		OwnerId:               ownerID,
		LeaseTtlSeconds:       90,
		SchemaHash:            "schema-2",
		Columns: []*pb.ViewColumn{{
			SpaceId: "crypto", ViewId: "spot_kline_1m_view", ColumnName: "close",
		}},
		SnapshotEnd: "2026-07-10T00:00:00Z",
	}
}

func updateBuildReq(ownerID string, expected pb.ViewIndexBuild_State, next pb.ViewIndexBuild_State) *pb.UpdateViewIndexBuildReq {
	return &pb.UpdateViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: ownerID,
		ExpectedState: expected, NextState: next, LeaseTtlSeconds: 90,
	}
}

func openTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	store, err := Open(ctx, Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "..", "schema", "metadata.sql"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return store
}

func sqliteTestView(spaceID string, viewID string) *pb.View {
	return &pb.View{
		SpaceId:          spaceID,
		ViewId:           viewID,
		Name:             viewID,
		PrimaryDatasetId: "dataset",
		DatasetIds:       []string{"dataset"},
		Engine:           "duckdb",
		Status:           "active",
	}
}

func seedSQLiteViewDataset(t *testing.T, ctx context.Context, store *Store, spaceID string) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: spaceID, Name: "Space", Status: "active"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{
		SpaceId:      spaceID,
		DataSourceId: "source",
		Name:         "Source",
		Kind:         "exchange",
		Status:       "active",
	}); err != nil {
		t.Fatalf("UpsertDataSource: %v", err)
	}
	if _, err := store.UpsertDataset(ctx, &pb.Dataset{
		SpaceId:      spaceID,
		DatasetId:    "dataset",
		DataSourceId: "source",
		Name:         "Dataset",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Status:       "active",
	}); err != nil {
		t.Fatalf("UpsertDataset: %v", err)
	}
}

func seedDatasetSubjects(t *testing.T, ctx context.Context, store *Store, subjectIDs ...string) {
	t.Helper()
	seedSpaceSourceDataset(t, ctx, store)
	for _, subjectID := range subjectIDs {
		seedSubject(t, ctx, store, subjectID)
		if _, err := store.BindDatasetSubject(ctx, &pb.DatasetSubject{
			SpaceId:   "space",
			DatasetId: "dataset",
			SubjectId: subjectID,
			Status:    "active",
		}); err != nil {
			t.Fatalf("BindDatasetSubject %s: %v", subjectID, err)
		}
	}
}

func seedSubjectSymbols(t *testing.T, ctx context.Context, store *Store, subjectIDs ...string) {
	t.Helper()
	seedSpaceSourceDataset(t, ctx, store)
	for _, subjectID := range subjectIDs {
		seedSubject(t, ctx, store, subjectID)
		if _, err := store.UpsertSubjectSymbol(ctx, &pb.SubjectSymbol{
			SpaceId:        "space",
			SubjectId:      subjectID,
			DataSourceId:   "source",
			ExternalSymbol: subjectID + "_ext",
			Status:         "active",
		}); err != nil {
			t.Fatalf("BindSubjectSymbol %s: %v", subjectID, err)
		}
	}
}

func seedSpaceSourceDataset(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "space", Name: "Space", Status: "active"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{
		SpaceId:      "space",
		DataSourceId: "source",
		Name:         "Source",
		Kind:         "exchange",
		Status:       "active",
	}); err != nil {
		t.Fatalf("UpsertDataSource: %v", err)
	}
	if _, err := store.UpsertDataset(ctx, &pb.Dataset{
		SpaceId:      "space",
		DatasetId:    "dataset",
		DataSourceId: "source",
		Name:         "Dataset",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Status:       "active",
	}); err != nil {
		t.Fatalf("UpsertDataset: %v", err)
	}
}

func seedSubject(t *testing.T, ctx context.Context, store *Store, subjectID string) {
	t.Helper()
	if _, err := store.UpsertSubject(ctx, &pb.Subject{
		SpaceId:     "space",
		SubjectId:   subjectID,
		SubjectType: "asset",
		Name:        subjectID,
		Status:      "active",
	}); err != nil {
		t.Fatalf("UpsertSubject %s: %v", subjectID, err)
	}
}
