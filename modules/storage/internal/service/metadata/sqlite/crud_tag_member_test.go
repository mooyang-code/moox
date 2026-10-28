package sqlite

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestApplyAutoTagSnapshotResolvesAndInactivatesMembers(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.UpsertTag(ctx, testAutoTag("assets")); err != nil {
		t.Fatal(err)
	}

	result, err := store.ApplyTagSnapshot(ctx, "space", "assets", []*pb.TagSnapshotItem{
		{SubjectId: "BTC-USDT", Name: "BTC"},
		{SubjectId: "ETH-USDT", Name: "ETH"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 2 || result.Activated != 0 || result.Inactivated != 0 {
		t.Fatalf("first snapshot result = %+v", result)
	}

	result, err = store.ApplyTagSnapshot(ctx, "space", "assets", []*pb.TagSnapshotItem{
		{SubjectId: "BTC-USDT", Name: "Bitcoin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Inactivated != 1 {
		t.Fatalf("second snapshot result = %+v", result)
	}
	resolved, err := store.ResolveSubjects(ctx, "space", []string{"assets"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].GetSubjectId() != "BTC-USDT" || resolved[0].GetName() != "BTC" {
		t.Fatalf("resolved subjects = %v", resolved)
	}
	all, _, err := store.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: "space", TagID: "assets", Page: &pb.Page{Page: 1, Size: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].GetStatus() != metadata.TagMemberActive || all[1].GetStatus() != metadata.TagMemberInactive {
		t.Fatalf("members after snapshot = %v", all)
	}
	if all[1].GetInactiveAt() == "" {
		t.Fatalf("被移出的成员应记录收到快照的时间：%v", all[1])
	}
}

func TestApplyAutoTagSnapshotRejectsEmptySnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.UpsertTag(ctx, testAutoTag("assets")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "assets", nil); err == nil {
		t.Fatal("empty auto snapshot must not clear members")
	}
	if _, err := store.GetTag(ctx, "space", "assets"); err != nil {
		t.Fatal(err)
	}
}

func TestManualTagSnapshotDoesNotCreateMembers(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertTag(ctx, &pb.Tag{SpaceId: "space", TagId: "manual", TagName: "Manual", Mode: metadata.TagModeManual}); err != nil {
		t.Fatal(err)
	}
	if affected, err := store.AddTagMembers(ctx, "space", "manual", []string{"a", "b"}); err != nil || affected != 2 {
		t.Fatalf("add members affected=%d err=%v", affected, err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "manual", []*pb.TagSnapshotItem{{SubjectId: "a"}, {SubjectId: "c"}}); err != nil {
		t.Fatal(err)
	}
	members, _, err := store.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: "space", TagID: "manual", Page: &pb.Page{Page: 1, Size: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].GetSubject().GetSubjectId() != "a" || members[0].GetStatus() != metadata.TagMemberActive || members[1].GetSubject().GetSubjectId() != "b" || members[1].GetStatus() != metadata.TagMemberInactive {
		t.Fatalf("manual members = %v", members)
	}
}

func TestListAllSubjectsIncludesUnassignedSubjects(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	for _, id := range []string{"assigned", "unassigned"} {
		if _, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertTag(ctx, &pb.Tag{SpaceId: "space", TagId: "manual", TagName: "Manual", Mode: metadata.TagModeManual}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTagMembers(ctx, "space", "manual", []string{"assigned"}); err != nil {
		t.Fatal(err)
	}
	members, _, err := store.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: "space", Page: &pb.Page{Page: 1, Size: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].GetTagId() != "" || members[1].GetTagId() != "" {
		t.Fatalf("all subject projection = %v", members)
	}
	if len(members[0].GetTagIds()) != 1 || members[0].GetTagIds()[0] != "manual" {
		t.Fatalf("assigned tags = %v", members[0].GetTagIds())
	}
}

func TestListAllSubjectsFiltersBySubjectStatus(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	for _, subject := range []*pb.Subject{
		{SpaceId: "space", SubjectId: "active-subject", Name: "Active", Status: "active"},
		{SpaceId: "space", SubjectId: "disabled-subject", Name: "Disabled", Status: "disabled"},
	} {
		if _, err := store.UpsertSubject(ctx, subject); err != nil {
			t.Fatal(err)
		}
	}
	members, page, err := store.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: "space", Status: "disabled", Page: &pb.Page{Page: 1, Size: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if page.GetTotal() != 1 || len(members) != 1 || members[0].GetSubject().GetSubjectId() != "disabled-subject" {
		t.Fatalf("disabled subject filter members=%v page=%v", members, page)
	}
}

func TestUpdateSubjectAttributesDoesNotCreateOrChangeMembership(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: "known", Name: "Old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertTag(ctx, &pb.Tag{SpaceId: "space", TagId: "manual", TagName: "Manual", Mode: metadata.TagModeManual}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTagMembers(ctx, "space", "manual", []string{"known"}); err != nil {
		t.Fatal(err)
	}
	updated, skipped, err := store.UpdateSubjectAttributes(ctx, "space", []*pb.SubjectAttributes{
		{SubjectId: "known", Name: "New", Attributes: map[string]string{"base": "BTC"}},
		{SubjectId: "missing", Name: "Missing"},
	})
	if err != nil || updated != 1 || skipped != 1 {
		t.Fatalf("updated=%d skipped=%d err=%v", updated, skipped, err)
	}
	known, err := store.GetSubject(ctx, "space", "known")
	if err != nil || known.GetName() != "New" || known.GetAttributes()["base"] != "BTC" {
		t.Fatalf("known subject = %v err=%v", known, err)
	}
	if _, _, err := store.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: "space", TagID: "manual", Page: &pb.Page{Page: 1, Size: 20}}); err != nil {
		t.Fatal(err)
	}
}

func TestListDatasetSubjectsUsesTagUnion(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node")
	if _, err := store.UpsertTag(ctx, testAutoTag("tag_a")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertTag(ctx, testAutoTag("tag_b")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "tag_a", []*pb.TagSnapshotItem{{SubjectId: "A"}, {SubjectId: "B"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "tag_b", []*pb.TagSnapshotItem{{SubjectId: "B"}, {SubjectId: "C"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "tag_b", []*pb.TagSnapshotItem{{SubjectId: "B"}}); err != nil {
		t.Fatal(err)
	}
	dataset := createTestDataset(t, ctx, store, "dataset_ab", "node")
	dataset.SubjectTags = []string{"tag_a", "tag_b"}
	if _, err := store.UpdateDataset(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	rows, page, err := store.ListDatasetSubjects(ctx, "space", "dataset_ab", "", &pb.Page{Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.GetTotal() != 3 || len(rows) != 3 {
		t.Fatalf("rows=%d total=%d", len(rows), page.GetTotal())
	}
	status := map[string]string{}
	for _, row := range rows {
		status[row.GetSubjectId()] = row.GetStatus()
	}
	if status["A"] != "active" || status["B"] != "active" || status["C"] != "inactive" {
		t.Fatalf("status = %v", status)
	}
}
