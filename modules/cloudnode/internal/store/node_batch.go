package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	NodeBatchPending = "pending"
	NodeBatchRunning = "running"
	NodeBatchSuccess = "success"
	NodeBatchFailed  = "failed"
	NodeBatchPartial = "partial"
)

var errNodeBatchClaimConflict = errors.New("node batch item claim conflict")

type NodeBatchItemCreate struct {
	ItemID      string
	ItemIndex   int
	NodeID      string
	RequestJSON string
}

type NodeBatchCreate struct {
	SpaceID   string
	JobID     string
	Operation string
	Items     []NodeBatchItemCreate
}

type NodeBatchAggregate struct {
	Job          NodeBatch
	Items        []NodeBatchItem
	PendingCount int
	RunningCount int
	SuccessCount int
	FailedCount  int
}

func (r *CatalogRepository) CreateNodeBatch(ctx context.Context, input NodeBatchCreate) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job := NodeBatch{
			SpaceID:    input.SpaceID,
			JobID:      input.JobID,
			Operation:  input.Operation,
			Status:     NodeBatchPending,
			TotalCount: len(input.Items),
			CreateTime: now,
			ModifyTime: now,
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}

		items := make([]NodeBatchItem, 0, len(input.Items))
		for _, item := range input.Items {
			items = append(items, NodeBatchItem{
				SpaceID:     input.SpaceID,
				JobID:       input.JobID,
				ItemID:      item.ItemID,
				ItemIndex:   item.ItemIndex,
				NodeID:      item.NodeID,
				Status:      NodeBatchPending,
				RequestJSON: item.RequestJSON,
				CreateTime:  now,
				ModifyTime:  now,
			})
		}
		if len(items) == 0 {
			return nil
		}
		return tx.Create(&items).Error
	})
}

func (r *CatalogRepository) TakePendingNodeBatchItems(ctx context.Context, limit int) ([]NodeBatchItem, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("node batch item limit must be positive")
	}

	for {
		var claimed []NodeBatchItem
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("c_status = ?", NodeBatchPending).
				Order("c_id ASC").
				Limit(limit).
				Find(&claimed).Error; err != nil {
				return err
			}
			if len(claimed) == 0 {
				return nil
			}

			ids := make([]int, 0, len(claimed))
			for _, item := range claimed {
				ids = append(ids, item.ID)
			}
			now := time.Now().UTC()
			result := tx.Model(&NodeBatchItem{}).
				Where("c_id IN ? AND c_status = ?", ids, NodeBatchPending).
				Updates(map[string]any{
					"c_status":     NodeBatchRunning,
					"c_started_at": now,
					"c_mtime":      now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(claimed)) {
				return errNodeBatchClaimConflict
			}

			seenJobs := make(map[string]struct{}, len(claimed))
			for i := range claimed {
				claimed[i].Status = NodeBatchRunning
				claimed[i].StartedAt = &now
				claimed[i].ModifyTime = now
				key := claimed[i].SpaceID + "\x00" + claimed[i].JobID
				if _, ok := seenJobs[key]; ok {
					continue
				}
				seenJobs[key] = struct{}{}
				if err := tx.Model(&NodeBatch{}).
					Where("c_space_id = ? AND c_job_id = ?", claimed[i].SpaceID, claimed[i].JobID).
					Updates(map[string]any{"c_status": NodeBatchRunning, "c_mtime": now}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, errNodeBatchClaimConflict) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		return claimed, err
	}
}

func (r *CatalogRepository) CompleteNodeBatchItem(
	ctx context.Context,
	spaceID, jobID, itemID, resultSummary string,
	executeErr error,
) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		status := NodeBatchSuccess
		errorMessage := ""
		if executeErr != nil {
			status = NodeBatchFailed
			errorMessage = executeErr.Error()
		}
		result := tx.Model(&NodeBatchItem{}).
			Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", spaceID, jobID, itemID).
			Updates(map[string]any{
				"c_status":         status,
				"c_result_summary": resultSummary,
				"c_error_message":  errorMessage,
				"c_completed_at":   now,
				"c_mtime":          now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("node batch item not found: %s", itemID)
		}

		counts, err := countNodeBatchItems(tx, spaceID, jobID)
		if err != nil {
			return err
		}
		jobStatus, completedAt := aggregateNodeBatchStatus(counts, now)
		return tx.Model(&NodeBatch{}).
			Where("c_space_id = ? AND c_job_id = ?", spaceID, jobID).
			Updates(map[string]any{
				"c_status":       jobStatus,
				"c_completed_at": completedAt,
				"c_mtime":        now,
			}).Error
	})
}

