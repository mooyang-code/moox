package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	NodeBatchPending                  = "pending"
	NodeBatchRunning                  = "running"
	NodeBatchReconciliationRequired   = "reconciliation_required"
	NodeBatchSuccess                  = "success"
	NodeBatchFailed                   = "failed"
	NodeBatchPartial                  = "partial"
	nodeBatchReconciliationRetryDelay = 10 * time.Second
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
		// A config-driven publish can be retried while the first async create
		// job is still pending. Reserve stable node IDs in the batch table so
		// two jobs cannot race the same SCF function name.
		for _, item := range input.Items {
			var conflict int64
			if err := tx.Table("t_cloud_node_batch_items AS i").
				Joins("JOIN t_cloud_node_batches AS b ON b.c_space_id = i.c_space_id AND b.c_job_id = i.c_job_id").
				Where("i.c_space_id = ? AND i.c_node_id = ? AND i.c_status IN ? AND b.c_operation = ?", input.SpaceID, item.NodeID, []string{NodeBatchPending, NodeBatchRunning, NodeBatchReconciliationRequired}, "create_nodes").
				Count(&conflict).Error; err != nil {
				return err
			}
			if conflict > 0 {
				return fmt.Errorf("node %s already has a pending create batch", item.NodeID)
			}
		}
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
			eligibleReconciliationAt := time.Now().UTC().Add(-nodeBatchReconciliationRetryDelay)
			if err := tx.Where("c_status = ? OR (c_status = ? AND c_mtime <= ?)", NodeBatchPending, NodeBatchReconciliationRequired, eligibleReconciliationAt).
				Order("c_id ASC").
				Limit(limit).
				Find(&claimed).Error; err != nil {
				return err
			}
			if len(claimed) == 0 {
				return nil
			}

			ids := make([]int, 0, len(claimed))
			for index := range claimed {
				item := &claimed[index]
				var err error
				item.ResumeClaim, err = nodeBatchItemOwnsLifecycleClaim(tx, *item)
				if err != nil {
					return err
				}
				ids = append(ids, item.ID)
			}
			now := time.Now().UTC()
			result := tx.Model(&NodeBatchItem{}).
				Where("c_id IN ? AND c_status IN ?", ids, []string{NodeBatchPending, NodeBatchReconciliationRequired}).
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

func nodeBatchItemOwnsLifecycleClaim(tx *gorm.DB, item NodeBatchItem) (bool, error) {
	var request struct {
		LifecycleID string `json:"lifecycleId"`
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal([]byte(item.RequestJSON), &request); err != nil || request.LifecycleID == "" || request.OperationID == "" || request.OperationID != item.ItemID {
		return false, nil
	}
	var count int64
	err := tx.Model(&CloudNode{}).
		Where("c_space_id = ? AND c_node_id = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", item.SpaceID, item.NodeID, request.LifecycleID, request.OperationID).
		Count(&count).Error
	return count == 1, err
}

// RequireNodeBatchItemReconciliation keeps an ambiguous provider mutation
// attached to its durable operation. The same item can resume after the delay;
// other operation IDs remain blocked by the lifecycle claim.
func (r *CatalogRepository) RequireNodeBatchItemReconciliation(
	ctx context.Context,
	spaceID, jobID, itemID, errorMessage string,
) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&NodeBatchItem{}).
			Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ? AND c_status = ?", spaceID, jobID, itemID, NodeBatchRunning).
			Updates(map[string]any{
				"c_status":        NodeBatchReconciliationRequired,
				"c_error_message": errorMessage,
				"c_completed_at":  nil,
				"c_started_at":    nil,
				"c_mtime":         now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("running node batch item not found: %s", itemID)
		}
		counts, err := countNodeBatchItems(tx, spaceID, jobID)
		if err != nil {
			return err
		}
		jobStatus, completedAt := aggregateNodeBatchStatus(counts, now)
		return tx.Model(&NodeBatch{}).
			Where("c_space_id = ? AND c_job_id = ?", spaceID, jobID).
			Updates(map[string]any{"c_status": jobStatus, "c_completed_at": completedAt, "c_mtime": now}).Error
	})
}

