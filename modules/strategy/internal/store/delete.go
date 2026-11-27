package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// deleteInstanceBatch 是删除实例明细子表时每批最多删除的行数；分批执行避免单连接长事务阻塞事件处理与接口。
var deleteInstanceBatch int64 = 5000

// 删除实例的 SQL：整条写成常量，不在调用处拼接。外键开着，先分批删大明细子表，再在短事务里删剩余小表与实例主行。
const (
	sqlDeleteInstanceReadyEventsBatch = `
		DELETE FROM t_strategy_ready_events
		WHERE rowid IN (SELECT rowid FROM t_strategy_ready_events WHERE c_instance_id = ? LIMIT ?)`
	sqlDeleteInstanceResultItemsBatch = `
		DELETE FROM t_strategy_result_items
		WHERE rowid IN (
			SELECT i.rowid FROM t_strategy_result_items i
			JOIN t_strategy_results r ON r.c_result_id = i.c_result_id
			WHERE r.c_instance_id = ?
			LIMIT ?
		)`
	sqlDeleteInstanceResultsBatch = `
		DELETE FROM t_strategy_results
		WHERE rowid IN (SELECT rowid FROM t_strategy_results WHERE c_instance_id = ? LIMIT ?)`
	sqlDeleteInstanceReplayBarsBatch = `
		DELETE FROM t_strategy_replay_bars
		WHERE rowid IN (
			SELECT b.rowid FROM t_strategy_replay_bars b
			JOIN t_strategy_replays r ON r.c_replay_id = b.c_replay_id
			WHERE r.c_instance_id = ?
			LIMIT ?
		)`
	sqlDeleteInstanceStates          = `DELETE FROM t_strategy_states WHERE c_instance_id = ?`
	sqlDeleteInstanceSessions        = `DELETE FROM t_strategy_sessions WHERE c_instance_id = ?`
	sqlDeleteInstanceReplays         = `DELETE FROM t_strategy_replays WHERE c_instance_id = ?`
	sqlDeleteInstanceDisplaySettings = `DELETE FROM t_strategy_display_settings WHERE c_instance_id = ?`
	sqlDeleteInstanceTasks           = `DELETE FROM t_strategy_instance_tasks WHERE c_instance_id = ?`
	// sqlDeleteStoppedInstance 只删已停用的实例（停用的实例上没有会话）。
	sqlDeleteStoppedInstance = `DELETE FROM t_strategy_instances WHERE c_instance_id = ? AND c_enabled = 0`
	// sqlSelectActiveReplaysOfInstance 列出还在排队或执行、会读到实例数据的回放：从实例发起的，以及本空间里按定义或 DSL 发起、
	// 但读取的因子任务（c_tasks_json 是 市场 → {factor_task_id, …}）在要删的任务（JSON 数组参数）里的。
	sqlSelectActiveReplaysOfInstance = `
		SELECT c_replay_id FROM t_strategy_replays
		WHERE c_status IN ('pending', 'running')
		AND (c_instance_id = ? OR (c_space_id = ? AND EXISTS (
			SELECT 1 FROM json_each(c_tasks_json) AS task
			WHERE json_extract(task.value, '$.factor_task_id') IN (SELECT value FROM json_each(?))
		)))
		ORDER BY c_replay_id`
	// sqlDeleteViewsAgePassedBatch 分批删除一组 View（JSON 数组参数）的年龄探针记录。
	sqlDeleteViewsAgePassedBatch = `
		DELETE FROM t_strategy_age_passed
		WHERE rowid IN (
			SELECT rowid FROM t_strategy_age_passed
			WHERE c_space_id = ? AND c_dataset_id IN (SELECT value FROM json_each(?))
			LIMIT ?
		)`
)

// instanceBatchedDeletes 是按实例 ID 分批删除的大明细子表，顺序满足外键（结果明细在结果之前，回放明细在回放之前）。
var instanceBatchedDeletes = []string{
	sqlDeleteInstanceReadyEventsBatch,
	sqlDeleteInstanceResultItemsBatch,
	sqlDeleteInstanceResultsBatch,
	sqlDeleteInstanceReplayBarsBatch,
}

