package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Definition 是一份策略定义：DSL 文本及其内容哈希。
type Definition struct {
	StrategyID string
	Name       string
	DSLYaml    string
	DSLHash    string
	DeletedAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DefinitionVersion 是出现过的一个 DSL 版本。
type DefinitionVersion struct {
	DSLHash   string
	DSLYaml   string
	CreatedAt time.Time
}

type definitionRow struct {
	StrategyID string       `gorm:"column:c_strategy_id"`
	Name       string       `gorm:"column:c_name"`
	DSLYaml    string       `gorm:"column:c_dsl_yaml"`
	DSLHash    string       `gorm:"column:c_dsl_hash"`
	DeletedAt  sql.NullTime `gorm:"column:c_deleted_at"`
	CreatedAt  time.Time    `gorm:"column:c_ctime"`
	UpdatedAt  time.Time    `gorm:"column:c_mtime"`
}

func (r definitionRow) definition() Definition {
	return Definition{StrategyID: r.StrategyID, Name: r.Name, DSLYaml: r.DSLYaml, DSLHash: r.DSLHash, DeletedAt: nullableTime(r.DeletedAt), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

// 策略定义的 SQL：整条写成常量，不在调用处拼接；读定义的语句列清单与 definitionRow 一一对应。
const (
	// sqlInsertDefinitionVersion 登记一个 DSL 版本，已存在则忽略。
	sqlInsertDefinitionVersion = `INSERT OR IGNORE INTO t_strategy_def_versions (c_dsl_hash, c_dsl_yaml, c_ctime) VALUES (?, ?, ?)`
	// sqlInsertDefinition 新建未删除的定义。
	sqlInsertDefinition = `
		INSERT INTO t_strategy_defs (c_strategy_id, c_name, c_dsl_yaml, c_dsl_hash, c_deleted_at, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, NULL, ?, ?)`
	// sqlSelectDefinitionByID 按策略 ID 读取定义（包括已软删除的）。
	sqlSelectDefinitionByID = `SELECT c_strategy_id, c_name, c_dsl_yaml, c_dsl_hash, c_deleted_at, c_ctime, c_mtime FROM t_strategy_defs WHERE c_strategy_id = ?`
	// sqlCountActiveInstancesOfDefinition 统计引用该定义、已启用或挂着会话的实例。
	sqlCountActiveInstancesOfDefinition = `SELECT COUNT(*) FROM t_strategy_instances WHERE c_strategy_id = ? AND (c_enabled = 1 OR c_session_id IS NOT NULL)`
	// sqlUpdateDefinition 替换定义的名称与 DSL。
	sqlUpdateDefinition = `UPDATE t_strategy_defs SET c_name = ?, c_dsl_yaml = ?, c_dsl_hash = ?, c_mtime = ? WHERE c_strategy_id = ?`
	// sqlListLiveDefinitions 按名称列出未删除的定义。
	sqlListLiveDefinitions = `SELECT c_strategy_id, c_name, c_dsl_yaml, c_dsl_hash, c_deleted_at, c_ctime, c_mtime FROM t_strategy_defs WHERE c_deleted_at IS NULL ORDER BY c_name, c_strategy_id`
	// sqlCountInstancesOfDefinition 统计引用该定义的全部实例。
	sqlCountInstancesOfDefinition = `SELECT COUNT(*) FROM t_strategy_instances WHERE c_strategy_id = ?`
	// sqlSoftDeleteDefinition 软删除未删除的定义。
	sqlSoftDeleteDefinition = `UPDATE t_strategy_defs SET c_deleted_at = ?, c_mtime = ? WHERE c_strategy_id = ? AND c_deleted_at IS NULL`
	// sqlSelectDefinitionVersion 按内容哈希读取 DSL 版本。
	sqlSelectDefinitionVersion = `SELECT c_dsl_hash, c_dsl_yaml, c_ctime FROM t_strategy_def_versions WHERE c_dsl_hash = ?`
)

func validateDefinition(def Definition) error {
	if strings.TrimSpace(def.StrategyID) == "" || strings.TrimSpace(def.Name) == "" || strings.TrimSpace(def.DSLYaml) == "" || strings.TrimSpace(def.DSLHash) == "" {
		return errors.New("策略定义缺少 strategy_id、name、dsl_yaml 或 dsl_hash")
	}
	return nil
}

func ensureDefinitionVersion(tx *gorm.DB, hash, yaml string, at time.Time) error {
	return tx.Exec(sqlInsertDefinitionVersion, hash, yaml, at.UTC()).Error
}

// CreateDefinition 新建定义并登记其 DSL 版本。
func (s *Store) CreateDefinition(ctx context.Context, def Definition) error {
	if err := validateDefinition(def); err != nil {
		return err
	}
	now, err := requireTime(def.CreatedAt)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := ensureDefinitionVersion(tx, def.DSLHash, def.DSLYaml, now); err != nil {
			return err
		}
		return tx.Exec(sqlInsertDefinition, def.StrategyID, def.Name, def.DSLYaml, def.DSLHash, now, now).Error
	})
}

// UpdateDefinition 替换 DSL 文本；只允许在没有启用实例引用时修改，并登记新版本。
func (s *Store) UpdateDefinition(ctx context.Context, def Definition) error {
	if err := validateDefinition(def); err != nil {
		return err
	}
	now, err := requireTime(def.UpdatedAt)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var current definitionRow
		if err := tx.Raw(sqlSelectDefinitionByID, def.StrategyID).Scan(&current).Error; err != nil {
			return err
		}
		if current.StrategyID == "" || current.DeletedAt.Valid {
			return ErrNotFound
		}
		var active int64
		if err := tx.Raw(sqlCountActiveInstancesOfDefinition, def.StrategyID).Scan(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errors.New("策略定义正被启用中的实例引用，请先停用这些实例")
		}
		if err := ensureDefinitionVersion(tx, def.DSLHash, def.DSLYaml, now); err != nil {
			return err
		}
		return tx.Exec(sqlUpdateDefinition, def.Name, def.DSLYaml, def.DSLHash, now, def.StrategyID).Error
	})
}

