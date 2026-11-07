package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/factorname"
	"gorm.io/gorm"
)

// 实例健康状态：degraded 是求值类问题，下一个 ok 周期自动恢复；session_unverified 是 Trade 会话结果未知，
// 只有重新核实会话成功才恢复，ok 周期不会清除它。
const (
	HealthOK                = "ok"
	HealthDegraded          = "degraded"
	HealthSessionUnverified = "session_unverified"
)

// Instance 把定义绑定到一个空间、一个或两个因子任务和可选的组合账户。因子任务在创建时写定、不能修改；读取的 View 是任务结果数据集的默认 View。
type Instance struct {
	InstanceID string
	StrategyID string
	SpaceID    string
	// FactorTasks 是市场（spot、swap、equity）→ 因子任务 ID；统一数据集上的任务登记为 multi。
	FactorTasks      map[string]string
	LogicalAccountID *string
	Enabled          bool
	SessionID        *string
	ResolvedJSON     json.RawMessage
	Health           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type instanceRow struct {
	InstanceID       string         `gorm:"column:c_instance_id"`
	StrategyID       string         `gorm:"column:c_strategy_id"`
	SpaceID          string         `gorm:"column:c_space_id"`
	LogicalAccountID sql.NullString `gorm:"column:c_logical_account_id"`
	Enabled          int            `gorm:"column:c_enabled"`
	SessionID        sql.NullString `gorm:"column:c_session_id"`
	ResolvedJSON     string         `gorm:"column:c_resolved_json"`
	Health           string         `gorm:"column:c_health"`
	CreatedAt        time.Time      `gorm:"column:c_ctime"`
	UpdatedAt        time.Time      `gorm:"column:c_mtime"`
}

func (r instanceRow) instance() Instance {
	return Instance{
		InstanceID: r.InstanceID, StrategyID: r.StrategyID, SpaceID: r.SpaceID,
		LogicalAccountID: nullableString(r.LogicalAccountID), Enabled: r.Enabled == 1, SessionID: nullableString(r.SessionID),
		ResolvedJSON: json.RawMessage(r.ResolvedJSON), Health: r.Health,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// 实例的 SQL：整条写成常量，不在调用处拼接；读实例的语句列清单与 instanceRow 一一对应。
const (
	// sqlSelectInstanceByID 按实例 ID 读取实例。
	sqlSelectInstanceByID = `SELECT c_instance_id, c_strategy_id, c_space_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_ctime, c_mtime FROM t_strategy_instances WHERE c_instance_id = ?`
	// sqlSelectTasksOfInstance 读取一个实例绑定的因子任务。
	sqlSelectTasksOfInstance = `SELECT c_market, c_factor_task_id FROM t_strategy_instance_tasks WHERE c_instance_id = ?`
	// sqlSelectTasksOfInstances 按实例 ID 的 JSON 数组一次读出一批实例绑定的因子任务。
	sqlSelectTasksOfInstances = `SELECT c_instance_id, c_market, c_factor_task_id FROM t_strategy_instance_tasks WHERE c_instance_id IN (SELECT value FROM json_each(?))`
	// sqlInsertInstance 新建停用、无会话、健康的实例。
	sqlInsertInstance = `
		INSERT INTO t_strategy_instances (c_instance_id, c_strategy_id, c_space_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, 0, NULL, ?, 'ok', ?, ?)`
	// sqlInsertInstanceTask 登记实例在一个市场绑定的因子任务。
	sqlInsertInstanceTask = `INSERT INTO t_strategy_instance_tasks (c_instance_id, c_market, c_factor_task_id) VALUES (?, ?, ?)`
	// sqlUpdateInstance 修改停用实例的定义与账户绑定，并清空绑定解析、恢复健康。
	sqlUpdateInstance = `
		UPDATE t_strategy_instances SET c_strategy_id = ?, c_logical_account_id = ?, c_resolved_json = '{}', c_health = 'ok', c_mtime = ?
		WHERE c_instance_id = ? AND c_enabled = 0`

	// ListInstances 按过滤条件选用的四个变体：全部、按空间、按启用状态、按空间与启用状态。
	sqlListInstances               = `SELECT c_instance_id, c_strategy_id, c_space_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_ctime, c_mtime FROM t_strategy_instances WHERE 1 = 1 ORDER BY c_ctime DESC, c_instance_id`
	sqlListInstancesBySpace        = `SELECT c_instance_id, c_strategy_id, c_space_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_ctime, c_mtime FROM t_strategy_instances WHERE 1 = 1 AND c_space_id = ? ORDER BY c_ctime DESC, c_instance_id`
	sqlListInstancesByEnabled      = `SELECT c_instance_id, c_strategy_id, c_space_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_ctime, c_mtime FROM t_strategy_instances WHERE 1 = 1 AND c_enabled = ? ORDER BY c_ctime DESC, c_instance_id`
	sqlListInstancesBySpaceEnabled = `SELECT c_instance_id, c_strategy_id, c_space_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_ctime, c_mtime FROM t_strategy_instances WHERE 1 = 1 AND c_space_id = ? AND c_enabled = ? ORDER BY c_ctime DESC, c_instance_id`
	// sqlListEnabledInstancesByTask 列出空间内绑定了某个因子任务的启用实例。
	sqlListEnabledInstancesByTask = `
		SELECT c_instance_id, c_strategy_id, c_space_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_ctime, c_mtime FROM t_strategy_instances
		WHERE c_space_id = ? AND c_enabled = 1
		AND c_instance_id IN (SELECT c_instance_id FROM t_strategy_instance_tasks WHERE c_factor_task_id = ?)
		ORDER BY c_instance_id`

	// sqlEnableInstance 把挂着会话的停用实例置为启用，写入绑定解析并恢复健康。
	sqlEnableInstance = `UPDATE t_strategy_instances SET c_enabled = 1, c_session_id = ?, c_resolved_json = ?, c_health = 'ok', c_mtime = ? WHERE c_instance_id = ?`
	// sqlAttachInstanceSession 把会话挂到停用的实例上。
	sqlAttachInstanceSession = `UPDATE t_strategy_instances SET c_session_id = ?, c_mtime = ? WHERE c_instance_id = ?`
	// sqlClearInstanceSession 比较交换清空停用实例的会话。
	sqlClearInstanceSession = `UPDATE t_strategy_instances SET c_session_id = NULL, c_mtime = ? WHERE c_instance_id = ? AND c_enabled = 0 AND c_session_id = ?`
	// sqlSetInstanceHealth 更新健康状态，状态没变时不写。
	sqlSetInstanceHealth = `UPDATE t_strategy_instances SET c_health = ?, c_mtime = ? WHERE c_instance_id = ? AND c_health <> ?`
	// sqlMarkInstanceDegraded 把仍以该会话启用、健康的实例标记为 degraded。
	sqlMarkInstanceDegraded = `
		UPDATE t_strategy_instances SET c_health = 'degraded', c_mtime = ?
		WHERE c_instance_id = ? AND c_health = 'ok' AND c_enabled = 1 AND c_session_id = ?`
	// sqlRecoverInstanceHealth 把仍以该会话启用的 degraded 实例恢复为 ok。
	sqlRecoverInstanceHealth = `
		UPDATE t_strategy_instances SET c_health = 'ok', c_mtime = ?
		WHERE c_instance_id = ? AND c_health = 'degraded' AND c_enabled = 1 AND c_session_id = ?`
	// sqlDisableInstanceKeepSession 停用实例并保留待 Trade 释放的会话。
	sqlDisableInstanceKeepSession = `UPDATE t_strategy_instances SET c_enabled = 0, c_session_id = ?, c_mtime = ? WHERE c_instance_id = ?`
	// sqlDisableInstanceClearSession 停用实例并清空会话。
	sqlDisableInstanceClearSession = `UPDATE t_strategy_instances SET c_enabled = 0, c_session_id = NULL, c_mtime = ? WHERE c_instance_id = ?`
	// sqlSetInstanceHealthOnly 只改健康状态（停用事务里使用，不改 c_mtime）。
	sqlSetInstanceHealthOnly = `UPDATE t_strategy_instances SET c_health = ? WHERE c_instance_id = ?`

	// sqlSelectSessionForMatch 读取启用握手比对所需的会话字段。
	sqlSelectSessionForMatch = `SELECT c_instance_id, c_dsl_hash, c_resolved_json, c_closed_at FROM t_strategy_sessions WHERE c_session_id = ?`
	// sqlSelectLiveDefinitionHash 读取未删除定义当前的 DSL 哈希。
	sqlSelectLiveDefinitionHash = `SELECT c_dsl_hash FROM t_strategy_defs WHERE c_strategy_id = ? AND c_deleted_at IS NULL`
	// sqlCountLiveDefinition 统计未删除的定义（0 或 1）。
	sqlCountLiveDefinition = `SELECT COUNT(*) FROM t_strategy_defs WHERE c_strategy_id = ? AND c_deleted_at IS NULL`
)

func validateInstanceIdentity(instance Instance) error {
	if strings.TrimSpace(instance.InstanceID) == "" || strings.TrimSpace(instance.StrategyID) == "" || strings.TrimSpace(instance.SpaceID) == "" {
		return errors.New("实例缺少 instance_id、strategy_id 或 space_id")
	}
	if len(instance.FactorTasks) == 0 || len(instance.FactorTasks) > 2 {
		return errors.New("实例需要绑定一个或两个因子任务")
	}
	for market, taskID := range instance.FactorTasks {
		if market != "spot" && market != "swap" && market != "equity" && market != "multi" {
			return fmt.Errorf("factor_tasks 的市场 %q 不受支持，只能是 spot、swap、equity 或 multi", market)
		}
		if strings.TrimSpace(taskID) == "" {
			return fmt.Errorf("factor_tasks 的 %s 任务不能为空", market)
		}
	}
	if instance.LogicalAccountID != nil && strings.TrimSpace(*instance.LogicalAccountID) == "" {
		return errors.New("logical_account_id 不能为空字符串")
	}
	return nil
}

func jsonObjectOrEmpty(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return "", errors.New("JSON 必须是对象")
	}
	return string(raw), nil
}

func readInstance(tx *gorm.DB, instanceID string) (instanceRow, error) {
	var row instanceRow
	if err := tx.Raw(sqlSelectInstanceByID, instanceID).Scan(&row).Error; err != nil {
		return instanceRow{}, err
	}
	if row.InstanceID == "" {
		return instanceRow{}, ErrNotFound
	}
	return row, nil
}

// instanceTasks 读取一个实例绑定的因子任务（市场 → 任务）。
func instanceTasks(tx *gorm.DB, instanceID string) (map[string]string, error) {
	var rows []struct {
		Market       string `gorm:"column:c_market"`
		FactorTaskID string `gorm:"column:c_factor_task_id"`
	}
	if err := tx.Raw(sqlSelectTasksOfInstance, instanceID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	tasks := make(map[string]string, len(rows))
	for _, row := range rows {
		tasks[row.Market] = row.FactorTaskID
	}
	return tasks, nil
}

// withTasks 给一批实例行补上绑定的因子任务，按一次查询读取。
func withTasks(tx *gorm.DB, rows []instanceRow) ([]Instance, error) {
	instances := make([]Instance, 0, len(rows))
	if len(rows) == 0 {
		return instances, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.InstanceID)
	}
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	var taskRows []struct {
		InstanceID   string `gorm:"column:c_instance_id"`
		Market       string `gorm:"column:c_market"`
		FactorTaskID string `gorm:"column:c_factor_task_id"`
	}
	if err := tx.Raw(sqlSelectTasksOfInstances, string(idsJSON)).Scan(&taskRows).Error; err != nil {
		return nil, err
	}
	byInstance := make(map[string]map[string]string, len(rows))
	for _, taskRow := range taskRows {
		if byInstance[taskRow.InstanceID] == nil {
			byInstance[taskRow.InstanceID] = map[string]string{}
		}
		byInstance[taskRow.InstanceID][taskRow.Market] = taskRow.FactorTaskID
	}
	for _, row := range rows {
		instance := row.instance()
		instance.FactorTasks = byInstance[row.InstanceID]
		instances = append(instances, instance)
	}
	return instances, nil
}

// CreateInstance 新建停用状态的实例。
func (s *Store) CreateInstance(ctx context.Context, instance Instance) error {
	if err := validateInstanceIdentity(instance); err != nil {
		return err
	}
	now, err := requireTime(instance.CreatedAt)
	if err != nil {
		return err
	}
	resolved, err := jsonObjectOrEmpty(instance.ResolvedJSON)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := requireLiveDefinition(tx, instance.StrategyID); err != nil {
			return err
		}
		if err := tx.Exec(sqlInsertInstance, instance.InstanceID, instance.StrategyID, instance.SpaceID, stringValue(instance.LogicalAccountID), resolved, now, now).Error; err != nil {
			return err
		}
		for market, taskID := range instance.FactorTasks {
			if err := tx.Exec(sqlInsertInstanceTask, instance.InstanceID, market, strings.TrimSpace(taskID)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetInstance 读取实例。
func (s *Store) GetInstance(ctx context.Context, instanceID string) (Instance, error) {
	tx := s.db.WithContext(ctx)
	row, err := readInstance(tx, instanceID)
	if err != nil {
		return Instance{}, err
	}
	instance := row.instance()
	if instance.FactorTasks, err = instanceTasks(tx, instanceID); err != nil {
		return Instance{}, err
	}
	return instance, nil
}

// UpdateInstance 修改停用且无会话实例的定义与账户绑定。因子任务在创建时写定，不能修改（也不写这一列）。
func (s *Store) UpdateInstance(ctx context.Context, instance Instance) error {
	if err := validateInstanceIdentity(instance); err != nil {
		return err
	}
	now, err := requireTime(instance.UpdatedAt)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instance.InstanceID)
		if err != nil {
			return err
		}
		if current.Enabled == 1 {
			return errors.New("实例必须先停用才能修改")
		}
		tasks, err := instanceTasks(tx, instance.InstanceID)
		if err != nil {
			return err
		}
		if !SameTasks(tasks, instance.FactorTasks) {
			return errors.New("实例的因子任务在创建时写定，不能修改")
		}
		if current.SessionID.Valid {
			return errors.New("实例的启用或停用操作尚未完成")
		}
		if current.SpaceID != instance.SpaceID {
			return errors.New("实例不能迁移到其他空间")
		}
		if err := requireLiveDefinition(tx, instance.StrategyID); err != nil {
			return err
		}
		return tx.Exec(sqlUpdateInstance, instance.StrategyID, stringValue(instance.LogicalAccountID), now, instance.InstanceID).Error
	})
}

// ListInstances 列出实例；spaceID 为空表示全部空间。
func (s *Store) ListInstances(ctx context.Context, spaceID string, enabled *bool) ([]Instance, error) {
	bySpace := strings.TrimSpace(spaceID) != ""
	var query string
	var args []any
	switch {
	case bySpace && enabled != nil:
		query, args = sqlListInstancesBySpaceEnabled, []any{spaceID, boolInt(*enabled)}
	case bySpace:
		query, args = sqlListInstancesBySpace, []any{spaceID}
	case enabled != nil:
		query, args = sqlListInstancesByEnabled, []any{boolInt(*enabled)}
	default:
		query, args = sqlListInstances, []any{}
	}
	var rows []instanceRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return withTasks(s.db.WithContext(ctx), rows)
}

// EnabledInstancesByView 返回事件路由到某个 View 的启用实例：实例读取的是关联因子任务的结果 View，所以由 View ID
// 推出任务 ID 再按任务查；不是因子结果 View 的 ID 没有实例。
func (s *Store) EnabledInstancesByView(ctx context.Context, spaceID, viewID string) ([]Instance, error) {
	suffix, err := factorname.ParseResultViewID(viewID)
	if err != nil {
		return []Instance{}, nil
	}
	taskID, err := factorname.TaskID(suffix)
	if err != nil {
		return []Instance{}, nil
	}
	var rows []instanceRow
	if err := s.db.WithContext(ctx).Raw(sqlListEnabledInstancesByTask, spaceID, taskID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return withTasks(s.db.WithContext(ctx), rows)
}

// SameTasks 报告两份因子任务绑定相同。
func SameTasks(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for market, taskID := range a {
		if strings.TrimSpace(b[market]) != taskID {
			return false
		}
	}
	return true
}

// SetInstanceEnabled 推进启用握手：enabled=false 把会话挂到停用的实例上（启用流程先写会话再联系 Trade，认领响应
// 丢失时启动对账能释放这个确定的身份）；enabled=true 把挂着该会话的实例置为启用。两步都对会话做比较交换。
// 停用走 DisableInstance（同时关闭会话）。
func (s *Store) SetInstanceEnabled(ctx context.Context, instanceID string, enabled bool, sessionID *string, resolvedJSON json.RawMessage, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	if strings.TrimSpace(instanceID) == "" {
		return errors.New("instance_id 不能为空")
	}
	if sessionID == nil || strings.TrimSpace(*sessionID) == "" {
		return errors.New("启用实例或挂会话需要会话 ID")
	}
	session := strings.TrimSpace(*sessionID)
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instanceID)
		if err != nil {
			return err
		}
		if current.Enabled == 1 {
			if enabled && current.SessionID.Valid && current.SessionID.String == session {
				return nil
			}
			return errors.New("实例已处于启用状态")
		}
		if enabled {
			// 比较交换：会话必须已经挂在实例上、未关闭，并且与实例当前引用的定义版本和 View 一致；
			// 期间被对账清掉、关闭，或实例被改绑，都拒绝启用。
			if !current.SessionID.Valid || current.SessionID.String != session {
				return errors.New("实例的启用或停用操作尚未完成")
			}
			if err := requireMatchingSession(tx, current, session); err != nil {
				return err
			}
			resolved := current.ResolvedJSON
			if len(resolvedJSON) > 0 {
				resolved, err = jsonObjectOrEmpty(resolvedJSON)
				if err != nil {
					return err
				}
			}
			return tx.Exec(sqlEnableInstance, session, resolved, now, instanceID).Error
		}
		if current.SessionID.Valid && current.SessionID.String != session {
			return errors.New("实例的启用或停用操作尚未完成")
		}
		// 挂上会话前确认它属于该实例、未关闭，并与实例当前引用的定义版本和 View 一致：打开会话之后实例被改绑
		// 或定义被修改时，在联系 Trade 之前就拒绝。
		if err := requireMatchingSession(tx, current, session); err != nil {
			return err
		}
		return tx.Exec(sqlAttachInstanceSession, session, now, instanceID).Error
	})
}