func (r *CatalogRepository) CompleteNodeBatchItem(
	ctx context.Context,
	spaceID, jobID, itemID, resultSummary string,
	executeErr error,
) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item NodeBatchItem
		if err := tx.Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", spaceID, jobID, itemID).First(&item).Error; err != nil {
			return err
		}
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
		if err := releaseTerminalNodeBatchMutationClaim(tx, item, now); err != nil {
			return err
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

// ReleaseTerminalNodeBatchClaims repairs successful claims left by older
// versions. Failed legacy items are ambiguous, so they must remain fenced.
func (r *CatalogRepository) ReleaseTerminalNodeBatchClaims(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Exec(`
		UPDATE t_cloud_nodes AS n
		SET c_lifecycle_operation_id = '', c_mtime = ?
		WHERE n.c_lifecycle_operation_id <> ''
		AND EXISTS (
			SELECT 1 FROM t_cloud_node_batch_items AS i
			WHERE i.c_space_id = n.c_space_id
			  AND i.c_node_id = n.c_node_id
			  AND i.c_item_id = n.c_lifecycle_operation_id
			  AND i.c_status = ?
			  AND json_valid(i.c_request_json)
			  AND json_extract(i.c_request_json, '$.lifecycleId') = n.c_lifecycle_id
			  AND json_extract(i.c_request_json, '$.operationId') = n.c_lifecycle_operation_id
		)`, now, NodeBatchSuccess)
	return result.RowsAffected, result.Error
}

// RecoverFailedNodeBatchMutationClaims promotes legacy failed items that still
// own the exact node generation claim back into reconciliation. Older runners
// could persist FAILED after an ambiguous provider outcome without releasing
// the node claim; keeping that claim and resuming the same item is the only
// safe automatic recovery.
func (r *CatalogRepository) RecoverFailedNodeBatchMutationClaims(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	var recovered int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE t_cloud_node_batch_items AS i
			SET c_status = ?, c_completed_at = NULL, c_started_at = NULL, c_mtime = ?
			WHERE i.c_status = ?
			AND EXISTS (
				SELECT 1 FROM t_cloud_nodes AS n
				WHERE n.c_space_id = i.c_space_id
				  AND n.c_node_id = i.c_node_id
				  AND n.c_lifecycle_operation_id = i.c_item_id
				  AND json_valid(i.c_request_json)
				  AND json_extract(i.c_request_json, '$.lifecycleId') = n.c_lifecycle_id
				  AND json_extract(i.c_request_json, '$.operationId') = n.c_lifecycle_operation_id
			)`, NodeBatchReconciliationRequired, now, NodeBatchFailed)
		if result.Error != nil {
			return result.Error
		}
		recovered = result.RowsAffected
		if recovered == 0 {
			return nil
		}
		return tx.Exec(`
			UPDATE t_cloud_node_batches AS b
			SET c_status = ?, c_completed_at = NULL, c_mtime = ?
			WHERE EXISTS (
				SELECT 1 FROM t_cloud_node_batch_items AS i
				WHERE i.c_space_id = b.c_space_id
				  AND i.c_job_id = b.c_job_id
				  AND i.c_status = ?
			)
		`, NodeBatchRunning, now, NodeBatchReconciliationRequired).Error
	})
	return recovered, err
}

func releaseTerminalNodeBatchMutationClaim(tx *gorm.DB, item NodeBatchItem, now time.Time) error {
	var request struct {
		LifecycleID string `json:"lifecycleId"`
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal([]byte(item.RequestJSON), &request); err != nil {
		return nil // malformed legacy requests never own a lifecycle claim
	}
	if request.LifecycleID == "" || request.OperationID == "" || request.OperationID != item.ItemID {
		return nil
	}
	return tx.Model(&CloudNode{}).
		Where("c_space_id = ? AND c_node_id = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", item.SpaceID, item.NodeID, request.LifecycleID, request.OperationID).
		Updates(map[string]any{"c_lifecycle_operation_id": "", "c_mtime": now}).Error
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

		now := time.Now().UTC()
		seenJobs := make(map[string]NodeBatchItem, len(interrupted))
		for _, item := range interrupted {
			status := NodeBatchPending
			hasClaim, err := nodeBatchItemOwnsLifecycleClaim(tx, item)
			if err != nil {
				return err
			}
			if hasClaim {
				status = NodeBatchReconciliationRequired
			}
			result := tx.Model(&NodeBatchItem{}).
				Where("c_id = ? AND c_status = ?", item.ID, NodeBatchRunning).
				Updates(map[string]any{"c_status": status, "c_started_at": nil, "c_mtime": now})
			if result.Error != nil {
				return result.Error
			}
			requeued += result.RowsAffected
			seenJobs[item.SpaceID+"\x00"+item.JobID] = item
		}

		for _, item := range seenJobs {
			counts, err := countNodeBatchItems(tx, item.SpaceID, item.JobID)
			if err != nil {
				return err
			}
			status := NodeBatchPending
			if counts.Running > 0 {
				status = NodeBatchRunning
			}
			if err := tx.Model(&NodeBatch{}).
				Where("c_space_id = ? AND c_job_id = ? AND c_status NOT IN ?", item.SpaceID, item.JobID, []string{NodeBatchSuccess, NodeBatchFailed, NodeBatchPartial}).
				Updates(map[string]any{"c_status": status, "c_completed_at": nil, "c_mtime": now}).Error; err != nil {
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
		case NodeBatchReconciliationRequired:
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
    SUM(CASE WHEN c_status IN (?, ?) THEN 1 ELSE 0 END) AS running,
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS success,
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS failed
FROM t_cloud_node_batch_items
WHERE c_space_id = ? AND c_job_id = ?

`, NodeBatchPending, NodeBatchRunning, NodeBatchReconciliationRequired, NodeBatchSuccess, NodeBatchFailed, spaceID, jobID).
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
