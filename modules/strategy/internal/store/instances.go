package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 实例健康状态。
const (
	HealthOK       = "ok"
	HealthDegraded = "degraded"
)

// Instance 把定义绑定到一个空间、一个 View 和可选的组合账户。
type Instance struct {
	InstanceID       string
	StrategyID       string
	SpaceID          string
	ViewID           string
	LogicalAccountID *string
	Enabled          bool
	SessionID        *string
	ResolvedJSON     json.RawMessage
	Health           string
	DeletedAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type instanceRow struct {
	InstanceID       string         `gorm:"column:c_instance_id"`
	StrategyID       string         `gorm:"column:c_strategy_id"`
	SpaceID          string         `gorm:"column:c_space_id"`
	ViewID           string         `gorm:"column:c_view_id"`
	LogicalAccountID sql.NullString `gorm:"column:c_logical_account_id"`
	Enabled          int            `gorm:"column:c_enabled"`
	SessionID        sql.NullString `gorm:"column:c_session_id"`
	ResolvedJSON     string         `gorm:"column:c_resolved_json"`
	Health           string         `gorm:"column:c_health"`
	DeletedAt        sql.NullTime   `gorm:"column:c_deleted_at"`
	CreatedAt        time.Time      `gorm:"column:c_ctime"`
	UpdatedAt        time.Time      `gorm:"column:c_mtime"`
}

func (r instanceRow) instance() Instance {
	return Instance{
		InstanceID: r.InstanceID, StrategyID: r.StrategyID, SpaceID: r.SpaceID, ViewID: r.ViewID,
		LogicalAccountID: nullableString(r.LogicalAccountID), Enabled: r.Enabled == 1, SessionID: nullableString(r.SessionID),
		ResolvedJSON: json.RawMessage(r.ResolvedJSON), Health: r.Health, DeletedAt: nullableTime(r.DeletedAt),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

const instanceColumns = "c_instance_id, c_strategy_id, c_space_id, c_view_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_deleted_at, c_ctime, c_mtime"

func validateInstanceIdentity(instance Instance) error {
	if strings.TrimSpace(instance.InstanceID) == "" || strings.TrimSpace(instance.StrategyID) == "" || strings.TrimSpace(instance.SpaceID) == "" || strings.TrimSpace(instance.ViewID) == "" {
		return errors.New("实例缺少 instance_id、strategy_id、space_id 或 view_id")
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
	if err := tx.Raw(`SELECT `+instanceColumns+` FROM t_strategy_instances WHERE c_instance_id = ?`, instanceID).Scan(&row).Error; err != nil {
		return instanceRow{}, err
	}
	if row.InstanceID == "" {
		return instanceRow{}, ErrNotFound
	}
	return row, nil
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
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_instances (c_instance_id, c_strategy_id, c_space_id, c_view_id, c_logical_account_id, c_enabled, c_session_id, c_resolved_json, c_health, c_deleted_at, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, 0, NULL, ?, 'ok', NULL, ?, ?)
	`, instance.InstanceID, instance.StrategyID, instance.SpaceID, instance.ViewID, stringValue(instance.LogicalAccountID), resolved, now, now).Error
}

// GetInstance 读取实例，包括已软删除的（DeletedAt 非空）。
func (s *Store) GetInstance(ctx context.Context, instanceID string) (Instance, error) {
	row, err := readInstance(s.db.WithContext(ctx), instanceID)
	if err != nil {
		return Instance{}, err
	}
	return row.instance(), nil
}

// UpdateInstance 修改停用且无会话实例的定义、View 与账户绑定。
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
		if current.DeletedAt.Valid {
			return ErrNotFound
		}
		if current.Enabled == 1 {
			return errors.New("实例必须先停用才能修改")
		}
		if current.SessionID.Valid {
			return errors.New("实例的启用或停用操作尚未完成")
		}
		if current.SpaceID != instance.SpaceID {
			return errors.New("实例不能迁移到其他空间")
		}
		return tx.Exec(`
			UPDATE t_strategy_instances SET c_strategy_id = ?, c_view_id = ?, c_logical_account_id = ?, c_resolved_json = '{}', c_health = 'ok', c_mtime = ?
			WHERE c_instance_id = ? AND c_enabled = 0
		`, instance.StrategyID, instance.ViewID, stringValue(instance.LogicalAccountID), now, instance.InstanceID).Error
	})
}

// ListInstances 列出未删除的实例；spaceID 为空表示全部空间。
func (s *Store) ListInstances(ctx context.Context, spaceID string, enabled *bool) ([]Instance, error) {
	query := `SELECT ` + instanceColumns + ` FROM t_strategy_instances WHERE c_deleted_at IS NULL`
	args := []any{}
	if strings.TrimSpace(spaceID) != "" {
		query += ` AND c_space_id = ?`
		args = append(args, spaceID)
	}
	if enabled != nil {
		query += ` AND c_enabled = ?`
		args = append(args, boolInt(*enabled))
	}
	query += ` ORDER BY c_ctime DESC, c_instance_id`
	var rows []instanceRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(rows))
	for _, row := range rows {
		instances = append(instances, row.instance())
	}
	return instances, nil
}

// EnabledInstancesByView 返回绑定某个 View 的启用实例，供事件路由。
func (s *Store) EnabledInstancesByView(ctx context.Context, spaceID, viewID string) ([]Instance, error) {
	var rows []instanceRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT `+instanceColumns+` FROM t_strategy_instances
		WHERE c_space_id = ? AND c_view_id = ? AND c_enabled = 1 AND c_deleted_at IS NULL
		ORDER BY c_instance_id
	`, spaceID, viewID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(rows))
	for _, row := range rows {
		instances = append(instances, row.instance())
	}
	return instances, nil
}

// SetInstanceEnabled 持久化生命周期状态。启用需要会话 ID；带会话的停用保留会话直到 Trade 确认释放，
// 不带会话的停用（观察实例）立即清空会话。enabled=false 且带会话也用于"启用进行中"的中间状态。
func (s *Store) SetInstanceEnabled(ctx context.Context, instanceID string, enabled bool, sessionID *string, resolvedJSON json.RawMessage, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	if strings.TrimSpace(instanceID) == "" {
		return errors.New("instance_id 不能为空")
	}
	if enabled && (sessionID == nil || strings.TrimSpace(*sessionID) == "") {
		return errors.New("启用实例需要会话 ID")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instanceID)
		if err != nil {
			return err
		}
		if current.DeletedAt.Valid {
			return ErrNotFound
		}
		if enabled {
			if current.Enabled == 1 {
				if current.SessionID.Valid && current.SessionID.String == *sessionID {
					return nil
				}
				return errors.New("实例已处于启用状态")
			}
			if current.SessionID.Valid && current.SessionID.String != *sessionID {
				return errors.New("实例的启用或停用操作尚未完成")
			}
			resolved := current.ResolvedJSON
			if len(resolvedJSON) > 0 {
				resolved, err = jsonObjectOrEmpty(resolvedJSON)
				if err != nil {
					return err
				}
			}
			return tx.Exec(`UPDATE t_strategy_instances SET c_enabled = 1, c_session_id = ?, c_resolved_json = ?, c_health = 'ok', c_mtime = ? WHERE c_instance_id = ?`, *sessionID, resolved, now, instanceID).Error
		}
		if sessionID != nil && strings.TrimSpace(*sessionID) != "" {
			if current.SessionID.Valid && current.SessionID.String != strings.TrimSpace(*sessionID) {
				return errors.New("实例的启用或停用操作尚未完成")
			}
			return tx.Exec(`UPDATE t_strategy_instances SET c_enabled = 0, c_session_id = ?, c_mtime = ? WHERE c_instance_id = ?`, strings.TrimSpace(*sessionID), now, instanceID).Error
		}
		return tx.Exec(`UPDATE t_strategy_instances SET c_enabled = 0, c_session_id = NULL, c_mtime = ? WHERE c_instance_id = ?`, now, instanceID).Error
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
	result := s.db.WithContext(ctx).Exec(`UPDATE t_strategy_instances SET c_session_id = NULL, c_mtime = ? WHERE c_instance_id = ? AND c_enabled = 0 AND c_session_id = ?`, now, instanceID, expectedSessionID)
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
	if health != HealthOK && health != HealthDegraded {
		return errors.New("健康状态只能是 ok 或 degraded")
	}
	result := s.db.WithContext(ctx).Exec(`UPDATE t_strategy_instances SET c_health = ?, c_mtime = ? WHERE c_instance_id = ? AND c_health <> ?`, health, now, instanceID, health)
	return result.Error
}

// SoftDeleteInstance 软删除停用且无会话的实例；会话与结果保留。
func (s *Store) SoftDeleteInstance(ctx context.Context, instanceID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := readInstance(tx, instanceID)
		if err != nil {
			return err
		}
		if current.DeletedAt.Valid {
			return ErrNotFound
		}
		if current.Enabled == 1 {
			return errors.New("实例必须先停用才能删除")
		}
		if current.SessionID.Valid {
			return errors.New("实例的停用操作尚未完成，请稍后再删除")
		}
		return tx.Exec(`UPDATE t_strategy_instances SET c_deleted_at = ?, c_mtime = ? WHERE c_instance_id = ?`, now, now, instanceID).Error
	})
}