// ClearInstanceSession 在 Trade 确认释放后清空停用实例的会话；比较交换，避免延迟的释放清掉新会话。
func (s *Store) ClearInstanceSession(ctx context.Context, instanceID, expectedSessionID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	if strings.TrimSpace(instanceID) == "" || strings.TrimSpace(expectedSessionID) == "" {
		return errors.New("instance_id 与期望的会话 ID 不能为空")
	}
	result := s.db.WithContext(ctx).Exec(sqlClearInstanceSession, now, instanceID, expectedSessionID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return nil
}

// SetInstanceHealth 更新健康状态（ok | degraded）。
func (s *Store) SetInstanceHealth(ctx context.Context, instanceID, health string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	if health != HealthOK && health != HealthDegraded && health != HealthSessionUnverified {
		return errors.New("健康状态只能是 ok、degraded 或 session_unverified")
	}
	result := s.db.WithContext(ctx).Exec(sqlSetInstanceHealth, health, now, instanceID, health)
	return result.Error
}

// MarkInstanceDegraded 在求值类问题出现时把健康的实例标记为 degraded；不覆盖 session_unverified。
// 只作用于仍以该会话启用的实例：求值期间实例被停用或换了会话时，这一期的结论已不代表实例的当前状态。
func (s *Store) MarkInstanceDegraded(ctx context.Context, instanceID, sessionID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(sqlMarkInstanceDegraded, now, instanceID, sessionID).Error
}

// RecoverInstanceHealth 在 ok 周期把 degraded 恢复为 ok；session_unverified 只能由重新核实会话恢复。
// 只作用于仍以该会话启用的实例：对账循环自动停用并标记的 degraded 不能被一个并发提交的 ok 周期抹掉。
func (s *Store) RecoverInstanceHealth(ctx context.Context, instanceID, sessionID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(sqlRecoverInstanceHealth, now, instanceID, sessionID).Error
}

// DisableInstance 在一个事务里停用实例并关闭会话：pending 非空时保留它等待 Trade 释放，否则清空。
// 停用与关闭会话必须同时成功，否则再次启用时可能沿用一个本应关闭的会话。health 非空时在同一事务里设置健康状态
// （对账自动停用时标记 degraded，让原因留在实例上）。
func (s *Store) DisableInstance(ctx context.Context, instanceID string, sessionID, pending *string, health string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	if health != "" && health != HealthOK && health != HealthDegraded && health != HealthSessionUnverified {
		return errors.New("健康状态只能是 ok、degraded 或 session_unverified")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instanceID)
		if err != nil {
			return err
		}
		// 比较交换：调用方看到的会话必须仍是实例当前的会话，不能关掉或清空一个之后才挂上的会话。
		if (sessionID == nil && current.SessionID.Valid) || (sessionID != nil && (!current.SessionID.Valid || current.SessionID.String != *sessionID)) {
			return errors.New("实例的会话已变化，请刷新后重试")
		}
		if pending != nil {
			if err := tx.Exec(sqlDisableInstanceKeepSession, *pending, now, instanceID).Error; err != nil {
				return err
			}
		} else if err := tx.Exec(sqlDisableInstanceClearSession, now, instanceID).Error; err != nil {
			return err
		}
		if health != "" {
			if err := tx.Exec(sqlSetInstanceHealthOnly, health, instanceID).Error; err != nil {
				return err
			}
		}
		if sessionID != nil {
			return tx.Exec(sqlCloseSession, now, *sessionID).Error
		}
		return nil
	})
}

