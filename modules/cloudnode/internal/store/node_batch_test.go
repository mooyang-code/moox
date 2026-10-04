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

func TestReplaceNodeBatchItemPublishFenceCASChangesOnlyFenceEnvelope(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	request := `{"nodeId":"node-a","packageId":"pkg-new","lifecycleId":"generation-1","operationId":"item-1","environment":{"TOKEN":"secret"},"config":{"timeout":"60"},"collectorPublishLeaseId":"cli-lease","collectorPublishFencingToken":"7"}`
	require.NoError(t, repo.CreateNodeBatch(ctx, NodeBatchCreate{
		SpaceID: "crypto", JobID: "fence-cas", Operation: "deploy_nodes",
		Items: []NodeBatchItemCreate{{ItemID: "item-1", ItemIndex: 0, NodeID: "node-a", RequestJSON: request}},
	}))
	items, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)

	updated, err := repo.ReplaceNodeBatchItemPublishFence(ctx, "crypto", "fence-cas", "item-1", "cli-lease", 7, "recovery-lease", 8)
	require.NoError(t, err)
	require.True(t, updated.PublishLeaseRecoveryOwned)
	var before, after map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(request), &before))
	require.NoError(t, json.Unmarshal([]byte(updated.RequestJSON), &after))
	assert.Equal(t, before["nodeId"], after["nodeId"])
	assert.Equal(t, before["packageId"], after["packageId"])
	assert.Equal(t, before["lifecycleId"], after["lifecycleId"])
	assert.Equal(t, before["operationId"], after["operationId"])
	assert.Equal(t, before["environment"], after["environment"])
	assert.Equal(t, before["config"], after["config"])
	assert.JSONEq(t, `"recovery-lease"`, string(after["collectorPublishLeaseId"]))
	assert.JSONEq(t, `"8"`, string(after["collectorPublishFencingToken"]))

	_, err = repo.ReplaceNodeBatchItemPublishFence(ctx, "crypto", "fence-cas", "item-1", "cli-lease", 7, "another", 9)
	require.ErrorContains(t, err, "fence changed")
}

func TestRecoveredPublishLeaseReleaseMarkerSurvivesUntilExactTerminalRelease(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	request := `{"nodeId":"node-a","lifecycleId":"generation-1","operationId":"item-1","collectorPublishLeaseId":"recovery-lease","collectorPublishFencingToken":"8"}`
	require.NoError(t, repo.CreateNodeBatch(ctx, NodeBatchCreate{
		SpaceID: "crypto", JobID: "lease-release", Operation: "deploy_nodes",
		Items: []NodeBatchItemCreate{{ItemID: "item-1", ItemIndex: 0, NodeID: "node-a", RequestJSON: request}},
	}))
	items, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, err = repo.ReplaceNodeBatchItemPublishFence(ctx, "crypto", "lease-release", "item-1", "recovery-lease", 8, "recovery-lease-2", 9)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteNodeBatchItem(ctx, "crypto", "lease-release", "item-1", "done", nil))

	pending, err := repo.ListTerminalNodeBatchItemsForPublishLeaseRelease(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.True(t, pending[0].PublishLeaseRecoveryOwned)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(pending[0].RequestJSON), &envelope))
	var tokenText string
	require.NoError(t, json.Unmarshal(envelope["collectorPublishFencingToken"], &tokenText))
	token, err := strconv.ParseInt(tokenText, 10, 64)
	require.NoError(t, err)
	require.NoError(t, repo.MarkNodeBatchItemPublishLeaseReleased(ctx, "crypto", "lease-release", "item-1", "recovery-lease-2", token))

	pending, err = repo.ListTerminalNodeBatchItemsForPublishLeaseRelease(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestCreateNodeBatchIsAtomic(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	err := repo.CreateNodeBatch(ctx, NodeBatchCreate{
		SpaceID:   "crypto",
		JobID:     "job-atomic",
		Operation: "create_nodes",
		Items: []NodeBatchItemCreate{
			{ItemID: "item-0", ItemIndex: 0, RequestJSON: `{"index":0}`},
			{ItemID: "item-1", ItemIndex: 0, RequestJSON: `{"index":1}`},
		},
	})
	require.Error(t, err)

	var jobCount, itemCount int64
	require.NoError(t, repo.db.Model(&NodeBatch{}).Where("c_job_id = ?", "job-atomic").Count(&jobCount).Error)
	require.NoError(t, repo.db.Model(&NodeBatchItem{}).Where("c_job_id = ?", "job-atomic").Count(&itemCount).Error)
	assert.Zero(t, jobCount)
	assert.Zero(t, itemCount)
}

func TestCreateNodeBatchRejectsStableNodeWhilePreviousCreateIsPending(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.CreateNodeBatch(ctx, NodeBatchCreate{
		SpaceID: "crypto", JobID: "job-first", Operation: "create_nodes",
		Items: []NodeBatchItemCreate{{ItemID: "item-first", ItemIndex: 0, NodeID: "stable-node", RequestJSON: `{}`}},
	}))
	err := repo.CreateNodeBatch(ctx, NodeBatchCreate{
		SpaceID: "crypto", JobID: "job-second", Operation: "create_nodes",
		Items: []NodeBatchItemCreate{{ItemID: "item-second", ItemIndex: 0, NodeID: "stable-node", RequestJSON: `{}`}},
	})
	require.ErrorContains(t, err, "already has a pending create batch")
}

