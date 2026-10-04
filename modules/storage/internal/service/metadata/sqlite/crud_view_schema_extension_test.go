package sqlite

import (
	"context"
	"errors"
	"testing"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func TestCommitViewSchemaExtensionCASAndRetry(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.Engine = "duckdb"
	view.Attributes = map[string]string{"primary_dataset_role": "factor_result"}
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatal(err)
	}
	closeColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: "source-view", ColumnName: "prices.close", OriginId: "prices.close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	if _, err := store.UpsertViewColumn(ctx, closeColumn); err != nil {
		t.Fatal(err)
	}
	active, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	active.ActiveIndexId = "prices-a"
	active.ActiveViewRevision = active.GetDesiredViewRevision()
	active.ActiveColumns = cloneViewColumns(active.GetColumns())
	active.ActiveViewSchemaHash = coreviewindex.HashViewIndexSchema(coreviewindex.ViewIndexSchema{
		SpaceID: active.GetSpaceId(), ViewID: active.GetViewId(), PrimaryDatasetID: active.GetDatasetId(),
		ViewVersion: active.GetActiveViewRevision(), Engine: active.GetEngine(), Columns: active.GetActiveColumns(),
	})
	active.Attributes["moox.active_dataset_id"] = active.GetDatasetId()
	active.Attributes["moox.active_primary_dataset_id"] = active.GetDatasetId()
	raw, err := marshal(active)
	if err != nil {
		t.Fatal(err)
	}
	activeColumns, err := marshalJSON(active.GetActiveColumns())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE t_views SET c_active_index_id = ?, c_active_view_revision = ?,
		c_active_columns_json = ?, c_active_view_schema_hash = ?, c_attrs_json = ? WHERE c_space_id = ? AND c_view_id = ?`,
		active.GetActiveIndexId(), active.GetActiveViewRevision(), activeColumns, active.GetActiveViewSchemaHash(), raw,
		active.GetSpaceId(), active.GetViewId()); err != nil {
		t.Fatal(err)
	}

	biasColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: "source-view", ColumnName: "prices.bias_20", OriginId: "prices.bias_20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: 1,
	}
	if _, err := store.UpsertViewColumn(ctx, biasColumn); err != nil {
		t.Fatal(err)
	}
	desired, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	wantHash := coreviewindex.HashViewIndexSchema(coreviewindex.ViewIndexSchema{
		SpaceID: desired.GetSpaceId(), ViewID: desired.GetViewId(), PrimaryDatasetID: desired.GetDatasetId(),
		ViewVersion: desired.GetDesiredViewRevision(), Engine: desired.GetEngine(), Columns: desired.GetColumns(),
	})
	req := &pb.CommitViewSchemaExtensionReq{
		SpaceId: desired.GetSpaceId(), ViewId: desired.GetViewId(), ActiveIndexId: desired.GetActiveIndexId(),
		ExpectedActiveRevision: desired.GetActiveViewRevision(), ExpectedDesiredRevision: desired.GetDesiredViewRevision(),
		ExpectedActiveSchemaHash: desired.GetActiveViewSchemaHash(), Columns: desired.GetColumns(), ViewSchemaHash: wantHash,
	}
	committed, err := store.CommitViewSchemaExtension(ctx, req)
	if err != nil {
		t.Fatalf("commit schema extension: %v", err)
	}
	if committed.GetActiveIndexId() != "prices-a" || committed.GetActiveViewRevision() != desired.GetDesiredViewRevision() ||
		committed.GetActiveViewSchemaHash() != wantHash || len(committed.GetActiveColumns()) != 2 || len(committed.GetColumns()) != 2 {
		t.Fatalf("committed View = %v", committed)
	}
	// A lost response after commit is an idempotent retry, not a second schema
	// mutation. The response must include both active and desired columns.
	retried, err := store.CommitViewSchemaExtension(ctx, req)
	if err != nil {
		t.Fatalf("retry committed schema extension: %v", err)
	}
	if retried.GetActiveViewRevision() != committed.GetActiveViewRevision() || len(retried.GetColumns()) != 2 {
		t.Fatalf("retry response = %v", retried)
	}

	stale := proto.Clone(req).(*pb.CommitViewSchemaExtensionReq)
	stale.ViewSchemaHash = "stale"
	if _, err := store.CommitViewSchemaExtension(ctx, stale); !errors.Is(err, metadatastore.ErrViewSchemaExtensionConflict) {
		t.Fatalf("stale CAS error = %v, want conflict", err)
	}
	latest, err := store.GetView(ctx, "space", "source-view")
	if err != nil || latest.GetActiveViewRevision() != committed.GetActiveViewRevision() {
		t.Fatalf("stale CAS changed active contract: latest=%v err=%v", latest, err)
	}
}
