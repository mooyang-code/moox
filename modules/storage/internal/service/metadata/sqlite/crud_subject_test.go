package sqlite

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestBindDatasetSubjectAcceptsDisabledLifecycleStatus(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	createTestDataset(t, ctx, store, "dataset", "node-a")

	if _, err := store.UpsertSubject(ctx, &pb.Subject{
		SpaceId: "space", SubjectId: "OLD-USDT", SubjectType: "crypto", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	membership := &pb.DatasetSubject{
		SpaceId: "space", DatasetId: "dataset", SubjectId: "OLD-USDT",
		SubjectRole: "normal", Status: "active",
	}
	if _, err := store.BindDatasetSubject(ctx, membership); err != nil {
		t.Fatal(err)
	}
	membership.Status = "disabled"
	if _, err := store.BindDatasetSubject(ctx, membership); err != nil {
		t.Fatalf("disable dataset subject: %v", err)
	}

	items, _, err := store.ListDatasetSubjects(ctx, "space", "dataset", "OLD-USDT", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].GetStatus() != "disabled" {
		t.Fatalf("dataset subject status = %v, want disabled", items)
	}
}

func TestListSubjectsTreatsNilIDsAsNoFilter(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.UpsertSubject(ctx, &pb.Subject{
		SpaceId: "space", SubjectId: "BTC-USDT", SubjectType: "crypto_pair", Market: "spot", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}

	items, page, err := store.ListSubjects(ctx, "space", "", "", nil, "", &pb.Page{Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].GetSubjectId() != "BTC-USDT" || page.GetTotal() != 1 {
		t.Fatalf("ListSubjects(nil) = items=%v page=%v, want BTC-USDT", items, page)
	}
}
