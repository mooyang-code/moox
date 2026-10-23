package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceFunctionJobItemPublishFenceCASChangesOnlyFenceEnvelope(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	request := `{"instanceId":"instance-a","packageId":"pkg-new","lifecycleId":"generation-1","operationId":"item-1","environment":{"TOKEN":"secret"},"config":{"timeout":"60"},"collectorPublishLeaseId":"cli-lease","collectorPublishFencingToken":"7"}`
	require.NoError(t, repo.CreateFunctionJob(ctx, FunctionJobCreate{
		SpaceID: "crypto", JobID: "fence-cas", Operation: "deploy_instances",
		Items: []FunctionJobItemCreate{{ItemID: "item-1", ItemIndex: 0, InstanceID: "instance-a", RequestJSON: request}},
	}))
	items, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)

	updated, err := repo.ReplaceFunctionJobItemPublishFence(ctx, "crypto", "fence-cas", "item-1", "cli-lease", 7, "recovery-lease", 8)
	require.NoError(t, err)
	require.True(t, updated.PublishLeaseRecoveryOwned)
	var before, after map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(request), &before))
	require.NoError(t, json.Unmarshal([]byte(updated.RequestJSON), &after))
	assert.Equal(t, before["instanceId"], after["instanceId"])
	assert.Equal(t, before["packageId"], after["packageId"])
	assert.Equal(t, before["lifecycleId"], after["lifecycleId"])
	assert.Equal(t, before["operationId"], after["operationId"])
	assert.Equal(t, before["environment"], after["environment"])
	assert.Equal(t, before["config"], after["config"])
	assert.JSONEq(t, `"recovery-lease"`, string(after["collectorPublishLeaseId"]))
	assert.JSONEq(t, `"8"`, string(after["collectorPublishFencingToken"]))

	_, err = repo.ReplaceFunctionJobItemPublishFence(ctx, "crypto", "fence-cas", "item-1", "cli-lease", 7, "another", 9)
	require.ErrorContains(t, err, "fence changed")
}

func TestRecoveredPublishLeaseReleaseMarkerSurvivesUntilExactTerminalRelease(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	request := `{"instanceId":"instance-a","lifecycleId":"generation-1","operationId":"item-1","collectorPublishLeaseId":"recovery-lease","collectorPublishFencingToken":"8"}`
	require.NoError(t, repo.CreateFunctionJob(ctx, FunctionJobCreate{
		SpaceID: "crypto", JobID: "lease-release", Operation: "deploy_instances",
		Items: []FunctionJobItemCreate{{ItemID: "item-1", ItemIndex: 0, InstanceID: "instance-a", RequestJSON: request}},
	}))
	items, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, err = repo.ReplaceFunctionJobItemPublishFence(ctx, "crypto", "lease-release", "item-1", "recovery-lease", 8, "recovery-lease-2", 9)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteFunctionJobItem(ctx, "crypto", "lease-release", "item-1", "done", nil))

	pending, err := repo.ListTerminalFunctionJobItemsForPublishLeaseRelease(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.True(t, pending[0].PublishLeaseRecoveryOwned)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(pending[0].RequestJSON), &envelope))
	var tokenText string
	require.NoError(t, json.Unmarshal(envelope["collectorPublishFencingToken"], &tokenText))
	token, err := strconv.ParseInt(tokenText, 10, 64)
	require.NoError(t, err)
	require.NoError(t, repo.MarkFunctionJobItemPublishLeaseReleased(ctx, "crypto", "lease-release", "item-1", "recovery-lease-2", token))

	pending, err = repo.ListTerminalFunctionJobItemsForPublishLeaseRelease(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestCreateFunctionJobIsAtomic(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	err := repo.CreateFunctionJob(ctx, FunctionJobCreate{
		SpaceID:   "crypto",
		JobID:     "job-atomic",
		Operation: "create_instances",
		Items: []FunctionJobItemCreate{
			{ItemID: "item-0", ItemIndex: 0, RequestJSON: `{"index":0}`},
			{ItemID: "item-1", ItemIndex: 0, RequestJSON: `{"index":1}`},
		},
	})
	require.Error(t, err)

	var jobCount, itemCount int64
	require.NoError(t, repo.db.Model(&FunctionJob{}).Where("c_job_id = ?", "job-atomic").Count(&jobCount).Error)
	require.NoError(t, repo.db.Model(&FunctionJobItem{}).Where("c_job_id = ?", "job-atomic").Count(&itemCount).Error)
	assert.Zero(t, jobCount)
	assert.Zero(t, itemCount)
}

func TestCreateFunctionJobRejectsStableInstanceWhilePreviousCreateIsPending(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.CreateFunctionJob(ctx, FunctionJobCreate{
		SpaceID: "crypto", JobID: "job-first", Operation: "create_instances",
		Items: []FunctionJobItemCreate{{ItemID: "item-first", ItemIndex: 0, InstanceID: "stable-instance", RequestJSON: `{}`}},
	}))
	err := repo.CreateFunctionJob(ctx, FunctionJobCreate{
		SpaceID: "crypto", JobID: "job-second", Operation: "create_instances",
		Items: []FunctionJobItemCreate{{ItemID: "item-second", ItemIndex: 0, InstanceID: "stable-instance", RequestJSON: `{}`}},
	})
	require.ErrorContains(t, err, "already has a pending create batch")
}

