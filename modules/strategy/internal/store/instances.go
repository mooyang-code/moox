package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/factorname"
	"gorm.io/gorm"
)

// 实例健康状态：degraded 是求值类问题，下一个 ok 周期自动恢复。
const (
	HealthOK       = "ok"
	HealthDegraded = "degraded"
)

// Instance 把定义绑定到一个空间、一个或两个因子任务和可选的组合账户。因子任务在创建时写定、不能修改；读取的是任务结果数据集的 View。
type Instance struct {
	InstanceID string
	StrategyID string
	// StrategyName 是关联策略定义的名称，由读取查询从 t_strategy_defs 带出。
	StrategyName string
	SpaceID      string
	// FactorTasks 是任务键 → 因子任务 ID：主帧与参考市场的任务键是市场（spot、swap、equity）；
	// 附加来源的任务键是 来源名:市场（见 dsl.TaskKey）。
	FactorTasks      map[string]string
	LogicalAccountID *string
	Enabled          bool
	// ConfirmedAt 为空表示试运行：目标只发影子、不下单。没挂账户的实例启用时写入，挂了账户的由用户确认实盘时写入；
	// 停用、修改实例都清空它。
	ConfirmedAt *time.Time
	SessionID   *string
	// ResolvedJSON 是绑定解析，只读：取当前会话固化的那份，停用的实例取最近一次会话的（没启用过为 {}）。
	ResolvedJSON json.RawMessage
	Health       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Trial 报告实例是否在试运行：启用、挂了组合账户、还没确认实盘。试运行的目标带影子标记，Trade 只换算并记录、不下单。
func (i Instance) Trial() bool {
	return i.Enabled && i.LogicalAccountID != nil && i.ConfirmedAt == nil
}

type instanceRow struct {
	InstanceID       string         `gorm:"column:c_instance_id"`
	StrategyID       string         `gorm:"column:c_strategy_id"`
	StrategyName     string         `gorm:"column:c_strategy_name"`
	SpaceID          string         `gorm:"column:c_space_id"`
	LogicalAccountID sql.NullString `gorm:"column:c_logical_account_id"`
	ConfirmedAt      sql.NullTime   `gorm:"column:c_confirmed_at"`
	SessionID        sql.NullString `gorm:"column:c_session_id"`
	ResolvedJSON     string         `gorm:"column:c_resolved_json"`
	Health           string         `gorm:"column:c_health"`
	CreatedAt        time.Time      `gorm:"column:c_ctime"`
	UpdatedAt        time.Time      `gorm:"column:c_mtime"`
}

func (r instanceRow) instance() Instance {
	return Instance{
		InstanceID: r.InstanceID, StrategyID: r.StrategyID, StrategyName: r.StrategyName, SpaceID: r.SpaceID,
		LogicalAccountID: nullableString(r.LogicalAccountID), Enabled: r.SessionID.Valid, ConfirmedAt: nullableTime(r.ConfirmedAt), SessionID: nullableString(r.SessionID),
		ResolvedJSON: json.RawMessage(r.ResolvedJSON), Health: r.Health,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// 实例的 SQL：整条写成常量，不在调用处拼接；读实例的语句列清单与 instanceRow 一一对应。
const (
	// sqlSelectInstances 是读实例的公共部分。绑定解析只存在会话里：启用的实例当前会话就是它开始时刻最大的会话
	// （会话开始时刻全库严格递增），所以“当前会话，没有则最近一次会话”就是按开始时刻取最近一次。
	sqlSelectInstances = `SELECT i.c_instance_id, i.c_strategy_id, COALESCE(d.c_name, '') AS c_strategy_name, i.c_space_id, i.c_logical_account_id, i.c_confirmed_at, i.c_session_id,
		COALESCE((SELECT s.c_resolved_json FROM t_strategy_sessions s WHERE s.c_instance_id = i.c_instance_id ORDER BY s.c_started_at DESC LIMIT 1), '{}') AS c_resolved_json,
		i.c_health, i.c_ctime, i.c_mtime FROM t_strategy_instances i
		LEFT JOIN t_strategy_defs d ON d.c_strategy_id = i.c_strategy_id`
	// sqlSelectInstanceByID 按实例 ID 读取实例。
	sqlSelectInstanceByID = sqlSelectInstances + ` WHERE i.c_instance_id = ?`
	// sqlSelectTasksOfInstance 读取一个实例绑定的因子任务。
	sqlSelectTasksOfInstance = `SELECT c_source, c_market, c_factor_task_id FROM t_strategy_instance_tasks WHERE c_instance_id = ?`
	// sqlSelectTasksOfInstances 按实例 ID 的 JSON 数组一次读出一批实例绑定的因子任务。
	sqlSelectTasksOfInstances = `SELECT c_instance_id, c_source, c_market, c_factor_task_id FROM t_strategy_instance_tasks WHERE c_instance_id IN (SELECT value FROM json_each(?))`
	// sqlInsertInstance 新建停用、无会话、健康的实例。
	sqlInsertInstance = `
		INSERT INTO t_strategy_instances (c_instance_id, c_strategy_id, c_space_id, c_logical_account_id, c_session_id, c_health, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, NULL, 'ok', ?, ?)`
	// sqlInsertInstanceTask 登记实例在一个（来源, 市场）绑定的因子任务。
	sqlInsertInstanceTask = `INSERT INTO t_strategy_instance_tasks (c_instance_id, c_source, c_market, c_factor_task_id) VALUES (?, ?, ?, ?)`
	// sqlUpdateInstance 修改停用实例的定义与账户绑定，并清空确认时间、恢复健康。
	sqlUpdateInstance = `
		UPDATE t_strategy_instances SET c_strategy_id = ?, c_logical_account_id = ?, c_health = 'ok', c_confirmed_at = NULL, c_mtime = ?
		WHERE c_instance_id = ? AND c_session_id IS NULL`

	// ListInstances 按过滤条件选用的变体：全部、按空间、按启用状态、按空间与启用状态。
	sqlListInstances                = sqlSelectInstances + ` ORDER BY i.c_ctime DESC, i.c_instance_id`
	sqlListInstancesBySpace         = sqlSelectInstances + ` WHERE i.c_space_id = ? ORDER BY i.c_ctime DESC, i.c_instance_id`
	sqlListInstancesEnabled         = sqlSelectInstances + ` WHERE i.c_session_id IS NOT NULL ORDER BY i.c_ctime DESC, i.c_instance_id`
	sqlListInstancesDisabled        = sqlSelectInstances + ` WHERE i.c_session_id IS NULL ORDER BY i.c_ctime DESC, i.c_instance_id`
	sqlListInstancesBySpaceEnabled  = sqlSelectInstances + ` WHERE i.c_space_id = ? AND i.c_session_id IS NOT NULL ORDER BY i.c_ctime DESC, i.c_instance_id`
	sqlListInstancesBySpaceDisabled = sqlSelectInstances + ` WHERE i.c_space_id = ? AND i.c_session_id IS NULL ORDER BY i.c_ctime DESC, i.c_instance_id`
	// sqlListEnabledInstancesByTask 列出空间内绑定了某个因子任务的启用实例。
	sqlListEnabledInstancesByTask = sqlSelectInstances + `
		WHERE i.c_space_id = ? AND i.c_session_id IS NOT NULL
		AND i.c_instance_id IN (SELECT t.c_instance_id FROM t_strategy_instance_tasks t WHERE t.c_factor_task_id = ?)
		ORDER BY i.c_instance_id`

	// sqlListInstanceIDsByTask 列出登记了某个因子任务的全部实例（不分空间与启用状态）。
	sqlListInstanceIDsByTask = `SELECT DISTINCT c_instance_id FROM t_strategy_instance_tasks WHERE c_factor_task_id = ? ORDER BY c_instance_id`

	// sqlEnableInstance 把停用的实例挂上会话并置为启用，恢复健康。确认时间按实例行上当时绑的账户决定：
	// 没挂账户的直接写入（观察实例不下单，不需要试运行），挂了账户的置空，从试运行开始。
	sqlEnableInstance = `
		UPDATE t_strategy_instances SET c_session_id = ?, c_health = 'ok',
			c_confirmed_at = CASE WHEN c_logical_account_id IS NULL THEN ? ELSE NULL END, c_mtime = ?
		WHERE c_instance_id = ? AND c_session_id IS NULL`
	// sqlMarkInstanceDegraded 把仍以该会话启用、健康的实例标记为 degraded。
	sqlMarkInstanceDegraded = `
		UPDATE t_strategy_instances SET c_health = 'degraded', c_mtime = ?
		WHERE c_instance_id = ? AND c_health = 'ok' AND c_session_id = ?`
	// sqlRecoverInstanceHealth 把仍以该会话启用的 degraded 实例恢复为 ok。
	sqlRecoverInstanceHealth = `
		UPDATE t_strategy_instances SET c_health = 'ok', c_mtime = ?
		WHERE c_instance_id = ? AND c_health = 'degraded' AND c_session_id = ?`
	// sqlDisableInstance 比较交换停用以该会话启用的实例，并清掉实例上的会话与确认时间。
	sqlDisableInstance = `UPDATE t_strategy_instances SET c_session_id = NULL, c_confirmed_at = NULL, c_mtime = ? WHERE c_instance_id = ? AND c_session_id = ?`
	// sqlConfirmInstance 比较交换确认实盘：实例仍以该会话启用、挂了组合账户、还没确认。
	sqlConfirmInstance = `
		UPDATE t_strategy_instances SET c_confirmed_at = ?, c_mtime = ?
		WHERE c_instance_id = ? AND c_session_id = ? AND c_logical_account_id IS NOT NULL AND c_confirmed_at IS NULL`

	// sqlSelectLiveDefinitionHash 读取未删除定义当前的 DSL 哈希。
	sqlSelectLiveDefinitionHash = `SELECT c_dsl_hash FROM t_strategy_defs WHERE c_strategy_id = ? AND c_deleted_at IS NULL`
	// sqlCountLiveDefinition 统计未删除的定义（0 或 1）。
	sqlCountLiveDefinition = `SELECT COUNT(*) FROM t_strategy_defs WHERE c_strategy_id = ? AND c_deleted_at IS NULL`
)

func validateInstanceIdentity(instance Instance) error {
	if strings.TrimSpace(instance.InstanceID) == "" || strings.TrimSpace(instance.StrategyID) == "" || strings.TrimSpace(instance.SpaceID) == "" {
		return errors.New("实例缺少 instance_id、strategy_id 或 space_id")
	}
	if err := ValidateTaskKeys(instance.FactorTasks); err != nil {
		return err
	}
	if instance.LogicalAccountID != nil && strings.TrimSpace(*instance.LogicalAccountID) == "" {
		return errors.New("logical_account_id 不能为空字符串")
	}
	return nil
}

// 任务个数上限：主帧与参考市场至多两个（模式市场数 + 参考市场数），附加来源至多两个、每个至多两个市场（与主帧的市场一致）。
// 与策略相关的精确个数由启用时的解析核对。
const (
	maxPlainTasks      = 2
	maxSources         = dsl.MaxSources
	maxMarketsInSource = 2
)

// ValidateTaskKeys 校验实例登记的因子任务：键是市场或 来源名:市场，市场只能是 spot、swap、equity，任务 ID 不能为空，个数不超过上限。
func ValidateTaskKeys(tasks map[string]string) error {
	plain := 0
	sources := map[string]int{}
	for key, taskID := range tasks {
		source, market := dsl.SplitTaskKey(key)
		if market != "spot" && market != "swap" && market != "equity" {
			return fmt.Errorf("factor_tasks 的市场 %q 不受支持，只能是 spot、swap 或 equity（附加来源写作 来源名:市场）", market)
		}
		if strings.Contains(key, ":") && !dsl.ValidSourceName(source) {
			return fmt.Errorf("factor_tasks 的键 %q 不合法：来源名必须是小写字母开头的 snake_case", key)
		}
		if strings.TrimSpace(taskID) == "" {
			return fmt.Errorf("factor_tasks 的 %s 任务不能为空", key)
		}
		if source == "" {
			plain++
		} else {
			sources[source]++
		}
	}
	if plain == 0 || plain > maxPlainTasks {
		return fmt.Errorf("实例需要按市场绑定一个或两个主帧（含参考市场）因子任务，实际 %d 个", plain)
	}
	if len(sources) > maxSources {
		return fmt.Errorf("实例至多绑定 %d 个附加来源，实际 %d 个", maxSources, len(sources))
	}
	for source, count := range sources {
		if count > maxMarketsInSource {
			return fmt.Errorf("附加来源 %s 至多绑定 %d 个市场的任务，实际 %d 个", source, maxMarketsInSource, count)
		}
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

// instanceTasks 读取一个实例绑定的因子任务（任务键 → 任务）。
func instanceTasks(tx *gorm.DB, instanceID string) (map[string]string, error) {
	var rows []struct {
		Source       string `gorm:"column:c_source"`
		Market       string `gorm:"column:c_market"`
		FactorTaskID string `gorm:"column:c_factor_task_id"`
	}
	if err := tx.Raw(sqlSelectTasksOfInstance, instanceID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	tasks := make(map[string]string, len(rows))
	for _, row := range rows {
		tasks[dsl.TaskKey(row.Source, row.Market)] = row.FactorTaskID
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
		Source       string `gorm:"column:c_source"`
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
		byInstance[taskRow.InstanceID][dsl.TaskKey(taskRow.Source, taskRow.Market)] = taskRow.FactorTaskID
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
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := requireLiveDefinition(tx, instance.StrategyID); err != nil {
			return err
		}
		if err := tx.Exec(sqlInsertInstance, instance.InstanceID, instance.StrategyID, instance.SpaceID, stringValue(instance.LogicalAccountID), now, now).Error; err != nil {
			return err
		}
		for key, taskID := range instance.FactorTasks {
			source, market := dsl.SplitTaskKey(key)
			if err := tx.Exec(sqlInsertInstanceTask, instance.InstanceID, source, market, strings.TrimSpace(taskID)).Error; err != nil {
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

// UpdateInstance 修改停用实例的定义与账户绑定。因子任务在创建时写定，不能修改（也不写这一列）。
// 同一个事务里清掉实例的规则状态：状态是按原来的定义和账户积累的理论持仓，换了账户或定义之后不能接着用。
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
		if current.SessionID.Valid {
			return errors.New("实例必须先停用才能修改")
		}
		tasks, err := instanceTasks(tx, instance.InstanceID)
		if err != nil {
			return err
		}
		if !SameTasks(tasks, instance.FactorTasks) {
			return errors.New("实例的因子任务在创建时写定，不能修改")
		}
		if current.SpaceID != instance.SpaceID {
			return errors.New("实例不能迁移到其他空间")
		}
		if err := requireLiveDefinition(tx, instance.StrategyID); err != nil {
			return err
		}
		if err := tx.Exec(sqlDeleteState, instance.InstanceID).Error; err != nil {
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
	case bySpace && enabled != nil && *enabled:
		query, args = sqlListInstancesBySpaceEnabled, []any{spaceID}
	case bySpace && enabled != nil:
		query, args = sqlListInstancesBySpaceDisabled, []any{spaceID}
	case bySpace:
		query, args = sqlListInstancesBySpace, []any{spaceID}
	case enabled != nil && *enabled:
		query, args = sqlListInstancesEnabled, []any{}
	case enabled != nil:
		query, args = sqlListInstancesDisabled, []any{}
	default:
		query, args = sqlListInstances, []any{}
	}
	var rows []instanceRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return withTasks(s.db.WithContext(ctx), rows)
}

// EnabledInstancesByDataset 返回事件路由到某个数据集的启用实例：实例读取的是关联因子任务的结果数据集，所以由数据集 ID
// 推出任务 ID 再按任务查；不是因子结果数据集的 ID 没有实例。
func (s *Store) EnabledInstancesByDataset(ctx context.Context, spaceID, datasetID string) ([]Instance, error) {
	suffix, err := factorname.ParseResultDatasetID(datasetID)
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

// InstanceIDsByTask 返回登记了因子任务 taskID 的全部实例 ID（排序；不分空间与启用状态）。删除实例时据此找出还在引用
// 要删的任务的其他实例。
func (s *Store) InstanceIDsByTask(ctx context.Context, taskID string) ([]string, error) {
	var ids []string
	if err := s.db.WithContext(ctx).Raw(sqlListInstanceIDsByTask, strings.TrimSpace(taskID)).Scan(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
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

// MarkInstanceDegraded 在求值类问题出现时把健康的实例标记为 degraded。
// 只作用于仍以该会话启用的实例：求值期间实例被停用或换了会话时，这一期的结论已不代表实例的当前状态。
func (s *Store) MarkInstanceDegraded(ctx context.Context, instanceID, sessionID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(sqlMarkInstanceDegraded, now, instanceID, sessionID).Error
}

// RecoverInstanceHealth 在 ok 周期把 degraded 恢复为 ok。只作用于仍以该会话启用的实例。
func (s *Store) RecoverInstanceHealth(ctx context.Context, instanceID, sessionID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(sqlRecoverInstanceHealth, now, instanceID, sessionID).Error
}

// DisableInstance 在一个事务里停用实例（批 3 的 3.6）：置为停用、清掉实例上的会话、关闭这个会话；停用的实例上从不挂会话。
// 比较交换：实例必须仍以 sessionID 启用，不能关掉一个之后才换上的会话。通知 Trade 关闭会话由调用方在提交之后做。
func (s *Store) DisableInstance(ctx context.Context, instanceID, sessionID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	if strings.TrimSpace(instanceID) == "" || strings.TrimSpace(sessionID) == "" {
		return errors.New("停用实例需要 instance_id 与会话 ID")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instanceID)
		if err != nil {
			return err
		}
		if !current.SessionID.Valid || current.SessionID.String != sessionID {
			return errConfirmSessionChanged
		}
		if err := tx.Exec(sqlDisableInstance, now, instanceID, sessionID).Error; err != nil {
			return err
		}
		return tx.Exec(sqlCloseSession, now, sessionID).Error
	})
}

// ConfirmInstance 把以 sessionID 启用、挂了组合账户的试运行实例确认为实盘，之后的目标不再带影子标记。
// 已经确认的再确认一次是成功（不改确认时间）；实例已停用、换了会话或没挂账户时拒绝。预热等业务前提由调用方检查。
func (s *Store) ConfirmInstance(ctx context.Context, instanceID, sessionID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	if strings.TrimSpace(instanceID) == "" || strings.TrimSpace(sessionID) == "" {
		return errors.New("确认实例需要 instance_id 与会话 ID")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instanceID)
		if err != nil {
			return err
		}
		if !current.SessionID.Valid || current.SessionID.String != sessionID {
			return errConfirmSessionChanged
		}
		if !current.LogicalAccountID.Valid {
			return errors.New("实例没有挂组合账户，不需要确认")
		}
		if current.ConfirmedAt.Valid {
			return nil
		}
		return confirmInstanceRow(tx, instanceID, sessionID, now)
	})
}

// errConfirmSessionChanged 是确认时实例的会话已经变了（停用、重新启用）的拒绝说明；页面原样显示给用户。
var errConfirmSessionChanged = errors.New("实例的会话已变化，请刷新后重试")

// confirmInstanceRow 按“还是这次启用、还没确认”的条件写入确认时间；一行都没改到说明条件已经不成立，不能当成确认成功。
func confirmInstanceRow(tx *gorm.DB, instanceID, sessionID string, now time.Time) error {
	result := tx.Exec(sqlConfirmInstance, now, now, instanceID, sessionID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errConfirmSessionChanged
	}
	return nil
}

// SessionTasks从会话固化的绑定解析（resolved_json）里取出全部任务键 → 因子任务：主帧的 tasks、参考市场 references 与附加来源
// sources（任务键 来源名:市场）。无法解析时返回空表。
func SessionTasks(resolvedJSON []byte) map[string]string {
	type task struct {
		FactorTaskID string `json:"factor_task_id"`
	}
	var resolved struct {
		Tasks      map[string]task `json:"tasks"`
		References map[string]task `json:"references"`
		Sources    map[string]struct {
			Tasks map[string]task `json:"tasks"`
		} `json:"sources"`
	}
	_ = json.Unmarshal(resolvedJSON, &resolved)
	tasks := make(map[string]string, len(resolved.Tasks)+len(resolved.References))
	for key, item := range resolved.Tasks {
		tasks[key] = item.FactorTaskID
	}
	for market, item := range resolved.References {
		tasks[market] = item.FactorTaskID
	}
	for name, source := range resolved.Sources {
		for market, item := range source.Tasks {
			tasks[dsl.TaskKey(name, market)] = item.FactorTaskID
		}
	}
	return tasks
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
