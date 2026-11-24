package sqlite

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func setTradingStatus(t *testing.T, ctx context.Context, store *Store, tagID string, pairs ...string) (int, int) {
	t.Helper()
	items := make([]*pb.TagMemberTradingStatus, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		items = append(items, &pb.TagMemberTradingStatus{SubjectId: pairs[i], TradingStatus: pairs[i+1]})
	}
	updated, skipped, err := store.SetTagMemberTradingStatus(ctx, "space", tagID, items)
	if err != nil {
		t.Fatal(err)
	}
	return updated, skipped
}

func resolvedIDs(t *testing.T, ctx context.Context, store *Store, tagIDs ...string) []string {
	t.Helper()
	subjects, err := store.ResolveSubjects(ctx, "space", tagIDs)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		ids = append(ids, subject.GetSubjectId())
	}
	return ids
}

func datasetSubjects(t *testing.T, ctx context.Context, store *Store, datasetID string) map[string]*pb.DatasetSubject {
	t.Helper()
	rows, _, err := store.ListDatasetSubjects(ctx, "space", datasetID, "", &pb.Page{Page: 1, Size: 50})
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*pb.DatasetSubject, len(rows))
	for _, row := range rows {
		out[row.GetSubjectId()] = row
	}
	return out
}

// 交易状态按标签成员记：不可交易的成员不在 ResolveSubjects 里、在 ListDatasetSubjects 里不活跃；只有停牌（不是退市、
// 也不是被移出标签）的标的 suspended 为真；同一标的在一个标签停牌、另一个标签正常时按标签分别解析。
func TestTradingStatusFiltersActiveQueriesPerTagMember(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	for _, tag := range []string{"spot", "swap"} {
		if _, err := store.UpsertTag(ctx, testAutoTag(tag)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "spot", []*pb.TagSnapshotItem{{SubjectId: "A"}, {SubjectId: "B"}, {SubjectId: "C"}, {SubjectId: "D"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "swap", []*pb.TagSnapshotItem{{SubjectId: "A"}, {SubjectId: "B"}}); err != nil {
		t.Fatal(err)
	}
	single := createTestDataset(t, ctx, store, "dataset_spot")
	single.SubjectTags = []string{"spot"}
	if _, err := store.UpdateDataset(ctx, single); err != nil {
		t.Fatal(err)
	}
	unified := createTestDataset(t, ctx, store, "dataset_unified")
	unified.SubjectTags = []string{"spot", "swap"}
	if _, err := store.UpdateDataset(ctx, unified); err != nil {
		t.Fatal(err)
	}

	// 新成员默认可交易。
	members, _, err := store.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: "space", TagID: "spot", Page: &pb.Page{Page: 1, Size: 20}})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.GetTradingStatus() != metadata.TradingStatusTrading {
			t.Fatalf("新成员默认应可交易：%v", member)
		}
	}

	// A：现货停牌、永续正常；B：现货与永续都停牌；C：现货退市；D：现货停牌后又被移出标签。
	setTradingStatus(t, ctx, store, "spot", "A", "suspended", "B", "suspended", "C", "delisted", "D", "suspended")
	setTradingStatus(t, ctx, store, "swap", "B", "suspended")
	if _, err := store.ApplyTagSnapshot(ctx, "space", "spot", []*pb.TagSnapshotItem{{SubjectId: "A"}, {SubjectId: "B"}, {SubjectId: "C"}}); err != nil {
		t.Fatal(err)
	}

	if got := resolvedIDs(t, ctx, store, "spot"); len(got) != 0 {
		t.Fatalf("现货里没有可交易的成员，ResolveSubjects 应为空：%v", got)
	}
	if got := resolvedIDs(t, ctx, store, "swap"); len(got) != 1 || got[0] != "A" {
		t.Fatalf("永续只有 A 可交易：%v", got)
	}

	rows := datasetSubjects(t, ctx, store, "dataset_spot")
	for id, want := range map[string]struct {
		status    string
		suspended bool
	}{
		"A": {"inactive", true}, "B": {"inactive", true}, "C": {"inactive", false}, "D": {"inactive", false},
	} {
		if rows[id].GetStatus() != want.status || rows[id].GetSuspended() != want.suspended {
			t.Fatalf("%s：status=%s suspended=%v，want %s %v", id, rows[id].GetStatus(), rows[id].GetSuspended(), want.status, want.suspended)
		}
	}
	rows = datasetSubjects(t, ctx, store, "dataset_unified")
	if rows["A"].GetStatus() != "active" || rows["A"].GetSuspended() {
		t.Fatalf("统一数据集里 A 的永续可交易，标的仍活跃：%v", rows["A"])
	}
	if rows["B"].GetStatus() != "inactive" || !rows["B"].GetSuspended() {
		t.Fatalf("统一数据集里 B 两边都停牌：%v", rows["B"])
	}

	// ListTagMembers 不过滤，带出交易状态。
	members, _, err = store.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: "space", TagID: "spot", Status: metadata.TagMemberActive, Page: &pb.Page{Page: 1, Size: 20}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, member := range members {
		got[member.GetSubject().GetSubjectId()] = member.GetTradingStatus()
	}
	if len(got) != 3 || got["A"] != "suspended" || got["C"] != "delisted" {
		t.Fatalf("成员列表应列出停牌、退市的成员并带出状态：%v", got)
	}

	// 复牌：回到解析结果里。
	setTradingStatus(t, ctx, store, "spot", "A", "trading")
	if got := resolvedIDs(t, ctx, store, "spot"); len(got) != 1 || got[0] != "A" {
		t.Fatalf("复牌后 A 回到现货：%v", got)
	}
}

