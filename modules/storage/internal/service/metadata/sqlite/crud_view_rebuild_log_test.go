package sqlite

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestViewRebuildLogLifecycleAndSkippedDeduplication(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)

	running, err := store.CreateViewRebuildLog(ctx, &pb.ViewRebuildLog{
		SpaceId: "space", ViewId: "source-view", BuildId: "build-1", IndexId: "index-b",
		TriggerReason: pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SIZE_LIMIT,
		Result:        pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING,
		StartedAt:     "2026-08-14T01:00:00Z", DetailsJson: `{"phase":"backfill"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if running.GetLogId() == 0 || running.GetCreatedAt() == "" || running.GetDetailsJson() == "" {
		t.Fatalf("running log = %v", running)
	}
	running.Result = pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED
	running.FinishedAt = "2026-08-14T01:01:00Z"
	running.EntriesWritten = 42
	updated, err := store.UpdateViewRebuildLog(ctx, running)
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetResult() != pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED || updated.GetEntriesWritten() != 42 {
		t.Fatalf("updated log = %v", updated)
	}
	updated.Result = pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED
	if _, err := store.UpdateViewRebuildLog(ctx, updated); err == nil {
		t.Fatal("terminal rebuild log was overwritten")
	}

	for i := 0; i < 2; i++ {
		if _, err := store.UpsertSkippedViewRebuildLog(ctx, &pb.ViewRebuildLog{
			SpaceId: "space", ViewId: "source-view",
			TriggerReason: pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SIZE_LIMIT,
			Result:        pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED,
			BlockReason:   "pending backlog",
			NumPending:    uint64(i + 1),
		}); err != nil {
			t.Fatal(err)
		}
	}
	logs, page, err := store.ListViewRebuildLogs(ctx, "space", "source-view", pb.ViewRebuildResult_VIEW_REBUILD_RESULT_UNSPECIFIED, &pb.Page{Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.GetTotal() != 2 || len(logs) != 2 {
		t.Fatalf("logs=%v page=%v", logs, page)
	}
	var skipped *pb.ViewRebuildLog
	for _, item := range logs {
		if item.GetResult() == pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED {
			skipped = item
		}
	}
	if skipped == nil || skipped.GetSkipCount() != 2 || skipped.GetNumPending() != 2 {
		t.Fatalf("deduplicated skipped log = %v", skipped)
	}
	if _, err := store.CreateViewRebuildLog(ctx, &pb.ViewRebuildLog{
		SpaceId: "space", ViewId: "source-view", BuildId: "build-2", IndexId: "index-a",
		TriggerReason: pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SIZE_LIMIT,
		Result:        pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSkippedViewRebuildLog(ctx, &pb.ViewRebuildLog{
		SpaceId: "space", ViewId: "source-view",
		TriggerReason: pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SIZE_LIMIT,
		Result:        pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED,
		BlockReason:   "pending backlog", NumPending: 3,
	}); err != nil {
		t.Fatal(err)
	}
	logs, _, err = store.ListViewRebuildLogs(ctx, "space", "source-view", pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED, &pb.Page{Page: 1, Size: 20})
	if err != nil || len(logs) != 2 || logs[0].GetSkipCount() != 1 {
		t.Fatalf("skip streak should restart after success: logs=%v err=%v", logs, err)
	}
}