func TestTakePendingFunctionJobItemsMarksWholeBatchRunning(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createFunctionJobFixture(t, repo, "crypto", "job-running", 3)

	items, err := repo.TakePendingFunctionJobItems(ctx, 3)
	require.NoError(t, err)
	require.Len(t, items, 3)
	for _, item := range items {
		assert.Equal(t, FunctionJobRunning, item.Status)
		assert.NotNil(t, item.StartedAt)
	}

	aggregate, err := repo.GetFunctionJob(ctx, "crypto", "job-running")
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, FunctionJobRunning, aggregate.Job.Status)
	assert.Equal(t, 3, aggregate.RunningCount)
}

func TestTakePendingFunctionJobItemsReturnsStableNonOverlappingBatches(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createFunctionJobFixture(t, repo, "crypto", "job-batches", 10)

	var gotSizes []int
	var gotIDs []string
	for range 4 {
		items, err := repo.TakePendingFunctionJobItems(ctx, 3)
		require.NoError(t, err)
		gotSizes = append(gotSizes, len(items))
		for i, item := range items {
			gotIDs = append(gotIDs, item.ItemID)
			if i > 0 {
				assert.Less(t, items[i-1].ID, item.ID)
			}
		}
	}

	assert.Equal(t, []int{3, 3, 3, 1}, gotSizes)
	assert.Equal(t, []string{
		"job-batches-item-00", "job-batches-item-01", "job-batches-item-02",
		"job-batches-item-03", "job-batches-item-04", "job-batches-item-05",
		"job-batches-item-06", "job-batches-item-07", "job-batches-item-08",
		"job-batches-item-09",
	}, gotIDs)

	empty, err := repo.TakePendingFunctionJobItems(ctx, 3)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestTakePendingFunctionJobItemsRejectsNonPositiveLimit(t *testing.T) {
	repo := newTestCatalog(t)
	_, err := repo.TakePendingFunctionJobItems(context.Background(), 0)
	require.Error(t, err)
}

func TestCompleteFunctionJobItemBuildsSuccessStatus(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createFunctionJobFixture(t, repo, "crypto", "job-success", 2)
	items, err := repo.TakePendingFunctionJobItems(ctx, 2)
	require.NoError(t, err)

	require.NoError(t, repo.CompleteFunctionJobItem(ctx, "crypto", "job-success", items[0].ItemID, "created instance 0", nil))
	require.NoError(t, repo.CompleteFunctionJobItem(ctx, "crypto", "job-success", items[1].ItemID, "created instance 1", nil))

	aggregate, err := repo.GetFunctionJob(ctx, "crypto", "job-success")
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, FunctionJobSuccess, aggregate.Job.Status)
	assert.Equal(t, 2, aggregate.SuccessCount)
	assert.NotNil(t, aggregate.Job.CompletedAt)
	assert.Equal(t, "created instance 0", aggregate.Items[0].ResultSummary)
}

func TestCompleteFunctionJobItemBuildsPartialStatus(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createFunctionJobFixture(t, repo, "crypto", "job-partial", 2)
	items, err := repo.TakePendingFunctionJobItems(ctx, 2)
	require.NoError(t, err)

	require.NoError(t, repo.CompleteFunctionJobItem(ctx, "crypto", "job-partial", items[0].ItemID, "created", nil))
	require.NoError(t, repo.CompleteFunctionJobItem(ctx, "crypto", "job-partial", items[1].ItemID, "", errors.New("SCF rejected request")))

	aggregate, err := repo.GetFunctionJob(ctx, "crypto", "job-partial")
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, FunctionJobPartial, aggregate.Job.Status)
	assert.Equal(t, 1, aggregate.SuccessCount)
	assert.Equal(t, 1, aggregate.FailedCount)
	assert.NotNil(t, aggregate.Job.CompletedAt)
	assert.Equal(t, "SCF rejected request", aggregate.Items[1].ErrorMessage)
}