// instanceFinalDeletes 是在明细清完后随实例主行在短事务里删除的小表，顺序满足外键（状态在会话之前）。
var instanceFinalDeletes = []string{
	sqlDeleteInstanceStates,
	sqlDeleteInstanceSessions,
	sqlDeleteInstanceReplays,
	sqlDeleteInstanceDisplaySettings,
	sqlDeleteInstanceTasks,
}

// ActiveReplaysOfInstance 返回还在排队或执行、会读到实例数据的回放 ID：从实例发起的，以及空间内读取 taskIDs 中任一任务的。
func (s *Store) ActiveReplaysOfInstance(ctx context.Context, instanceID, spaceID string, taskIDs []string) ([]string, error) {
	tasks, err := json.Marshal(append([]string{}, taskIDs...))
	if err != nil {
		return nil, err
	}
	var ids []string
	err = s.db.WithContext(ctx).Raw(sqlSelectActiveReplaysOfInstance, strings.TrimSpace(instanceID), spaceID, string(tasks)).Scan(&ids).Error
	return ids, err
}

// DeleteInstance 删除已停用的实例在策略库里的全部记录：先分批短事务清理大明细子表与按 View 存的年龄探针，
// 再在最终短事务里删掉剩余小表与实例主行，避免单写连接上的大事务长时间锁库。
// 实例不存在返回 ErrNotFound；实例已启用或在删除过程中被启用时拒绝删除。
func (s *Store) DeleteInstance(ctx context.Context, instanceID, spaceID string, datasetIDs []string) error {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return errors.New("实例 ID 不能为空")
	}
	views, err := json.Marshal(append([]string{}, datasetIDs...))
	if err != nil {
		return err
	}
	current, err := readInstance(s.db.WithContext(ctx), instanceID)
	if err != nil {
		return err
	}
	if current.Enabled == 1 {
		return errors.New("实例已启用，不能删除")
	}
	batch := deleteInstanceBatch
	if batch <= 0 {
		batch = 5000
	}
	for _, statement := range instanceBatchedDeletes {
		if err := s.deleteStoppedInstanceBatchLoop(ctx, instanceID, batch, func(tx *gorm.DB) (int64, error) {
			res := tx.Exec(statement, instanceID, batch)
			return res.RowsAffected, res.Error
		}); err != nil {
			return err
		}
	}
	if len(datasetIDs) > 0 {
		if err := s.deleteStoppedInstanceBatchLoop(ctx, instanceID, batch, func(tx *gorm.DB) (int64, error) {
			res := tx.Exec(sqlDeleteViewsAgePassedBatch, spaceID, string(views), batch)
			return res.RowsAffected, res.Error
		}); err != nil {
			return err
		}
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instanceID)
		if err != nil {
			return err
		}
		if current.Enabled == 1 {
			return errors.New("实例在删除过程中被启用，没有删除")
		}
		for _, statement := range instanceFinalDeletes {
			if err := tx.Exec(statement, instanceID).Error; err != nil {
				return err
			}
		}
		result := tx.Exec(sqlDeleteStoppedInstance, instanceID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("实例在删除过程中被启用，没有删除")
		}
		return nil
	})
}

// deleteStoppedInstanceBatchLoop 每批在短事务内确认实例仍处于停用状态后删除至多 batch 行，
// 直到本表不再有待删行；批间释放写连接，避免阻塞其他读写。
func (s *Store) deleteStoppedInstanceBatchLoop(ctx context.Context, instanceID string, batch int64, exec func(tx *gorm.DB) (int64, error)) error {
	for {
		var affected int64
		err := s.transaction(ctx, func(tx *gorm.DB) error {
			current, err := readInstance(tx, instanceID)
			if err != nil {
				return err
			}
			if current.Enabled == 1 {
				return errors.New("实例在删除过程中被启用，没有删除")
			}
			affected, err = exec(tx)
			return err
		})
		if err != nil {
			return err
		}
		if affected < batch {
			return nil
		}
	}
}
