package sqlite

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestDataNodeRegistrationPreservesAdminFields(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	first, err := store.RegisterDataNode(ctx, " node-a ", " trpc://node-a/v1 ", "Initial")
	if err != nil {
		t.Fatal(err)
	}
	if first.GetNodeId() != "node-a" || first.GetServiceTarget() != "trpc://node-a/v1" || first.GetName() != "Initial" || first.GetStatus() != "active" {
		t.Fatalf("first registration = %v", first)
	}
	admin, err := store.UpdateDataNode(ctx, "node-a", "Admin name", "disabled")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RegisterDataNode(ctx, "node-a", "trpc://node-a/v2", "Deployment name")
	if err != nil {
		t.Fatal(err)
	}
	if second.GetServiceTarget() != "trpc://node-a/v2" || second.GetName() != admin.GetName() || second.GetStatus() != admin.GetStatus() {
		t.Fatalf("idempotent registration overwrote admin fields: got=%v admin=%v", second, admin)
	}
	if _, err := store.UpdateDataNode(ctx, "node-a", "Admin name", "invalid"); err == nil {
		t.Fatal("accepted invalid DataNode status")
	}
}

func TestDataNodeListIsOrderedAndPaginated(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	for _, nodeID := range []string{"node-c", "node-a", "node-b"} {
		if _, err := store.RegisterDataNode(ctx, nodeID, "trpc://"+nodeID, nodeID); err != nil {
			t.Fatal(err)
		}
	}
	items, page, err := store.ListDataNodes(ctx, &pb.Page{Page: 1, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{items[0].GetNodeId(), items[1].GetNodeId()}; got[0] != "node-a" || got[1] != "node-b" || page.GetTotal() != 3 || !page.GetHasMore() {
		t.Fatalf("first page items=%v page=%v", got, page)
	}
	items, page, err = store.ListDataNodes(ctx, &pb.Page{Page: 2, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].GetNodeId() != "node-c" || page.GetHasMore() {
		t.Fatalf("second page items=%v page=%v", items, page)
	}
}

func TestDataNodeDeleteRequiresDisabledAndNoDatasets(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.RegisterDataNode(ctx, "node-a", "trpc://node-a", "Node A"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDataNode(ctx, "node-a"); !errors.Is(err, ErrDataNodeMustBeDisabled) {
		t.Fatalf("delete active node error=%v, want=%v", err, ErrDataNodeMustBeDisabled)
	}
	if _, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", DataSourceId: "source", DataNodeId: "node-a",
		Name: "Dataset", DataKind: pb.DataKind_DATA_KIND_RECORD,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDataNode(ctx, "node-a", "Node A", "disabled"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDataNode(ctx, "node-a"); !errors.Is(err, ErrDataNodeReferenced) {
		t.Fatalf("delete referenced node error=%v, want=%v", err, ErrDataNodeReferenced)
	}
	if _, err := store.GetDataNode(ctx, "node-a"); err != nil {
		t.Fatalf("referenced node was deleted: %v", err)
	}

	if _, err := store.RegisterDataNode(ctx, "node-b", "trpc://node-b", "Node B"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDataNode(ctx, "node-b", "Node B", "disabled"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDataNode(ctx, "node-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetDataNode(ctx, "node-b"); err == nil {
		t.Fatal("deleted DataNode still exists")
	}
}
