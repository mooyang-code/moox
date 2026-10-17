package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

// LibraryDefinition 是因子库里的一个表达式定义。
type LibraryDefinition struct {
	Name string
	Spec string
}

// MigrateResult 是一次迁移的结果。
type MigrateResult struct {
	AlreadyMigrated bool
	Migrated        []string
	// Backup 是迁移前备份的文件；没有备份时为空。
	Backup string
}

// MigrateExprWithBackup 先确认需要迁移，再把数据库备份到 backup（为空则不备份），然后执行 MigrateExpr。
// 已经是新结构时不备份、什么也不做。
func MigrateExprWithBackup(ctx context.Context, dbPath, backup string, library map[string]LibraryDefinition) (MigrateResult, error) {
	migrated, err := isExprSchema(ctx, dbPath)
	if err != nil {
		return MigrateResult{}, err
	}
	if migrated {
		return MigrateResult{AlreadyMigrated: true}, nil
	}
	if backup != "" {
		if err := BackupDatabase(ctx, dbPath, backup); err != nil {
			return MigrateResult{}, err
		}
	}
	result, err := MigrateExpr(ctx, dbPath, library)
	result.Backup = backup
	return result, err
}

// isExprSchema 报告定义表是否已经是表达式结构。
func isExprSchema(ctx context.Context, dbPath string) (bool, error) {
	db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		return false, fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return false, err
	}
	defer sqlDB.Close()
	var columns []string
	if err := db.WithContext(ctx).Raw("SELECT name FROM pragma_table_info('t_factor_defs') ORDER BY cid").Scan(&columns).Error; err != nil {
		return false, fmt.Errorf("inspect t_factor_defs: %w", err)
	}
	if len(columns) == 0 {
		return false, errors.New("数据库里没有 t_factor_defs 表")
	}
	return contains(columns, "c_spec"), nil
}