func TestTakePendingNodeBatchItemsMarksWholeBatchRunning(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createNodeBatchFixture(t, repo, "crypto", "job-running", 3)

	items, err := repo.TakePendingNodeBatchItems(ctx, 3)
	require.NoError(t, err)
	require.Len(t, items, 3)
	for _, item := range items {
		assert.Equal(t, NodeBatchRunning, item.Status)
		assert.NotNil(t, item.StartedAt)
	}

	aggregate, err := repo.GetNodeBatch(ctx, "crypto", "job-running")
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, NodeBatchRunning, aggregate.Job.Status)
	assert.Equal(t, 3, aggregate.RunningCount)
}

func TestTakePendingNodeBatchItemsReturnsStableNonOverlappingBatches(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createNodeBatchFixture(t, repo, "crypto", "job-batches", 10)

	var gotSizes []int
	var gotIDs []string
	for range 4 {
		items, err := repo.TakePendingNodeBatchItems(ctx, 3)
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

	empty, err := repo.TakePendingNodeBatchItems(ctx, 3)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestTakePendingNodeBatchItemsRejectsNonPositiveLimit(t *testing.T) {
	repo := newTestCatalog(t)
	_, err := repo.TakePendingNodeBatchItems(context.Background(), 0)
	require.Error(t, err)
}

func TestCompleteNodeBatchItemBuildsSuccessStatus(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createNodeBatchFixture(t, repo, "crypto", "job-success", 2)
	items, err := repo.TakePendingNodeBatchItems(ctx, 2)
	require.NoError(t, err)

	require.NoError(t, repo.CompleteNodeBatchItem(ctx, "crypto", "job-success", items[0].ItemID, "created node 0", nil))
	require.NoError(t, repo.CompleteNodeBatchItem(ctx, "crypto", "job-success", items[1].ItemID, "created node 1", nil))

	aggregate, err := repo.GetNodeBatch(ctx, "crypto", "job-success")
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, NodeBatchSuccess, aggregate.Job.Status)
	assert.Equal(t, 2, aggregate.SuccessCount)
	assert.NotNil(t, aggregate.Job.CompletedAt)
	assert.Equal(t, "created node 0", aggregate.Items[0].ResultSummary)
}

func TestCompleteNodeBatchItemBuildsPartialStatus(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createNodeBatchFixture(t, repo, "crypto", "job-partial", 2)
	items, err := repo.TakePendingNodeBatchItems(ctx, 2)
	require.NoError(t, err)

	require.NoError(t, repo.CompleteNodeBatchItem(ctx, "crypto", "job-partial", items[0].ItemID, "created", nil))
	require.NoError(t, repo.CompleteNodeBatchItem(ctx, "crypto", "job-partial", items[1].ItemID, "", errors.New("SCF rejected request")))

	aggregate, err := repo.GetNodeBatch(ctx, "crypto", "job-partial")
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, NodeBatchPartial, aggregate.Job.Status)
	assert.Equal(t, 1, aggregate.SuccessCount)
	assert.Equal(t, 1, aggregate.FailedCount)
	assert.NotNil(t, aggregate.Job.CompletedAt)
	assert.Equal(t, "SCF rejected request", aggregate.Items[1].ErrorMessage)
}