func (r *CatalogRepository) RequeueInterruptedNodeBatchItems(ctx context.Context) (int64, error) {
	var requeued int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var interrupted []NodeBatchItem
		if err := tx.Where("c_status = ?", NodeBatchRunning).
			Order("c_id ASC").
			Find(&interrupted).Error; err != nil {
			return err
		}
		if len(interrupted) == 0 {
			return nil
		}

		ids := make([]int, 0, len(interrupted))
		for _, item := range interrupted {
			ids = append(ids, item.ID)
		}
		now := time.Now().UTC()
		result := tx.Model(&NodeBatchItem{}).
			Where("c_id IN ? AND c_status = ?", ids, NodeBatchRunning).
			Updates(map[string]any{
				"c_status":     NodeBatchPending,
				"c_started_at": nil,
				"c_mtime":      now,
			})
		if result.Error != nil {
			return result.Error
		}
		requeued = result.RowsAffected

		seenJobs := make(map[string]struct{}, len(interrupted))
		for _, item := range interrupted {
			key := item.SpaceID + "\x00" + item.JobID
			if _, ok := seenJobs[key]; ok {
				continue
			}
			seenJobs[key] = struct{}{}
			if err := tx.Model(&NodeBatch{}).
				Where(
					"c_space_id = ? AND c_job_id = ? AND c_status NOT IN ?",
					item.SpaceID,
					item.JobID,
					[]string{NodeBatchSuccess, NodeBatchFailed, NodeBatchPartial},
				).
				Updates(map[string]any{
					"c_status":       NodeBatchPending,
					"c_completed_at": nil,
					"c_mtime":        now,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return requeued, err
}

func (r *CatalogRepository) GetNodeBatch(ctx context.Context, spaceID, jobID string) (*NodeBatchAggregate, error) {
	var job NodeBatch
	var items []NodeBatchItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Where("c_space_id = ? AND c_job_id = ?", spaceID, jobID).
			First(&job).Error; err != nil {
			return err
		}
		return tx.
			Where("c_space_id = ? AND c_job_id = ?", spaceID, jobID).
			Order("c_item_index ASC").
			Find(&items).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	aggregate := &NodeBatchAggregate{Job: job, Items: items}
	for _, item := range items {
		switch item.Status {
		case NodeBatchPending:
			aggregate.PendingCount++
		case NodeBatchRunning:
			aggregate.RunningCount++
		case NodeBatchSuccess:
			aggregate.SuccessCount++
		case NodeBatchFailed:
			aggregate.FailedCount++
		}
	}
	return aggregate, nil
}

type nodeBatchItemCounts struct {
	Pending int
	Running int
	Success int
	Failed  int
}

func countNodeBatchItems(tx *gorm.DB, spaceID, jobID string) (nodeBatchItemCounts, error) {
	var counts nodeBatchItemCounts
	err := tx.Raw(`
SELECT
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS pending,
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS running,
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS success,
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS failed
FROM t_cloud_node_batch_items
WHERE c_space_id = ? AND c_job_id = ?
`, NodeBatchPending, NodeBatchRunning, NodeBatchSuccess, NodeBatchFailed, spaceID, jobID).
		Scan(&counts).Error
	return counts, err
}

func aggregateNodeBatchStatus(counts nodeBatchItemCounts, completedAt time.Time) (string, *time.Time) {
	if counts.Pending > 0 || counts.Running > 0 {
		return NodeBatchRunning, nil
	}
	if counts.Success > 0 && counts.Failed == 0 {
		return NodeBatchSuccess, &completedAt
	}
	if counts.Failed > 0 && counts.Success == 0 {
		return NodeBatchFailed, &completedAt
	}
	return NodeBatchPartial, &completedAt
}