// 值不变不写库、不递增修订号；值变了才递增。不是成员的条目计入跳过。取值非法时整批拒绝。
func TestSetTagMemberTradingStatusSkipsUnchangedRows(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.UpsertTag(ctx, testAutoTag("assets")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "assets", []*pb.TagSnapshotItem{{SubjectId: "A"}, {SubjectId: "B"}}); err != nil {
		t.Fatal(err)
	}

	before := subjectsRevision(t, ctx, store)
	if updated, skipped := setTradingStatus(t, ctx, store, "assets", "A", "trading", "B", "trading", "GHOST", "suspended"); updated != 0 || skipped != 3 {
		t.Fatalf("值没变、不是成员：updated=%d skipped=%d", updated, skipped)
	}
	if after := subjectsRevision(t, ctx, store); after != before {
		t.Fatalf("值没变不应递增修订号：%d -> %d", before, after)
	}
	if updated, skipped := setTradingStatus(t, ctx, store, "assets", "A", "suspended", "B", "trading"); updated != 1 || skipped != 1 {
		t.Fatalf("只有 A 变了：updated=%d skipped=%d", updated, skipped)
	}
	if after := subjectsRevision(t, ctx, store); after <= before {
		t.Fatalf("值变了要递增修订号：%d -> %d", before, after)
	}

	if _, _, err := store.SetTagMemberTradingStatus(ctx, "space", "assets", []*pb.TagMemberTradingStatus{{SubjectId: "A", TradingStatus: "halted"}}); err == nil {
		t.Fatal("非法取值应拒绝")
	}

	// 标签同步只改成员状态，不动交易状态：移出又加回的成员保留旧值。
	if _, err := store.ApplyTagSnapshot(ctx, "space", "assets", []*pb.TagSnapshotItem{{SubjectId: "B"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "assets", []*pb.TagSnapshotItem{{SubjectId: "A"}, {SubjectId: "B"}}); err != nil {
		t.Fatal(err)
	}
	members, _, err := store.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: "space", TagID: "assets", Page: &pb.Page{Page: 1, Size: 20}})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.GetSubject().GetSubjectId() == "A" && (member.GetStatus() != "active" || member.GetTradingStatus() != "suspended") {
			t.Fatalf("重新激活的成员保留旧的交易状态：%v", member)
		}
	}
}