func TestRequeueInterruptedNodeBatchItemsReturnsRunningItemsToPending(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createNodeBatchFixture(t, repo, "crypto", "job-requeue", 3)
	items, err := repo.TakePendingNodeBatchItems(ctx, 2)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteNodeBatchItem(ctx, "crypto", "job-requeue", items[0].ItemID, "done", nil))

	count, err := repo.RequeueInterruptedNodeBatchItems(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	aggregate, err := repo.GetNodeBatch(ctx, "crypto", "job-requeue")
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, NodeBatchPending, aggregate.Job.Status)
	assert.Equal(t, 2, aggregate.PendingCount)
	assert.Equal(t, 1, aggregate.SuccessCount)
	assert.Zero(t, aggregate.RunningCount)
	for _, item := range aggregate.Items {
		if item.Status == NodeBatchPending {
			assert.Nil(t, item.StartedAt)
		}
	}
}

func TestDefinitiveNodeMutationFailureReleasesClaimForNextOperation(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, node := createNodeMutationBatchFixture(t, repo, "job-definitive")
	require.NoError(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, item.ItemID))
	taken, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)

	require.NoError(t, repo.CompleteNodeBatchItem(ctx, item.SpaceID, item.JobID, item.ItemID, "", errors.New("permission denied before provider mutation")))
	aggregate, err := repo.GetNodeBatch(ctx, item.SpaceID, item.JobID)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, NodeBatchFailed, aggregate.Items[0].Status)
	require.NoError(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, "next-operation"))
}

func TestInterruptedNodeMutationResumesSameOperationAndKeepsClaim(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, node := createNodeMutationBatchFixture(t, repo, "job-crash-resume")
	require.NoError(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, item.ItemID))

	firstTake, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, firstTake, 1)
	assert.True(t, firstTake[0].ResumeClaim)
	count, err := repo.RequeueInterruptedNodeBatchItems(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	var queued NodeBatchItem
	require.NoError(t, repo.db.Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).First(&queued).Error)
	assert.Equal(t, NodeBatchReconciliationRequired, queued.Status)
	require.NoError(t, repo.db.Model(&NodeBatchItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).
		Update("c_mtime", time.Now().UTC().Add(-nodeBatchReconciliationRetryDelay-time.Second)).Error)

	resumed, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	assert.Equal(t, item.ItemID, resumed[0].ItemID)
	assert.True(t, resumed[0].ResumeClaim)
	require.NoError(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, item.ItemID))
	require.ErrorIs(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, "new-operation"), ErrNodeMutationClaimed)
}

