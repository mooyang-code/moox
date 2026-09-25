package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func testAutoTag(id string) *pb.Tag {
	return &pb.Tag{SpaceId: "space", TagId: id, TagName: id, Mode: metadata.TagModeAuto, Sources: []string{"binance"}, InstrumentType: "spot"}
}

func TestUpsertTagDefaultsAndValidation(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)

	got, err := store.UpsertTag(ctx, testAutoTag("binance_spot"))
	if err != nil {
		t.Fatal(err)
	}
	if got.GetCron() != "0 * * * *" || got.GetTimezone() != "UTC" || got.GetBuiltin() {
		t.Fatalf("defaults not applied: %v", got)
	}

	cases := map[string]*pb.Tag{
		"bad id":               {SpaceId: "space", TagId: "Bad-ID", TagName: "x", Mode: metadata.TagModeManual},
		"bad mode":             {SpaceId: "space", TagId: "x1", TagName: "x1", Mode: "sync"},
		"auto without sources": {SpaceId: "space", TagId: "x2", TagName: "x2", Mode: metadata.TagModeAuto, InstrumentType: "spot"},
		"manual half probe":    {SpaceId: "space", TagId: "x3", TagName: "x3", Mode: metadata.TagModeManual, Sources: []string{"binance"}},
		"bad instrument type":  {SpaceId: "space", TagId: "x4", TagName: "x4", Mode: metadata.TagModeAuto, Sources: []string{"binance"}, InstrumentType: "option"},
		"bad cron":             {SpaceId: "space", TagId: "x5", TagName: "x5", Mode: metadata.TagModeManual, Cron: "* * *"},
		"bad timezone":         {SpaceId: "space", TagId: "x6", TagName: "x6", Mode: metadata.TagModeManual, Timezone: "Mars/Base"},
	}
	for name, item := range cases {
		if _, err := store.UpsertTag(ctx, item); !errors.Is(err, metadata.ErrTagInvalid) {
			t.Errorf("%s: err = %v, want ErrTagInvalid", name, err)
		}
	}
}

func TestUpsertTagKeepsBuiltinFlag(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	item := testAutoTag("binance_spot")
	item.Builtin = true
	if _, err := store.UpsertTag(ctx, item); err != nil {
		t.Fatal(err)
	}
	item = testAutoTag("binance_spot")
	item.TagName = "现货"
	got, err := store.UpsertTag(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetBuiltin() || got.GetTagName() != "现货" {
		t.Fatalf("builtin flag must survive edits: %v", got)
	}
}

func TestDeleteTagProtection(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node")

	builtin := testAutoTag("builtin_tag")
	builtin.Builtin = true
	if _, err := store.UpsertTag(ctx, builtin); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTag(ctx, "space", "builtin_tag"); !errors.Is(err, metadata.ErrTagBuiltin) {
		t.Fatalf("err = %v, want ErrTagBuiltin", err)
	}

	if _, err := store.UpsertTag(ctx, testAutoTag("used_tag")); err != nil {
		t.Fatal(err)
	}
	dataset := createTestDataset(t, ctx, store, "dataset_used", "node")
	dataset.SubjectTags = []string{"used_tag"}
	if _, err := store.UpdateDataset(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	err := store.DeleteTag(ctx, "space", "used_tag")
	var refErr *metadata.TagReferencedError
	if !errors.As(err, &refErr) || len(refErr.References) != 1 || refErr.References[0].GetDatasetId() != "dataset_used" {
		t.Fatalf("err = %v, want TagReferencedError(dataset_used)", err)
	}

	if _, err := store.UpsertTag(ctx, testAutoTag("free_tag")); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTag(ctx, "space", "free_tag"); err != nil {
		t.Fatal(err)
	}
}
