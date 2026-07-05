package projection

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestProjectionRepositoryCreatesDeduplicatesRunsReportsAndLists(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newProjectionTestDB(t), RepositoryOptions{
		Clock:              fixedClock{now: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)},
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	item := testProjectionJobItem(t, "crypto", "job-1", "ji-1")
	meta := QueueMeta{Subject: "moox.cloudnode.exec.v1.jobitem.s.crypto.pkg.collector.type.collect_kline", Stream: "MOOX_CLOUDNODE_EXEC", StreamSeq: 1}

	created, err := repo.CreatePending(ctx, item, meta)
	if err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if !created.Created || created.Status != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED {
		t.Fatalf("created = %+v", created)
	}
	duplicate, err := repo.CreatePending(ctx, item, meta)
	if err != nil {
		t.Fatalf("duplicate CreatePending() error = %v", err)
	}
	if !duplicate.Deduplicated || duplicate.Status != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED {
		t.Fatalf("duplicate = %+v", duplicate)
	}

	ok, running, err := repo.TryMarkRunning(ctx, RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-1",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.demo",
		StreamSeq:  1,
	})
	if err != nil {
		t.Fatalf("TryMarkRunning() error = %v", err)
	}
	if !ok || running.AttemptNo != 1 || running.AckSubject != "$JS.ACK.demo" {
		t.Fatalf("running = ok:%v %+v", ok, running)
	}

	reportTime := time.Date(2026, 7, 5, 10, 0, 1, 0, time.UTC)
	err = repo.MarkReportedBatch(ctx, []ReportEvent{{
		SpaceID:       "crypto",
		JobItemID:     "ji-1",
		NodeID:        "node-1",
		AttemptNo:     1,
		Status:        ReportStatusSuccess,
		ResultSummary: map[string]any{"rows": float64(3)},
		DurationMS:    42,
		Time:          reportTime,
	}})
	if err != nil {
		t.Fatalf("MarkReportedBatch() error = %v", err)
	}

	detail, err := repo.Get(ctx, &pb.GetJobItemReq{SpaceId: "crypto", JobItemId: "ji-1"})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.GetStatus() != pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS || detail.GetAttemptNo() != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	if got := detail.GetResultSummary().GetFields()["rows"].GetNumberValue(); got != 3 {
		t.Fatalf("result rows = %v, want 3", got)
	}

	items, page, err := repo.List(ctx, &pb.ListJobItemsReq{SpaceId: "crypto"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 || page.GetTotal() != 1 {
		t.Fatalf("List() items=%d page=%+v", len(items), page)
	}
	attempts, err := repo.ListAttempts(ctx, &pb.ListJobItemAttemptsReq{SpaceId: "crypto", JobItemId: "ji-1"})
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].GetStatus() != pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_SUCCESS {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestProjectionRepositoryCancelsPendingItem(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newProjectionTestDB(t), RepositoryOptions{Clock: fixedClock{now: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)}})
	if _, err := repo.CreatePending(ctx, testProjectionJobItem(t, "crypto", "job-1", "ji-cancel"), QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}

	if err := repo.MarkCanceled(ctx, "crypto", "ji-cancel", "user canceled"); err != nil {
		t.Fatalf("MarkCanceled() error = %v", err)
	}
	detail, err := repo.Get(ctx, &pb.GetJobItemReq{SpaceId: "crypto", JobItemId: "ji-cancel"})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.GetStatus() != pb.JobItemStatus_JOB_ITEM_STATUS_CANCELED {
		t.Fatalf("status = %v, want canceled", detail.GetStatus())
	}
}

func TestProjectionRepositoryRedeliveryRecoversExpiredRunningAttempt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	repo := NewRepository(newProjectionTestDB(t), RepositoryOptions{
		Clock:              fixedClock{now: now},
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	if _, err := repo.CreatePending(ctx, testProjectionJobItem(t, "crypto", "job-1", "ji-retry"), QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := repo.TryMarkRunning(ctx, RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-retry",
		NodeID:     "node-old",
		AckSubject: "$JS.ACK.old",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("first TryMarkRunning() ok=%v err=%v", ok, err)
	}

	repo.clock = fixedClock{now: now.Add(2 * time.Minute)}
	ok, running, err := repo.TryMarkRunning(ctx, RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-retry",
		NodeID:     "node-new",
		AckSubject: "$JS.ACK.new",
		StreamSeq:  2,
	})
	if err != nil {
		t.Fatalf("redelivery TryMarkRunning() error = %v", err)
	}
	if !ok || running.AttemptNo != 2 {
		t.Fatalf("redelivery ok=%v running=%+v, want attempt 2", ok, running)
	}
	attempts, err := repo.ListAttempts(ctx, &pb.ListJobItemAttemptsReq{SpaceId: "crypto", JobItemId: "ji-retry"})
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(attempts))
	}
	if attempts[0].GetStatus() != pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_LOST {
		t.Fatalf("old attempt status = %v, want lost", attempts[0].GetStatus())
	}
	if attempts[1].GetStatus() != pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_RUNNING {
		t.Fatalf("new attempt status = %v, want running", attempts[1].GetStatus())
	}
}

func TestProjectionRepositoryCancelRunningMarksAttemptCanceled(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	repo := NewRepository(newProjectionTestDB(t), RepositoryOptions{
		Clock:              fixedClock{now: now},
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	if _, err := repo.CreatePending(ctx, testProjectionJobItem(t, "crypto", "job-1", "ji-cancel-running"), QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := repo.TryMarkRunning(ctx, RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-cancel-running",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.cancel",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("TryMarkRunning() ok=%v err=%v", ok, err)
	}
	if err := repo.MarkCanceled(ctx, "crypto", "ji-cancel-running", "user canceled"); err != nil {
		t.Fatalf("MarkCanceled() error = %v", err)
	}
	attempts, err := repo.ListAttempts(ctx, &pb.ListJobItemAttemptsReq{SpaceId: "crypto", JobItemId: "ji-cancel-running"})
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].GetStatus() != pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_CANCELED {
		t.Fatalf("attempts = %+v, want one canceled attempt", attempts)
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func newProjectionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&JobItem{}, &JobItemAttempt{}); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}
	return db
}

func testProjectionJobItem(t *testing.T, spaceID, jobID, itemID string) *pb.JobItem {
	t.Helper()
	params, err := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	if err != nil {
		t.Fatalf("NewStruct() error = %v", err)
	}
	return &pb.JobItem{
		SpaceId:       spaceID,
		JobId:         jobID,
		JobItemId:     itemID,
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
		Priority:      10,
	}
}
