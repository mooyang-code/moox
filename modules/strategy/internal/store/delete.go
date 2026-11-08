package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// 删除实例的 SQL：整条写成常量，不在调用处拼接。外键开着，按引用关系从叶子往根删。
const (
	sqlDeleteInstanceReadyEvents = `DELETE FROM t_strategy_ready_events WHERE c_instance_id = ?`
	sqlDeleteInstanceResultItems = `
		DELETE FROM t_strategy_result_items
		WHERE c_result_id IN (SELECT c_result_id FROM t_strategy_results WHERE c_instance_id = ?)`
	sqlDeleteInstanceResults    = `DELETE FROM t_strategy_results WHERE c_instance_id = ?`
	sqlDeleteInstanceSessions   = `DELETE FROM t_strategy_sessions WHERE c_instance_id = ?`
	sqlDeleteInstanceReplayBars = `
		DELETE FROM t_strategy_replay_bars
		WHERE c_replay_id IN (SELECT c_replay_id FROM t_strategy_replays WHERE c_instance_id = ?)`
	sqlDeleteInstanceReplays         = `DELETE FROM t_strategy_replays WHERE c_instance_id = ?`
	sqlDeleteInstanceDisplaySettings = `DELETE FROM t_strategy_display_settings WHERE c_instance_id = ?`
	sqlDeleteInstanceTasks           = `DELETE FROM t_strategy_instance_tasks WHERE c_instance_id = ?`
	// sqlDeleteStoppedInstance 只删已停用、没有会话的实例。
	sqlDeleteStoppedInstance = `DELETE FROM t_strategy_instances WHERE c_instance_id = ? AND c_enabled = 0 AND c_session_id IS NULL`
	// sqlSelectActiveInstanceReplays 列出实例还在排队或执行的回放。
	sqlSelectActiveInstanceReplays = `SELECT c_replay_id FROM t_strategy_replays WHERE c_instance_id = ? AND c_status IN ('pending', 'running') ORDER BY c_replay_id`
	// sqlDeleteViewsAgePassed 删除一组 View（JSON 数组参数）的年龄探针记录。
	sqlDeleteViewsAgePassed = `DELETE FROM t_strategy_age_passed WHERE c_space_id = ? AND c_view_id IN (SELECT value FROM json_each(?))`
)

// instanceScopedDeletes 是按实例 ID 删除的语句，顺序满足外键：结果明细在结果之前，结果、到达记录在会话之前，
// 回放记录在回放之前，实例本身最后单独删。
var instanceScopedDeletes = []string{
	sqlDeleteInstanceReadyEvents,
	sqlDeleteInstanceResultItems,
	sqlDeleteInstanceResults,
	sqlDeleteInstanceSessions,
	sqlDeleteInstanceReplayBars,
	sqlDeleteInstanceReplays,
	sqlDeleteInstanceDisplaySettings,
	sqlDeleteInstanceTasks,
}

// ActiveInstanceReplays 返回实例还在排队或执行的回放 ID。
func (s *Store) ActiveInstanceReplays(ctx context.Context, instanceID string) ([]string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Raw(sqlSelectActiveInstanceReplays, strings.TrimSpace(instanceID)).Scan(&ids).Error
	return ids, err
}

// DeleteInstance 在一个事务里删除已停用、没有会话的实例在策略库里的全部记录：会话、结果与明细、到达记录、回放、外显设置、
// 因子任务登记和实例本身；viewIDs 是实例结果数据集的 View，按 (空间, View) 存的年龄探针记录一并删除。
// 实例不存在返回 ErrNotFound；实例已启用或还挂着会话时什么也不删。
func (s *Store) DeleteInstance(ctx context.Context, instanceID, spaceID string, viewIDs []string) error {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return errors.New("实例 ID 不能为空")
	}
	views, err := json.Marshal(append([]string{}, viewIDs...))
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instanceID)
		if err != nil {
			return err
		}
		if current.Enabled == 1 || current.SessionID.Valid {
			return errors.New("实例已启用或还有会话，不能删除")
		}
		for _, statement := range instanceScopedDeletes {
			if err := tx.Exec(statement, instanceID).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(sqlDeleteViewsAgePassed, spaceID, string(views)).Error; err != nil {
			return err
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