func TestRequeueInterruptedFunctionJobItemsReturnsRunningItemsToPending(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createFunctionJobFixture(t, repo, "crypto", "job-requeue", 3)
	items, err := repo.TakePendingFunctionJobItems(ctx, 2)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteFunctionJobItem(ctx, "crypto", "job-requeue", items[0].ItemID, "done", nil))

	count, err := repo.RequeueInterruptedFunctionJobItems(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	aggregate, err := repo.GetFunctionJob(ctx, "crypto", "job-requeue")
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, FunctionJobPending, aggregate.Job.Status)
	assert.Equal(t, 2, aggregate.PendingCount)
	assert.Equal(t, 1, aggregate.SuccessCount)
	assert.Zero(t, aggregate.RunningCount)
	for _, item := range aggregate.Items {
		if item.Status == FunctionJobPending {
			assert.Nil(t, item.StartedAt)
		}
	}
}

func TestDefinitiveInstanceMutationFailureReleasesClaimForNextOperation(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, instance := createInstanceMutationBatchFixture(t, repo, "job-definitive")
	require.NoError(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, item.ItemID))
	taken, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)

	require.NoError(t, repo.CompleteFunctionJobItem(ctx, item.SpaceID, item.JobID, item.ItemID, "", errors.New("permission denied before provider mutation")))
	aggregate, err := repo.GetFunctionJob(ctx, item.SpaceID, item.JobID)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, FunctionJobFailed, aggregate.Items[0].Status)
	require.NoError(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, "next-operation"))
}

func TestInterruptedInstanceMutationResumesSameOperationAndKeepsClaim(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, instance := createInstanceMutationBatchFixture(t, repo, "job-crash-resume")
	require.NoError(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, item.ItemID))

	firstTake, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, firstTake, 1)
	assert.True(t, firstTake[0].ResumeClaim)
	count, err := repo.RequeueInterruptedFunctionJobItems(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	var queued FunctionJobItem
	require.NoError(t, repo.db.Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).First(&queued).Error)
	assert.Equal(t, FunctionJobReconciliationRequired, queued.Status)
	require.NoError(t, repo.db.Model(&FunctionJobItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).
		Update("c_mtime", time.Now().UTC().Add(-functionJobReconciliationRetryDelay-time.Second)).Error)

	resumed, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	assert.Equal(t, item.ItemID, resumed[0].ItemID)
	assert.True(t, resumed[0].ResumeClaim)
	require.NoError(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, item.ItemID))
	require.ErrorIs(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, "new-operation"), ErrInstanceMutationClaimed)
}

func TestAmbiguousInstanceMutationRemainsFencedUntilSameOperationReconciles(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, instance := createInstanceMutationBatchFixture(t, repo, "job-ambiguous")
	taken, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	require.NoError(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, item.ItemID))
	require.NoError(t, repo.RequireFunctionJobItemReconciliation(ctx, item.SpaceID, item.JobID, item.ItemID, "provider outcome is ambiguous"))
	require.ErrorIs(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, "new-operation"), ErrInstanceMutationClaimed)

	var stored FunctionJobItem
	require.NoError(t, repo.db.Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).First(&stored).Error)
	assert.Equal(t, FunctionJobReconciliationRequired, stored.Status)
	assert.Nil(t, stored.CompletedAt)

	require.NoError(t, repo.db.Model(&FunctionJobItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).
		Update("c_mtime", time.Now().UTC().Add(-functionJobReconciliationRetryDelay-time.Second)).Error)
	resumed, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	assert.True(t, resumed[0].ResumeClaim)
	require.NoError(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, item.ItemID))
	require.NoError(t, repo.UpdateInstanceDeployment(ctx, instance.SpaceID, instance.InstanceID, "pkg-next", "v2", nil, nil, false, instance.LifecycleID, item.ItemID))
	require.NoError(t, repo.CompleteFunctionJobItem(ctx, item.SpaceID, item.JobID, item.ItemID, "reconciled", nil))
	require.NoError(t, repo.ClaimInstanceMutation(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, "new-operation"))
}

