package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
