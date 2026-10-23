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
	FunctionJobPending                  = "pending"
	FunctionJobRunning                  = "running"
	FunctionJobReconciliationRequired   = "reconciliation_required"
	FunctionJobSuccess                  = "success"
	FunctionJobFailed                   = "failed"
	FunctionJobPartial                  = "partial"
	functionJobReconciliationRetryDelay = 10 * time.Second
)

var errFunctionJobClaimConflict = errors.New("instance batch item claim conflict")

type FunctionJobItemCreate struct {
	ItemID      string
	ItemIndex   int
	InstanceID  string
	RequestJSON string
}

type FunctionJobCreate struct {
	SpaceID   string
	JobID     string
	Operation string
	Items     []FunctionJobItemCreate
}

type FunctionJobAggregate struct {
	Job          FunctionJob
	Items        []FunctionJobItem
	PendingCount int
	RunningCount int
	SuccessCount int
	FailedCount  int
}

func (r *CatalogRepository) CreateFunctionJob(ctx context.Context, input FunctionJobCreate) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A config-driven publish can be retried while the first async create
		// job is still pending. Reserve stable instance IDs in the batch table so
		// two jobs cannot race the same SCF function name.
		for _, item := range input.Items {
			var conflict int64
			if err := tx.Table("t_cloud_function_job_items AS i").
				Joins("JOIN t_cloud_function_jobs AS b ON b.c_space_id = i.c_space_id AND b.c_job_id = i.c_job_id").
				Where("i.c_space_id = ? AND i.c_instance_id = ? AND i.c_status IN ? AND b.c_operation = ?", input.SpaceID, item.InstanceID, []string{FunctionJobPending, FunctionJobRunning, FunctionJobReconciliationRequired}, "create_instances").
				Count(&conflict).Error; err != nil {
				return err
			}
			if conflict > 0 {
				return fmt.Errorf("instance %s already has a pending create batch", item.InstanceID)
			}
		}
		job := FunctionJob{
			SpaceID:    input.SpaceID,
			JobID:      input.JobID,
			Operation:  input.Operation,
			Status:     FunctionJobPending,
			TotalCount: len(input.Items),
			CreateTime: now,
			ModifyTime: now,
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}

		items := make([]FunctionJobItem, 0, len(input.Items))
		for _, item := range input.Items {
			items = append(items, FunctionJobItem{
				SpaceID:     input.SpaceID,
				JobID:       input.JobID,
				ItemID:      item.ItemID,
				ItemIndex:   item.ItemIndex,
				InstanceID:  item.InstanceID,
				Status:      FunctionJobPending,
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

func (r *CatalogRepository) TakePendingFunctionJobItems(ctx context.Context, limit int) ([]FunctionJobItem, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("instance batch item limit must be positive")
	}

	for {
		var claimed []FunctionJobItem
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			eligibleReconciliationAt := time.Now().UTC().Add(-functionJobReconciliationRetryDelay)
			if err := tx.Where("c_status = ? OR (c_status = ? AND c_mtime <= ?)", FunctionJobPending, FunctionJobReconciliationRequired, eligibleReconciliationAt).
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
				item.ResumeClaim, err = functionJobItemOwnsLifecycleClaim(tx, *item)
				if err != nil {
					return err
				}
				ids = append(ids, item.ID)
			}
			now := time.Now().UTC()
			result := tx.Model(&FunctionJobItem{}).
				Where("c_id IN ? AND c_status IN ?", ids, []string{FunctionJobPending, FunctionJobReconciliationRequired}).
				Updates(map[string]any{
					"c_status":     FunctionJobRunning,
					"c_started_at": now,
					"c_mtime":      now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(claimed)) {
				return errFunctionJobClaimConflict
			}

			seenJobs := make(map[string]struct{}, len(claimed))
			for i := range claimed {
				claimed[i].Status = FunctionJobRunning
				claimed[i].StartedAt = &now
				claimed[i].ModifyTime = now
				key := claimed[i].SpaceID + "\x00" + claimed[i].JobID
				if _, ok := seenJobs[key]; ok {
					continue
				}
				seenJobs[key] = struct{}{}
				if err := tx.Model(&FunctionJob{}).
					Where("c_space_id = ? AND c_job_id = ?", claimed[i].SpaceID, claimed[i].JobID).
					Updates(map[string]any{"c_status": FunctionJobRunning, "c_mtime": now}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, errFunctionJobClaimConflict) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		return claimed, err
	}
}

func functionJobItemOwnsLifecycleClaim(tx *gorm.DB, item FunctionJobItem) (bool, error) {
	var request struct {
		LifecycleID string `json:"lifecycleId"`
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal([]byte(item.RequestJSON), &request); err != nil || request.LifecycleID == "" || request.OperationID == "" || request.OperationID != item.ItemID {
		return false, nil
	}
	var count int64
	err := tx.Model(&FunctionInstance{}).
		Where("c_space_id = ? AND c_instance_id = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", item.SpaceID, item.InstanceID, request.LifecycleID, request.OperationID).
		Count(&count).Error
	return count == 1, err
}

// RequireFunctionJobItemReconciliation keeps an ambiguous provider mutation
// attached to its durable operation. The same item can resume after the delay;
// other operation IDs remain blocked by the lifecycle claim.
func (r *CatalogRepository) RequireFunctionJobItemReconciliation(
	ctx context.Context,
	spaceID, jobID, itemID, errorMessage string,
) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&FunctionJobItem{}).
			Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ? AND c_status = ?", spaceID, jobID, itemID, FunctionJobRunning).
			Updates(map[string]any{
				"c_status":        FunctionJobReconciliationRequired,
				"c_error_message": errorMessage,
				"c_completed_at":  nil,
				"c_started_at":    nil,
				"c_mtime":         now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("running instance batch item not found: %s", itemID)
		}
		counts, err := countFunctionJobItems(tx, spaceID, jobID)
		if err != nil {
			return err
		}
		jobStatus, completedAt := aggregateFunctionJobStatus(counts, now)
		return tx.Model(&FunctionJob{}).
			Where("c_space_id = ? AND c_job_id = ?", spaceID, jobID).
			Updates(map[string]any{"c_status": jobStatus, "c_completed_at": completedAt, "c_mtime": now}).Error
	})
}

func (r *CatalogRepository) CompleteFunctionJobItem(
	ctx context.Context,
	spaceID, jobID, itemID, resultSummary string,
	executeErr error,
) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item FunctionJobItem
		if err := tx.Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", spaceID, jobID, itemID).First(&item).Error; err != nil {
			return err
		}
		status := FunctionJobSuccess
		errorMessage := ""
		if executeErr != nil {
			status = FunctionJobFailed
			errorMessage = executeErr.Error()
		}
		result := tx.Model(&FunctionJobItem{}).
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
			return fmt.Errorf("instance batch item not found: %s", itemID)
		}
		if err := releaseTerminalFunctionJobMutationClaim(tx, item, now); err != nil {
			return err
		}

		counts, err := countFunctionJobItems(tx, spaceID, jobID)
		if err != nil {
			return err
		}
		jobStatus, completedAt := aggregateFunctionJobStatus(counts, now)
		return tx.Model(&FunctionJob{}).
			Where("c_space_id = ? AND c_job_id = ?", spaceID, jobID).
			Updates(map[string]any{
				"c_status":       jobStatus,
				"c_completed_at": completedAt,
				"c_mtime":        now,
			}).Error
	})
}