func TestDeletedInstanceGenerationRemainsFencedUntilBatchItemCompletes(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, instance := createInstanceMutationBatchFixture(t, repo, "job-delete-commit-gap", "delete_instances")
	taken, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	require.NoError(t, repo.ClaimInstanceDelete(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, item.ItemID))
	require.NoError(t, repo.CompleteInstanceDelete(ctx, instance.SpaceID, instance.InstanceID, instance.LifecycleID, item.ItemID))

	tombstone, err := repo.GetInstanceIncludingDeleted(ctx, instance.SpaceID, instance.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, tombstone)
	assert.True(t, tombstone.IsDeleted)
	assert.Equal(t, item.ItemID, tombstone.LifecycleOperationID)
	require.ErrorIs(t, repo.UpsertInstance(ctx, FunctionInstance{SpaceID: instance.SpaceID, InstanceID: instance.InstanceID, PackageID: "new-package"}), ErrInstanceDeleteClaimed)

	count, err := repo.RequeueInterruptedFunctionJobItems(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	require.NoError(t, repo.db.Model(&FunctionJobItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).
		Update("c_mtime", time.Now().UTC().Add(-functionJobReconciliationRetryDelay-time.Second)).Error)
	resumed, err := repo.TakePendingFunctionJobItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	assert.True(t, resumed[0].ResumeClaim)
	require.NoError(t, repo.CompleteFunctionJobItem(ctx, item.SpaceID, item.JobID, item.ItemID, "deleted", nil))
	require.NoError(t, repo.UpsertInstance(ctx, FunctionInstance{SpaceID: instance.SpaceID, InstanceID: instance.InstanceID, PackageID: "new-package"}))
	current, err := repo.GetInstance(ctx, instance.SpaceID, instance.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.NotEqual(t, instance.LifecycleID, current.LifecycleID)
}

func createInstanceMutationBatchFixture(t *testing.T, repo *CatalogRepository, jobID string, operation ...string) (FunctionJobItem, FunctionInstance) {
	t.Helper()
	ctx := context.Background()
	batchOperation := "deploy_instances"
	if len(operation) > 0 {
		batchOperation = operation[0]
	}
	instance := FunctionInstance{SpaceID: "crypto", InstanceID: jobID + "-instance", PackageID: "pkg-old"}
	require.NoError(t, repo.UpsertInstance(ctx, instance))
	storedInstance, err := repo.GetInstance(ctx, instance.SpaceID, instance.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, storedInstance)
	itemID := jobID + "-item-00"
	request := fmt.Sprintf(`{"lifecycleId":%q,"operationId":%q}`, storedInstance.LifecycleID, itemID)
	require.NoError(t, repo.CreateFunctionJob(ctx, FunctionJobCreate{
		SpaceID:   storedInstance.SpaceID,
		JobID:     jobID,
		Operation: batchOperation,
		Items:     []FunctionJobItemCreate{{ItemID: itemID, ItemIndex: 0, InstanceID: storedInstance.InstanceID, RequestJSON: request}},
	}))
	return FunctionJobItem{SpaceID: storedInstance.SpaceID, JobID: jobID, ItemID: itemID, InstanceID: storedInstance.InstanceID}, *storedInstance
}

func TestGetFunctionJobIsSpaceScoped(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createFunctionJobFixture(t, repo, "crypto", "same-job-id", 1)
	createFunctionJobFixture(t, repo, "stocks", "same-job-id", 2)

	crypto, err := repo.GetFunctionJob(ctx, "crypto", "same-job-id")
	require.NoError(t, err)
	require.NotNil(t, crypto)
	assert.Equal(t, 1, crypto.Job.TotalCount)
	assert.Len(t, crypto.Items, 1)

	stocks, err := repo.GetFunctionJob(ctx, "stocks", "same-job-id")
	require.NoError(t, err)
	require.NotNil(t, stocks)
	assert.Equal(t, 2, stocks.Job.TotalCount)
	assert.Len(t, stocks.Items, 2)

	missing, err := repo.GetFunctionJob(ctx, "forex", "same-job-id")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func createFunctionJobFixture(t *testing.T, repo *CatalogRepository, spaceID, jobID string, count int) {
	t.Helper()
	items := make([]FunctionJobItemCreate, 0, count)
	for i := range count {
		items = append(items, FunctionJobItemCreate{
			ItemID:      fmt.Sprintf("%s-item-%02d", jobID, i),
			ItemIndex:   i,
			InstanceID:  fmt.Sprintf("instance-%02d", i),
			RequestJSON: fmt.Sprintf(`{"index":%d}`, i),
		})
	}
	require.NoError(t, repo.CreateFunctionJob(context.Background(), FunctionJobCreate{
		SpaceID:   spaceID,
		JobID:     jobID,
		Operation: "create_instances",
		Items:     items,
	}))
}
