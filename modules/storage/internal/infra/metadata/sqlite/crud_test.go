package sqlite

import (
	"context"
	"path/filepath"
	"testing"

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