// MigrateExpr 把定义表从 Python 源码的旧结构迁移为表达式结构（docs/计划/因子表达式引擎详细设计.md 13.2）。
// 必须在 factor-mgr 停止时运行，调用方负责先备份数据库文件。按 SQLite 官方的“重建表”步骤：事务之外关闭外键，
// 事务内建新表、逐行按 factor_id 找因子库里的定义并编译（输出列与回看必须与旧行一致）、删旧表、改名，
// 提交前用 foreign_key_check 确认成员表的外键完好；任何一行不满足就回滚，原库不变。已经是新结构时什么也不做。
func MigrateExpr(ctx context.Context, dbPath string, library map[string]LibraryDefinition) (MigrateResult, error) {
	db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		return MigrateResult{}, fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return MigrateResult{}, err
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1) // PRAGMA foreign_keys 是连接级设置，固定在一个连接上执行
	var columns []string
	if err := db.WithContext(ctx).Raw("SELECT name FROM pragma_table_info('t_factor_defs') ORDER BY cid").Scan(&columns).Error; err != nil {
		return MigrateResult{}, fmt.Errorf("inspect t_factor_defs: %w", err)
	}
	if len(columns) == 0 {
		return MigrateResult{}, errors.New("数据库里没有 t_factor_defs 表")
	}
	if contains(columns, "c_spec") {
		return MigrateResult{AlreadyMigrated: true}, nil
	}
	if !contains(columns, "c_source_code") {
		return MigrateResult{}, fmt.Errorf("t_factor_defs 的结构无法识别：%s", strings.Join(columns, ","))
	}
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		return MigrateResult{}, err
	}
	defer db.Exec("PRAGMA foreign_keys = ON")
	result := MigrateResult{}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		type oldRow struct {
			FactorID             string    `gorm:"column:c_factor_id"`
			Name                 string    `gorm:"column:c_name"`
			OutputsJSON          string    `gorm:"column:c_outputs_json"`
			LookbackPeriods      int       `gorm:"column:c_lookback_periods"`
			AllowPartialUniverse int       `gorm:"column:c_allow_partial_universe"`
			CreatedAt            time.Time `gorm:"column:c_ctime"`
		}
		var rows []oldRow
		if err := tx.Raw(`SELECT c_factor_id, c_name, c_outputs_json, c_lookback_periods, c_allow_partial_universe, c_ctime
			FROM t_factor_defs ORDER BY c_factor_id`).Scan(&rows).Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE TABLE t_factor_defs_v2 (
    c_factor_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_spec TEXT NOT NULL,
    c_spec_hash TEXT NOT NULL,
    c_allow_partial_universe INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_allow_partial_universe IN (0, 1))
)`).Error; err != nil {
			return err
		}
		for _, row := range rows {
			entry, ok := library[row.FactorID]
			if !ok {
				return fmt.Errorf("因子库里没有 %s 的表达式定义", row.FactorID)
			}
			name := entry.Name
			if name == "" {
				name = row.Name
			}
			def, err := domain.NormalizeFactorDefinition(domain.FactorDef{
				FactorID: row.FactorID, Name: name, Spec: entry.Spec, AllowPartialUniverse: row.AllowPartialUniverse != 0,
			})
			if err != nil {
				return fmt.Errorf("因子 %s：%w", row.FactorID, err)
			}
			var oldOutputs []string
			if err := json.Unmarshal([]byte(row.OutputsJSON), &oldOutputs); err != nil {
				return fmt.Errorf("因子 %s 的旧输出列无法解析：%w", row.FactorID, err)
			}
			if strings.Join(def.Outputs, ",") != strings.Join(oldOutputs, ",") {
				return fmt.Errorf("因子 %s 的输出列不一致：新 %v，旧 %v", row.FactorID, def.Outputs, oldOutputs)
			}
			if def.LookbackPeriods != row.LookbackPeriods {
				return fmt.Errorf("因子 %s 的回看不一致：新 %d，旧 %d", row.FactorID, def.LookbackPeriods, row.LookbackPeriods)
			}
			if err := tx.Exec(`INSERT INTO t_factor_defs_v2
				(c_factor_id, c_name, c_spec, c_spec_hash, c_allow_partial_universe, c_ctime, c_mtime) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				def.FactorID, def.Name, def.Spec, def.SpecHash, boolInt(def.AllowPartialUniverse), row.CreatedAt, time.Now().UTC()).Error; err != nil {
				return err
			}
			result.Migrated = append(result.Migrated, def.FactorID)
		}
		if err := tx.Exec("DROP TABLE t_factor_defs").Error; err != nil {
			return err
		}
		if err := tx.Exec("ALTER TABLE t_factor_defs_v2 RENAME TO t_factor_defs").Error; err != nil {
			return err
		}
		// DROP TABLE 会一并删除旧表上的触发器，按 schema/factor.sql 重建。
		if err := tx.Exec(`CREATE TRIGGER IF NOT EXISTS trg_t_factor_defs_mtime
AFTER UPDATE ON t_factor_defs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_defs SET c_mtime = CURRENT_TIMESTAMP WHERE c_factor_id = OLD.c_factor_id;
END`).Error; err != nil {
			return err
		}
		rowsFK, err := tx.Raw("PRAGMA foreign_key_check").Rows()
		if err != nil {
			return err
		}
		defer rowsFK.Close()
		if rowsFK.Next() {
			var table string
			var rowid sql.NullInt64
			var parent string
			var fkid sql.NullInt64
			_ = rowsFK.Scan(&table, &rowid, &parent, &fkid)
			return fmt.Errorf("迁移后外键检查失败：表 %s 引用 %s", table, parent)
		}
		return rowsFK.Err()
	})
	if err != nil {
		return MigrateResult{}, err
	}
	return result, nil
}

// BackupDatabase 用 VACUUM INTO 把数据库（含尚未检查点的 WAL 内容）复制成一个一致的新文件；目标已存在时报错。
func BackupDatabase(ctx context.Context, dbPath, dest string) error {
	db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if err := db.WithContext(ctx).Exec("VACUUM INTO ?", dest).Error; err != nil {
		return fmt.Errorf("备份数据库到 %s：%w", dest, err)
	}
	return nil
}
