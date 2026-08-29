package sqlite

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestDatasetSubjectSetStagingKeepsOldActiveSetUntilAtomicActivation(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	createTestDataset(t, ctx, store, "dataset", "node-a")
	for _, subjectID := range []string{"old", "new"} {
		if _, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: subjectID, SubjectType: "stock", Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "space", DatasetId: "dataset", SubjectId: "old", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageDatasetSubjectSet(ctx, "space", "set-1", []*pb.DatasetSubject{{SpaceId: "space", DatasetId: "dataset", SubjectId: "new", Status: "active"}}); err != nil {
		t.Fatal(err)
	}
	active, _, err := store.ListDatasetSubjects(ctx, "space", "dataset", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].GetSubjectId() != "old" {
		t.Fatalf("active set changed during staging: %v", active)
	}
	if _, err := store.ActivateDatasetSubjectSet(context.Background(), "space", "set-1"); err != nil {
		t.Fatal(err)
	}
	active, _, err = store.ListDatasetSubjects(ctx, "space", "dataset", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].GetSubjectId() != "new" || active[0].GetStatus() != "active" {
		t.Fatalf("active set after activation = %v", active)
	}
	if count, err := store.ActivateDatasetSubjectSet(ctx, "space", "set-1"); err != nil {
		t.Fatalf("repeat activation: %v", err)
	} else if count != 1 {
		t.Fatalf("repeat activation count = %d, want 1", count)
	}
}
