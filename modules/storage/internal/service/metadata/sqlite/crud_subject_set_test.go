package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

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
	if _, err := store.StageDatasetSubjectSet(ctx, "space", "set-1", []*pb.DatasetSubject{{SpaceId: "space", DatasetId: "dataset", SubjectId: "new", Status: "active", Attributes: map[string]string{
		"active_instrument_set_fetched_at": time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}}}); err != nil {
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

	if _, err := store.StageDatasetSubjectSet(ctx, "space", "set-old", []*pb.DatasetSubject{{
		SpaceId: "space", DatasetId: "dataset", SubjectId: "old", Status: "active",
		Attributes: map[string]string{"active_instrument_set_fetched_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateDatasetSubjectSet(ctx, "space", "set-old"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale activation error = %v, want ErrRevisionConflict", err)
	}
}

func TestDatasetSubjectSetRevisionCheckScopesToStagedDatasets(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	createTestDataset(t, ctx, store, "dataset", "node-a")
	createTestDataset(t, ctx, store, "other-dataset", "node-a")
	for _, subjectID := range []string{"old", "other", "new"} {
		if _, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: subjectID, SubjectType: "stock", Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "space", DatasetId: "other-dataset", SubjectId: "other", Status: "active", Attributes: map[string]string{
		"active_instrument_set_fetched_at": time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageDatasetSubjectSet(ctx, "space", "set-scoped", []*pb.DatasetSubject{{SpaceId: "space", DatasetId: "dataset", SubjectId: "new", Status: "active", Attributes: map[string]string{
		"active_instrument_set_fetched_at": time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateDatasetSubjectSet(ctx, "space", "set-scoped"); err != nil {
		t.Fatalf("activation should ignore a newer unrelated dataset: %v", err)
	}
	active, _, err := store.ListDatasetSubjects(ctx, "space", "other-dataset", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].GetSubjectId() != "other" {
		t.Fatalf("unrelated active dataset changed: %v", active)
	}
}

func TestDatasetSubjectSetShardsActivateOnlyAfterCompleteUnion(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	createTestDataset(t, ctx, store, "dataset", "node-a")
	createTestDataset(t, ctx, store, "target", "node-a")
	for _, subjectID := range []string{"old", "a", "b"} {
		if _, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: subjectID, SubjectType: "stock", Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "space", DatasetId: "dataset", SubjectId: "old", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	stage := func(setID, subjectID string) {
		t.Helper()
		if _, err := store.StageDatasetSubjectSet(ctx, "space", setID, []*pb.DatasetSubject{
			{SpaceId: "space", DatasetId: "dataset", SubjectId: subjectID, SubjectRole: "record", Status: "active", Attributes: map[string]string{"active_instrument_set_fetched_at": fetchedAt}},
			{SpaceId: "space", DatasetId: "target", SubjectId: subjectID, SubjectRole: "normal", Status: "active", Attributes: map[string]string{"active_instrument_set_fetched_at": fetchedAt}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	stage("snapshot::shard:0/2", "a")
	if count, err := store.ActivateDatasetSubjectSet(ctx, "space", "snapshot"); err != nil {
		t.Fatal(err)
	} else if count != 0 {
		t.Fatalf("incomplete shard activation count = %d, want 0", count)
	}
	active, _, err := store.ListDatasetSubjects(ctx, "space", "dataset", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].GetSubjectId() != "old" {
		t.Fatalf("old active set changed before all shards arrived: %v", active)
	}

	stage("snapshot::shard:1/2", "b")
	count, err := store.ActivateDatasetSubjectSet(ctx, "space", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("complete shard activation count = %d, want 4", count)
	}
	active, _, err = store.ListDatasetSubjects(ctx, "space", "dataset", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[0].GetSubjectId() != "a" || active[1].GetSubjectId() != "b" {
		t.Fatalf("active union = %v", active)
	}
	target, _, err := store.ListDatasetSubjects(ctx, "space", "target", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(target) != 2 || target[0].GetSubjectId() != "a" || target[1].GetSubjectId() != "b" {
		t.Fatalf("target active union = %v", target)
	}
}

func TestDatasetSubjectSetShardsRejectMixedInstrumentSnapshotFingerprints(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	createTestDataset(t, ctx, store, "dataset", "node-a")
	for _, subjectID := range []string{"old", "a", "b"} {
		if _, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: subjectID, SubjectType: "stock", Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "space", DatasetId: "dataset", SubjectId: "old", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	attrs := func(fingerprint string) map[string]string {
		return map[string]string{
			"active_instrument_set_fetched_at": time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			"instrument_snapshot_fingerprint":  fingerprint,
		}
	}
	for index, fingerprint := range []string{"fingerprint-a", "fingerprint-b"} {
		setID := "snapshot::shard:" + string(rune('0'+index)) + "/2"
		if _, err := store.StageDatasetSubjectSet(ctx, "space", setID, []*pb.DatasetSubject{{SpaceId: "space", DatasetId: "dataset", SubjectId: string(rune('a' + index)), Status: "active", Attributes: attrs(fingerprint)}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ActivateDatasetSubjectSet(ctx, "space", "snapshot"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("mixed instrument snapshot fingerprints error = %v, want ErrRevisionConflict", err)
	}
	active, _, err := store.ListDatasetSubjects(ctx, "space", "dataset", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].GetSubjectId() != "old" {
		t.Fatalf("mixed snapshot activation changed old active set: %v", active)
	}
}

func TestDatasetSubjectSetRejectsSameTimestampDifferentInstrumentFingerprint(t *testing.T) {
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
	fetchedAt := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := store.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "space", DatasetId: "dataset", SubjectId: "old", Status: "active", Attributes: map[string]string{
		"active_instrument_set_fetched_at": fetchedAt,
		"instrument_snapshot_fingerprint":  "fingerprint-a",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageDatasetSubjectSet(ctx, "space", "same-time", []*pb.DatasetSubject{{SpaceId: "space", DatasetId: "dataset", SubjectId: "new", Status: "active", Attributes: map[string]string{
		"active_instrument_set_fetched_at": fetchedAt,
		"instrument_snapshot_fingerprint":  "fingerprint-b",
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateDatasetSubjectSet(ctx, "space", "same-time"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("same timestamp fingerprint error = %v, want ErrRevisionConflict", err)
	}
	active, _, err := store.ListDatasetSubjects(ctx, "space", "dataset", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].GetSubjectId() != "old" {
		t.Fatalf("same timestamp activation changed old active set: %v", active)
	}
}
