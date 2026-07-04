package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

type fixedJobItemClock struct {
	now time.Time
}

func (c fixedJobItemClock) Now() time.Time {
	return c.now
}

func TestSubmitJobItemsDeduplicatesBySpaceAndJobItemID(t *testing.T) {
	db := newJobItemTestDB(t)
	repo := NewJobItemRepository(db)
	ctx := context.Background()
	item := testJobItem("space-a", "job-1", "ji-1")

	acks, err := repo.Submit(ctx, []*pb.JobItem{item})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if acks[0].GetStatus() != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED {
		t.Fatalf("first ack status = %v, want CREATED", acks[0].GetStatus())
	}

	acks, err = repo.Submit(ctx, []*pb.JobItem{item})
	if err != nil {
		t.Fatalf("duplicate Submit() error = %v", err)
	}
	if acks[0].GetStatus() != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED {
		t.Fatalf("duplicate ack status = %v, want DEDUPLICATED", acks[0].GetStatus())
	}
}

func TestPollJobItemsDispatchesByPackageAndJobType(t *testing.T) {
	db := newJobItemTestDB(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	repo := NewJobItemRepositoryWithOptions(db, JobItemRepositoryOptions{Clock: fixedJobItemClock{now: now}})
	ctx := context.Background()
	insertPollingNode(t, db, "space-a", "node-a", "collector-scf")
	if _, err := repo.Submit(ctx, []*pb.JobItem{
		testJobItem("space-a", "job-1", "ji-kline"),
		testJobItemWithTypeAndPackage("space-a", "job-1", "ji-symbol", "collect.symbol", "collector-scf"),
		testJobItemWithTypeAndPackage("space-a", "job-1", "ji-other-package", "collect.kline", "other-scf"),
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	items, err := repo.Poll(ctx, &pb.PollJobItemsReq{
		SpaceId:           "space-a",
		NodeId:            "node-a",
		SupportedJobTypes: []string{"collect.kline"},
		Limit:             10,
	})
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(items) != 1 || items[0].GetJobItemId() != "ji-kline" {
		t.Fatalf("polled items = %#v, want only ji-kline", items)
	}
	if items[0].GetAttemptNo() != 1 {
		t.Fatalf("attempt_no = %d, want 1", items[0].GetAttemptNo())
	}
}

func TestPollJobItemsUsesDefaultPriorityFIFO(t *testing.T) {
	db := newJobItemTestDB(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	repo := NewJobItemRepositoryWithOptions(db, JobItemRepositoryOptions{Clock: fixedJobItemClock{now: now}})
	ctx := context.Background()
	insertPollingNode(t, db, "space-a", "node-a", "collector-scf")
	if _, err := repo.Submit(ctx, []*pb.JobItem{
		testJobItemWithPriority("space-a", "job-1", "ji-low", 1),
		testJobItemWithPriority("space-a", "job-1", "ji-high", 10),
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	items, err := repo.Poll(ctx, &pb.PollJobItemsReq{
		SpaceId:           "space-a",
		NodeId:            "node-a",
		SupportedJobTypes: []string{"collect.kline"},
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(items) != 1 || items[0].GetJobItemId() != "ji-high" {
		t.Fatalf("polled items = %#v, want ji-high first", items)
	}
}

func TestPollJobItemsRecoversExpiredRunningAttemptAsLost(t *testing.T) {
	db := newJobItemTestDB(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	repo := NewJobItemRepositoryWithOptions(db, JobItemRepositoryOptions{Clock: fixedJobItemClock{now: now}})
	ctx := context.Background()
	insertPollingNode(t, db, "space-a", "node-a", "collector-scf")
	expired := now.Add(-time.Minute)
	if err := db.Create(&JobItem{
		SpaceID:       "space-a",
		JobID:         "job-1",
		JobItemID:     "ji-expired",
		JobType:       "collect.kline",
		CodePackageID: "collector-scf",
		Params:        "{}",
		Status:        JobItemStatusRunning,
		RunningNode:   "node-old",
		AttemptNo:     1,
		RecoverAt:     &expired,
		CreateTime:    now.Add(-time.Hour),
		ModifyTime:    now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("insert job item: %v", err)
	}
	if err := db.Create(&JobItemAttempt{
		SpaceID:    "space-a",
		JobItemID:  "ji-expired",
		AttemptNo:  1,
		NodeID:     "node-old",
		Status:     JobItemAttemptStatusRunning,
		StartedAt:  now.Add(-time.Hour),
		CreateTime: now.Add(-time.Hour),
		ModifyTime: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("insert attempt: %v", err)
	}

	items, err := repo.Poll(ctx, &pb.PollJobItemsReq{
		SpaceId:           "space-a",
		NodeId:            "node-a",
		SupportedJobTypes: []string{"collect.kline"},
		Limit:             10,
	})
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(items) != 1 || items[0].GetAttemptNo() != 2 {
		t.Fatalf("poll result = %#v, want attempt 2", items)
	}
	var oldAttempt JobItemAttempt
	if err := db.Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ?", "space-a", "ji-expired", 1).First(&oldAttempt).Error; err != nil {
		t.Fatalf("load old attempt: %v", err)
	}
	if oldAttempt.Status != JobItemAttemptStatusLost {
		t.Fatalf("old attempt status = %q, want lost", oldAttempt.Status)
	}
}

func TestReportRejectsStaleAttemptNumber(t *testing.T) {
	db := newJobItemTestDB(t)
	repo := NewJobItemRepository(db)
	now := time.Now().UTC()
	if err := db.Create(&JobItem{
		SpaceID:       "space-a",
		JobID:         "job-1",
		JobItemID:     "ji-stale",
		JobType:       "collect.kline",
		CodePackageID: "collector-scf",
		Params:        "{}",
		Status:        JobItemStatusRunning,
		RunningNode:   "node-a",
		AttemptNo:     2,
		CreateTime:    now,
		ModifyTime:    now,
	}).Error; err != nil {
		t.Fatalf("insert job item: %v", err)
	}

	err := repo.Report(context.Background(), &pb.ReportJobItemStatusReq{
		SpaceId:   "space-a",
		NodeId:    "node-a",
		JobItemId: "ji-stale",
		AttemptNo: 1,
		Status:    pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS,
	})
	if !errors.Is(err, ErrStaleJobItemAttempt) {
		t.Fatalf("Report() error = %v, want ErrStaleJobItemAttempt", err)
	}
}

func TestReportRetryableFailureReturnsToPending(t *testing.T) {
	db := newJobItemTestDB(t)
	repo := NewJobItemRepository(db)
	now := time.Now().UTC()
	insertRunningJobItem(t, db, now, "ji-retry", 1)

	if err := repo.Report(context.Background(), &pb.ReportJobItemStatusReq{
		SpaceId:      "space-a",
		NodeId:       "node-a",
		JobItemId:    "ji-retry",
		AttemptNo:    1,
		Status:       pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_FAILED,
		ErrorKind:    pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE,
		ErrorCode:    "UPSTREAM_TEMPORARY",
		ErrorMessage: "temporary",
	}); err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	var got JobItem
	if err := db.Where("c_space_id = ? AND c_job_item_id = ?", "space-a", "ji-retry").First(&got).Error; err != nil {
		t.Fatalf("load job item: %v", err)
	}
	if got.Status != JobItemStatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
}

func TestCancelJobItemOnlyAllowsPending(t *testing.T) {
	db := newJobItemTestDB(t)
	repo := NewJobItemRepository(db)
	now := time.Now().UTC()
	if err := db.Create(&JobItem{
		SpaceID:       "space-a",
		JobID:         "job-1",
		JobItemID:     "ji-cancel",
		JobType:       "collect.kline",
		CodePackageID: "collector-scf",
		Params:        "{}",
		Status:        JobItemStatusPending,
		CreateTime:    now,
		ModifyTime:    now,
	}).Error; err != nil {
		t.Fatalf("insert job item: %v", err)
	}
	if err := repo.Cancel(context.Background(), &pb.CancelJobItemReq{SpaceId: "space-a", JobItemId: "ji-cancel"}); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	var got JobItem
	if err := db.Where("c_space_id = ? AND c_job_item_id = ?", "space-a", "ji-cancel").First(&got).Error; err != nil {
		t.Fatalf("load job item: %v", err)
	}
	if got.Status != JobItemStatusCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
}

func newJobItemTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&CloudNode{}, &JobItem{}, &JobItemAttempt{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func testJobItem(spaceID string, jobID string, jobItemID string) *pb.JobItem {
	return testJobItemWithTypeAndPackage(spaceID, jobID, jobItemID, "collect.kline", "collector-scf")
}

func testJobItemWithTypeAndPackage(spaceID string, jobID string, jobItemID string, jobType string, codePackageID string) *pb.JobItem {
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	return &pb.JobItem{
		SpaceId:       spaceID,
		JobId:         jobID,
		JobItemId:     jobItemID,
		JobType:       jobType,
		CodePackageId: codePackageID,
		Params:        params,
	}
}

func testJobItemWithPriority(spaceID string, jobID string, jobItemID string, priority int32) *pb.JobItem {
	item := testJobItem(spaceID, jobID, jobItemID)
	item.Priority = priority
	return item
}

func insertPollingNode(t *testing.T, db *gorm.DB, spaceID string, nodeID string, packageID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&CloudNode{
		SpaceID:            spaceID,
		NodeID:             nodeID,
		PackageID:          packageID,
		Status:             "online",
		SupportedWorkloads: "[]",
		CreateTime:         now,
		ModifyTime:         now,
	}).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
}

func insertRunningJobItem(t *testing.T, db *gorm.DB, now time.Time, jobItemID string, attemptNo int) {
	t.Helper()
	if err := db.Create(&JobItem{
		SpaceID:       "space-a",
		JobID:         "job-1",
		JobItemID:     jobItemID,
		JobType:       "collect.kline",
		CodePackageID: "collector-scf",
		Params:        "{}",
		Status:        JobItemStatusRunning,
		RunningNode:   "node-a",
		AttemptNo:     attemptNo,
		CreateTime:    now,
		ModifyTime:    now,
	}).Error; err != nil {
		t.Fatalf("insert job item: %v", err)
	}
	if err := db.Create(&JobItemAttempt{
		SpaceID:    "space-a",
		JobItemID:  jobItemID,
		AttemptNo:  attemptNo,
		NodeID:     "node-a",
		Status:     JobItemAttemptStatusRunning,
		StartedAt:  now,
		CreateTime: now,
		ModifyTime: now,
	}).Error; err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
}