func TestAmbiguousNodeMutationRemainsFencedUntilSameOperationReconciles(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, node := createNodeMutationBatchFixture(t, repo, "job-ambiguous")
	taken, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	require.NoError(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, item.ItemID))
	require.NoError(t, repo.RequireNodeBatchItemReconciliation(ctx, item.SpaceID, item.JobID, item.ItemID, "provider outcome is ambiguous"))
	require.ErrorIs(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, "new-operation"), ErrNodeMutationClaimed)

	var stored NodeBatchItem
	require.NoError(t, repo.db.Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).First(&stored).Error)
	assert.Equal(t, NodeBatchReconciliationRequired, stored.Status)
	assert.Nil(t, stored.CompletedAt)
	repaired, err := repo.ReleaseTerminalNodeBatchClaims(ctx)
	require.NoError(t, err)
	assert.Zero(t, repaired, "reconciliation-required claims are never startup-released")

	require.NoError(t, repo.db.Model(&NodeBatchItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).
		Update("c_mtime", time.Now().UTC().Add(-nodeBatchReconciliationRetryDelay-time.Second)).Error)
	resumed, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	assert.True(t, resumed[0].ResumeClaim)
	require.NoError(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, item.ItemID))
	require.NoError(t, repo.UpdateNodeDeployment(ctx, node.SpaceID, node.NodeID, "pkg-next", "v2", nil, nil, false, node.LifecycleID, item.ItemID))
	require.NoError(t, repo.CompleteNodeBatchItem(ctx, item.SpaceID, item.JobID, item.ItemID, "reconciled", nil))
	require.NoError(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, "new-operation"))
}

func TestStartupRecoversLegacyFailedItemThatStillOwnsMutationClaim(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, node := createNodeMutationBatchFixture(t, repo, "job-legacy-ambiguous")
	require.NoError(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, item.ItemID))
	require.NoError(t, repo.db.Model(&NodeBatchItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).
		Updates(map[string]any{"c_status": NodeBatchFailed, "c_error_message": "old runner lost provider outcome", "c_completed_at": time.Now().UTC()}).Error)
	require.NoError(t, repo.db.Model(&NodeBatch{}).
		Where("c_space_id = ? AND c_job_id = ?", item.SpaceID, item.JobID).
		Update("c_status", NodeBatchFailed).Error)

	recovered, err := repo.RecoverFailedNodeBatchMutationClaims(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, recovered)
	aggregate, err := repo.GetNodeBatch(ctx, item.SpaceID, item.JobID)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, NodeBatchRunning, aggregate.Job.Status)
	assert.Equal(t, NodeBatchReconciliationRequired, aggregate.Items[0].Status)
	assert.Nil(t, aggregate.Items[0].CompletedAt)
	require.ErrorIs(t, repo.ClaimNodeMutation(ctx, node.SpaceID, node.NodeID, node.LifecycleID, "new-operation"), ErrNodeMutationClaimed)

	require.NoError(t, repo.db.Model(&NodeBatchItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).
		Update("c_mtime", time.Now().UTC().Add(-nodeBatchReconciliationRetryDelay-time.Second)).Error)
	resumed, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	assert.Equal(t, item.ItemID, resumed[0].ItemID)
	assert.True(t, resumed[0].ResumeClaim)
}

