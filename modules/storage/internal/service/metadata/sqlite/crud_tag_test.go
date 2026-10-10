package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func testAutoTag(id string) *pb.Tag {
	return &pb.Tag{SpaceId: "space", TagId: id, TagName: id, Mode: metadata.TagModeAuto, Source: "source", MarketType: "spot"}
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
		"bad id":          {SpaceId: "space", TagId: "Bad-ID", TagName: "x", Mode: metadata.TagModeManual},
		"bad mode":        {SpaceId: "space", TagId: "x1", TagName: "x1", Mode: "sync"},
		"missing source":  {SpaceId: "space", TagId: "x2", TagName: "x2", Mode: metadata.TagModeAuto, MarketType: "spot"},
		"missing market":  {SpaceId: "space", TagId: "x3", TagName: "x3", Mode: metadata.TagModeManual, Source: "source"},
		"bad market type": {SpaceId: "space", TagId: "x4", TagName: "x4", Mode: metadata.TagModeAuto, Source: "source", MarketType: "option"},
		"bad cron":        {SpaceId: "space", TagId: "x5", TagName: "x5", Mode: metadata.TagModeManual, Source: "source", MarketType: "spot", Cron: "* * *"},
		"bad timezone":    {SpaceId: "space", TagId: "x6", TagName: "x6", Mode: metadata.TagModeManual, Source: "source", MarketType: "spot", Timezone: "Mars/Base"},
		"unknown source":  {SpaceId: "space", TagId: "x7", TagName: "x7", Mode: metadata.TagModeAuto, Source: "missing", MarketType: "spot"},
	}
	for name, item := range cases {
		if _, err := store.UpsertTag(ctx, item); !errors.Is(err, metadata.ErrTagInvalid) {
			t.Errorf("%s: err = %v, want ErrTagInvalid", name, err)
		}
	}
}

func TestCreateTagRejectsExistingTagWithoutReplacingIt(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	original := testAutoTag("unique_tag")
	original.TagName = "Original"
	if _, err := store.UpsertTag(ctx, original); err != nil {
		t.Fatal(err)
	}
	duplicate := testAutoTag("unique_tag")
	duplicate.TagName = "Replacement"
	duplicate.Builtin = true
	if _, err := store.CreateTag(ctx, duplicate); err == nil {
		t.Fatal("expected duplicate tag error")
	}
	got, err := store.GetTag(ctx, "space", "unique_tag")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTagName() != "Original" || got.GetBuiltin() {
		t.Fatalf("existing tag was changed: %+v", got)
	}
}

func TestCreateTagReportsDuplicateNameSeparatelyFromDuplicateID(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	first := testAutoTag("first_tag")
	first.TagName = "Shared name"
	if _, err := store.CreateTag(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := testAutoTag("second_tag")
	second.TagName = "Shared name"
	if _, err := store.CreateTag(ctx, second); err == nil || !strings.Contains(err.Error(), "tag name") {
		t.Fatalf("err = %v, want duplicate tag name", err)
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

func TestCreateTagAllowsMissingSubjectListingCapabilityFailOpen(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)

	// Bootstrap seeds DataSources/Tags before the subject sync in Collector has published
	// subject_listing. Missing capability metadata is therefore fail-open; the
	// planner/provider still validates the concrete route before dispatch.
	if _, err := store.CreateTag(ctx, &pb.Tag{
		SpaceId: "space", TagId: "legacy_spot", TagName: "legacy spot", Mode: metadata.TagModeAuto, Source: "source", MarketType: "spot",
	}); err != nil {
		t.Fatalf("missing subject_listing metadata must remain fail-open: %v", err)
	}
}

func TestCreateTagEnforcesPublishedSubjectListingCapabilities(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	_, err := store.UpsertDataSource(ctx, &pb.DataSource{
		SpaceId: "space", DataSourceId: "capable", Name: "capable", Kind: "internal", Status: "active",
		Attributes: map[string]string{"subject_listing": `{"instrument_types":["spot"]}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTag(ctx, &pb.Tag{SpaceId: "space", TagId: "capable_spot", TagName: "capable spot", Mode: metadata.TagModeAuto, Source: "capable", MarketType: "spot"}); err != nil {
		t.Fatalf("supported market type rejected: %v", err)
	}
	_, err = store.CreateTag(ctx, &pb.Tag{SpaceId: "space", TagId: "capable_swap", TagName: "capable swap", Mode: metadata.TagModeAuto, Source: "capable", MarketType: "swap"})
	if !errors.Is(err, metadata.ErrTagInvalid) || !strings.Contains(err.Error(), "does not support market_type") {
		t.Fatalf("unsupported market type err=%v, want ErrTagInvalid capability diagnostic", err)
	}
}

func TestCreateTagRejectsCorruptPublishedSubjectListingCapabilities(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	_, err := store.UpsertDataSource(ctx, &pb.DataSource{
		SpaceId: "space", DataSourceId: "broken", Name: "broken", Kind: "internal", Status: "active",
		Attributes: map[string]string{"subject_listing": `{not-json}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateTag(ctx, &pb.Tag{SpaceId: "space", TagId: "broken_spot", TagName: "broken spot", Mode: metadata.TagModeAuto, Source: "broken", MarketType: "spot"})
	if !errors.Is(err, metadata.ErrTagInvalid) || !strings.Contains(err.Error(), "invalid subject_listing") {
		t.Fatalf("corrupt capability err=%v, want ErrTagInvalid", err)
	}
}