func releaseTerminalFunctionJobMutationClaim(tx *gorm.DB, item FunctionJobItem, now time.Time) error {
	var request struct {
		LifecycleID string `json:"lifecycleId"`
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal([]byte(item.RequestJSON), &request); err != nil {
		return nil // malformed requests never own a lifecycle claim
	}
	if request.LifecycleID == "" || request.OperationID == "" || request.OperationID != item.ItemID {
		return nil
	}
	return tx.Model(&FunctionInstance{}).
		Where("c_space_id = ? AND c_instance_id = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", item.SpaceID, item.InstanceID, request.LifecycleID, request.OperationID).
		Updates(map[string]any{"c_lifecycle_operation_id": "", "c_mtime": now}).Error
}

func (r *CatalogRepository) RequeueInterruptedFunctionJobItems(ctx context.Context) (int64, error) {
	var requeued int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var interrupted []FunctionJobItem
		if err := tx.Where("c_status = ?", FunctionJobRunning).
			Order("c_id ASC").
			Find(&interrupted).Error; err != nil {
			return err
		}
		if len(interrupted) == 0 {
			return nil
		}

		now := time.Now().UTC()
		seenJobs := make(map[string]FunctionJobItem, len(interrupted))
		for _, item := range interrupted {
			status := FunctionJobPending
			hasClaim, err := functionJobItemOwnsLifecycleClaim(tx, item)
			if err != nil {
				return err
			}
			if hasClaim {
				status = FunctionJobReconciliationRequired
			}
			result := tx.Model(&FunctionJobItem{}).
				Where("c_id = ? AND c_status = ?", item.ID, FunctionJobRunning).
				Updates(map[string]any{"c_status": status, "c_started_at": nil, "c_mtime": now})
			if result.Error != nil {
				return result.Error
			}
			requeued += result.RowsAffected
			seenJobs[item.SpaceID+"\x00"+item.JobID] = item
		}

		for _, item := range seenJobs {
			counts, err := countFunctionJobItems(tx, item.SpaceID, item.JobID)
			if err != nil {
				return err
			}
			status := FunctionJobPending
			if counts.Running > 0 {
				status = FunctionJobRunning
			}
			if err := tx.Model(&FunctionJob{}).
				Where("c_space_id = ? AND c_job_id = ? AND c_status NOT IN ?", item.SpaceID, item.JobID, []string{FunctionJobSuccess, FunctionJobFailed, FunctionJobPartial}).
				Updates(map[string]any{"c_status": status, "c_completed_at": nil, "c_mtime": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return requeued, err
}

func (r *CatalogRepository) GetFunctionJob(ctx context.Context, spaceID, jobID string) (*FunctionJobAggregate, error) {
	var job FunctionJob
	var items []FunctionJobItem
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
	aggregate := &FunctionJobAggregate{Job: job, Items: items}
	for _, item := range items {
		switch item.Status {
		case FunctionJobPending:
			aggregate.PendingCount++
		case FunctionJobRunning:
			aggregate.RunningCount++
		case FunctionJobReconciliationRequired:
			aggregate.RunningCount++
		case FunctionJobSuccess:
			aggregate.SuccessCount++
		case FunctionJobFailed:
			aggregate.FailedCount++
		}
	}
	return aggregate, nil
}

type functionJobItemCounts struct {
	Pending int
	Running int
	Success int
	Failed  int
}

func countFunctionJobItems(tx *gorm.DB, spaceID, jobID string) (functionJobItemCounts, error) {
	var counts functionJobItemCounts
	err := tx.Raw(`
SELECT
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS pending,
    SUM(CASE WHEN c_status IN (?, ?) THEN 1 ELSE 0 END) AS running,
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS success,
    SUM(CASE WHEN c_status = ? THEN 1 ELSE 0 END) AS failed
FROM t_cloud_function_job_items
WHERE c_space_id = ? AND c_job_id = ?

`, FunctionJobPending, FunctionJobRunning, FunctionJobReconciliationRequired, FunctionJobSuccess, FunctionJobFailed, spaceID, jobID).
		Scan(&counts).Error
	return counts, err
}

func aggregateFunctionJobStatus(counts functionJobItemCounts, completedAt time.Time) (string, *time.Time) {
	if counts.Pending > 0 || counts.Running > 0 {
		return FunctionJobRunning, nil
	}
	if counts.Success > 0 && counts.Failed == 0 {
		return FunctionJobSuccess, &completedAt
	}
	if counts.Failed > 0 && counts.Success == 0 {
		return FunctionJobFailed, &completedAt
	}
	return FunctionJobPartial, &completedAt
}