func TestDeletedNodeGenerationRemainsFencedUntilBatchItemCompletes(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	item, node := createNodeMutationBatchFixture(t, repo, "job-delete-commit-gap", "delete_nodes")
	taken, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	require.NoError(t, repo.ClaimNodeDelete(ctx, node.SpaceID, node.NodeID, node.LifecycleID, item.ItemID))
	require.NoError(t, repo.CompleteNodeDelete(ctx, node.SpaceID, node.NodeID, node.LifecycleID, item.ItemID))

	tombstone, err := repo.GetNodeIncludingDeleted(ctx, node.SpaceID, node.NodeID)
	require.NoError(t, err)
	require.NotNil(t, tombstone)
	assert.True(t, tombstone.IsDeleted)
	assert.Equal(t, item.ItemID, tombstone.LifecycleOperationID)
	require.ErrorIs(t, repo.UpsertNode(ctx, CloudNode{SpaceID: node.SpaceID, NodeID: node.NodeID, PackageID: "new-package"}), ErrNodeDeleteClaimed)

	count, err := repo.RequeueInterruptedNodeBatchItems(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	require.NoError(t, repo.db.Model(&NodeBatchItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", item.SpaceID, item.JobID, item.ItemID).
		Update("c_mtime", time.Now().UTC().Add(-nodeBatchReconciliationRetryDelay-time.Second)).Error)
	resumed, err := repo.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	assert.True(t, resumed[0].ResumeClaim)
	require.NoError(t, repo.CompleteNodeBatchItem(ctx, item.SpaceID, item.JobID, item.ItemID, "deleted", nil))
	require.NoError(t, repo.UpsertNode(ctx, CloudNode{SpaceID: node.SpaceID, NodeID: node.NodeID, PackageID: "new-package"}))
	current, err := repo.GetNode(ctx, node.SpaceID, node.NodeID)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.NotEqual(t, node.LifecycleID, current.LifecycleID)
}

func createNodeMutationBatchFixture(t *testing.T, repo *CatalogRepository, jobID string, operation ...string) (NodeBatchItem, CloudNode) {
	t.Helper()
	ctx := context.Background()
	batchOperation := "deploy_nodes"
	if len(operation) > 0 {
		batchOperation = operation[0]
	}
	node := CloudNode{SpaceID: "crypto", NodeID: jobID + "-node", PackageID: "pkg-old"}
	require.NoError(t, repo.UpsertNode(ctx, node))
	storedNode, err := repo.GetNode(ctx, node.SpaceID, node.NodeID)
	require.NoError(t, err)
	require.NotNil(t, storedNode)
	itemID := jobID + "-item-00"
	request := fmt.Sprintf(`{"lifecycleId":%q,"operationId":%q}`, storedNode.LifecycleID, itemID)
	require.NoError(t, repo.CreateNodeBatch(ctx, NodeBatchCreate{
		SpaceID:   storedNode.SpaceID,
		JobID:     jobID,
		Operation: batchOperation,
		Items:     []NodeBatchItemCreate{{ItemID: itemID, ItemIndex: 0, NodeID: storedNode.NodeID, RequestJSON: request}},
	}))
	return NodeBatchItem{SpaceID: storedNode.SpaceID, JobID: jobID, ItemID: itemID, NodeID: storedNode.NodeID}, *storedNode
}

func TestGetNodeBatchIsSpaceScoped(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	createNodeBatchFixture(t, repo, "crypto", "same-job-id", 1)
	createNodeBatchFixture(t, repo, "stocks", "same-job-id", 2)

	crypto, err := repo.GetNodeBatch(ctx, "crypto", "same-job-id")
	require.NoError(t, err)
	require.NotNil(t, crypto)
	assert.Equal(t, 1, crypto.Job.TotalCount)
	assert.Len(t, crypto.Items, 1)

	stocks, err := repo.GetNodeBatch(ctx, "stocks", "same-job-id")
	require.NoError(t, err)
	require.NotNil(t, stocks)
	assert.Equal(t, 2, stocks.Job.TotalCount)
	assert.Len(t, stocks.Items, 2)

	missing, err := repo.GetNodeBatch(ctx, "forex", "same-job-id")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func createNodeBatchFixture(t *testing.T, repo *CatalogRepository, spaceID, jobID string, count int) {
	t.Helper()
	items := make([]NodeBatchItemCreate, 0, count)
	for i := range count {
		items = append(items, NodeBatchItemCreate{
			ItemID:      fmt.Sprintf("%s-item-%02d", jobID, i),
			ItemIndex:   i,
			NodeID:      fmt.Sprintf("node-%02d", i),
			RequestJSON: fmt.Sprintf(`{"index":%d}`, i),
		})
	}
	require.NoError(t, repo.CreateNodeBatch(context.Background(), NodeBatchCreate{
		SpaceID:   spaceID,
		JobID:     jobID,
		Operation: "create_nodes",
		Items:     items,
	}))
}
