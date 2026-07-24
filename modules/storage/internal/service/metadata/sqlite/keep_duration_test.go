package sqlite

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestKeepDurationCovers(t *testing.T) {
	tests := []struct {
		name        string
		datasetKeep string
		viewKeep    string
		want        bool
	}{
		{name: "dataset permanent", datasetKeep: "0", viewKeep: "720h", want: true},
		{name: "equal", datasetKeep: "720h", viewKeep: "720h", want: true},
		{name: "dataset longer", datasetKeep: "721h", viewKeep: "720h", want: true},
		{name: "dataset shorter", datasetKeep: "719h", viewKeep: "720h", want: false},
		{name: "view permanent and dataset finite", datasetKeep: "720h", viewKeep: "0", want: false},
		{name: "both permanent", datasetKeep: "0", viewKeep: "0", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := keepDurationCovers(test.datasetKeep, test.viewKeep)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("keepDurationCovers(%q, %q) = %t, want %t", test.datasetKeep, test.viewKeep, got, test.want)
			}
		})
	}
}

func TestUpsertViewRejectsDatasetWithShorterKeepDuration(t *testing.T) {
	ctx, store := newKeepDurationStore(t)
	createKeepDurationDataset(t, ctx, store, "short", "719h")
	createKeepDurationDataset(t, ctx, store, "long", "721h")

	_, err := store.UpsertView(ctx, &pb.View{
		SpaceId: "space", ViewId: "view", Name: "View", PrimaryDatasetId: "long",
		DatasetIds: []string{"long", "short", "short"}, KeepDuration: "720h",
	})
	if !errors.Is(err, ErrDatasetKeepDurationShorterThanView) {
		t.Fatalf("UpsertView() error = %v, want %v", err, ErrDatasetKeepDurationShorterThanView)
	}
	if _, getErr := store.GetView(ctx, "space", "view"); getErr == nil {
		t.Fatal("rejected view was persisted")
	}
}

func TestUpsertViewAllowsCoveredAndPermanentKeepDuration(t *testing.T) {
	ctx, store := newKeepDurationStore(t)
	createKeepDurationDataset(t, ctx, store, "finite", "720h")
	createKeepDurationDataset(t, ctx, store, "permanent", "0")

	for _, view := range []*pb.View{
		{SpaceId: "space", ViewId: "equal", Name: "Equal", PrimaryDatasetId: "finite", KeepDuration: "720h"},
		{SpaceId: "space", ViewId: "permanent", Name: "Permanent", PrimaryDatasetId: "permanent", KeepDuration: "0"},
	} {
		if _, err := store.UpsertView(ctx, view); err != nil {
			t.Fatalf("UpsertView(%s) error = %v", view.GetViewId(), err)
		}
	}
}

func TestUpdateDatasetRejectsKeepDurationShorterThanExistingView(t *testing.T) {
	ctx, store := newKeepDurationStore(t)
	dataset := createKeepDurationDataset(t, ctx, store, "prices", "720h")
	if _, err := store.UpsertView(ctx, &pb.View{
		SpaceId: "space", ViewId: "weekly", Name: "Weekly", PrimaryDatasetId: "prices", KeepDuration: "168h",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "prices", Name: dataset.GetName(),
		KeepDuration: "24h", Status: "disabled", Revision: dataset.GetRevision(),
	})
	if !errors.Is(err, ErrDatasetKeepDurationShorterThanView) {
		t.Fatalf("UpdateDataset() error = %v, want %v", err, ErrDatasetKeepDurationShorterThanView)
	}
	got, getErr := store.GetDataset(ctx, "space", "prices")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got.GetKeepDuration() != "720h0m0s" || got.GetRevision() != dataset.GetRevision() {
		t.Fatalf("rejected update changed dataset: %+v", got)
	}

	permanent, err := store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "prices", Name: dataset.GetName(),
		KeepDuration: "0", Status: "disabled", Revision: dataset.GetRevision(),
	})
	if err != nil {
		t.Fatalf("permanent UpdateDataset() error = %v", err)
	}
	if permanent.GetKeepDuration() != "0" {
		t.Fatalf("keep_duration = %q, want 0", permanent.GetKeepDuration())
	}
}

func newKeepDurationStore(t *testing.T) (context.Context, *Store) {
	t.Helper()
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node")
	return ctx, store
}

func createKeepDurationDataset(t *testing.T, ctx context.Context, store *Store, datasetID, keepDuration string) *pb.Dataset {
	t.Helper()
	item, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: datasetID, DataSourceId: "source", DataNodeId: "node",
		Name: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, KeepDuration: keepDuration,
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