// GetDefinition 读取定义，包括已软删除的（DeletedAt 非空），供历史页面还原。
func (s *Store) GetDefinition(ctx context.Context, strategyID string) (Definition, error) {
	var row definitionRow
	if err := s.db.WithContext(ctx).Raw(sqlSelectDefinitionByID, strategyID).Scan(&row).Error; err != nil {
		return Definition{}, err
	}
	if row.StrategyID == "" {
		return Definition{}, ErrNotFound
	}
	return row.definition(), nil
}

// ListDefinitions 列出未删除的定义。
func (s *Store) ListDefinitions(ctx context.Context) ([]Definition, error) {
	var rows []definitionRow
	if err := s.db.WithContext(ctx).Raw(sqlListLiveDefinitions).Scan(&rows).Error; err != nil {
		return nil, err
	}
	defs := make([]Definition, 0, len(rows))
	for _, row := range rows {
		defs = append(defs, row.definition())
	}
	return defs, nil
}

// SoftDeleteDefinition 软删除定义；仍被实例引用时拒绝（实例不可删除，被引用的定义因此也不可删除）。
func (s *Store) SoftDeleteDefinition(ctx context.Context, strategyID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var references int64
		if err := tx.Raw(sqlCountInstancesOfDefinition, strategyID).Scan(&references).Error; err != nil {
			return err
		}
		if references > 0 {
			return fmt.Errorf("策略定义仍被 %d 个实例引用，不能删除", references)
		}
		result := tx.Exec(sqlSoftDeleteDefinition, now, now, strategyID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		return nil
	})
}

// GetDefinitionVersion 按内容哈希读取 DSL 文本。
func (s *Store) GetDefinitionVersion(ctx context.Context, dslHash string) (DefinitionVersion, error) {
	var row struct {
		DSLHash   string    `gorm:"column:c_dsl_hash"`
		DSLYaml   string    `gorm:"column:c_dsl_yaml"`
		CreatedAt time.Time `gorm:"column:c_ctime"`
	}
	if err := s.db.WithContext(ctx).Raw(sqlSelectDefinitionVersion, dslHash).Scan(&row).Error; err != nil {
		return DefinitionVersion{}, err
	}
	if row.DSLHash == "" {
		return DefinitionVersion{}, ErrNotFound
	}
	return DefinitionVersion{DSLHash: row.DSLHash, DSLYaml: row.DSLYaml, CreatedAt: row.CreatedAt.UTC()}, nil
}