// requireMatchingSession 确认会话属于该实例、未关闭，且固化的 DSL 版本与因子任务与实例当前的定义和因子任务一致。
func requireMatchingSession(tx *gorm.DB, instance instanceRow, sessionID string) error {
	var session struct {
		InstanceID   string       `gorm:"column:c_instance_id"`
		DSLHash      string       `gorm:"column:c_dsl_hash"`
		ResolvedJSON string       `gorm:"column:c_resolved_json"`
		ClosedAt     sql.NullTime `gorm:"column:c_closed_at"`
	}
	if err := tx.Raw(sqlSelectSessionForMatch, sessionID).Scan(&session).Error; err != nil {
		return err
	}
	if session.InstanceID != instance.InstanceID || session.ClosedAt.Valid {
		return errors.New("实例的会话已关闭或不属于该实例，请重新启用")
	}
	var definitionHash string
	if err := tx.Raw(sqlSelectLiveDefinitionHash, instance.StrategyID).Scan(&definitionHash).Error; err != nil {
		return err
	}
	var resolved struct {
		Tasks map[string]struct {
			FactorTaskID string `json:"factor_task_id"`
		} `json:"tasks"`
	}
	_ = json.Unmarshal([]byte(session.ResolvedJSON), &resolved)
	sessionTasks := make(map[string]string, len(resolved.Tasks))
	for market, task := range resolved.Tasks {
		sessionTasks[market] = task.FactorTaskID
	}
	tasks, err := instanceTasks(tx, instance.InstanceID)
	if err != nil {
		return err
	}
	if definitionHash != session.DSLHash || !SameTasks(sessionTasks, tasks) {
		return errors.New("实例在启用期间被修改，请重新启用")
	}
	return nil
}

// requireLiveDefinition 在写实例的事务里确认引用的定义存在且未删除：与软删除定义在同一连接上串行，
// 改绑或新建实例不会引用一个刚被删除的定义。
func requireLiveDefinition(tx *gorm.DB, strategyID string) error {
	var live int64
	if err := tx.Raw(sqlCountLiveDefinition, strategyID).Scan(&live).Error; err != nil {
		return err
	}
	if live == 0 {
		return fmt.Errorf("策略定义 %s 不存在", strategyID)
	}
	return nil
}
