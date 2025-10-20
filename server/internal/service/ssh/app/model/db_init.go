package model

import (
	"errors"
	"log/slog"

	"github.com/mooyang-code/moox/server/internal/service/ssh/app/config"
	"github.com/mooyang-code/moox/server/internal/service/ssh/gorm"
	"github.com/mooyang-code/moox/server/internal/service/ssh/gorm/driver/mysql"
	"github.com/mooyang-code/moox/server/internal/service/ssh/gorm/driver/pgsql"
	"github.com/mooyang-code/moox/server/internal/service/ssh/gorm/logger"
	_ "github.com/mooyang-code/moox/server/internal/service/ssh/mysql"
	_ "github.com/mooyang-code/moox/server/internal/service/ssh/pgsql"
)

var Db *gorm.DB

func init() {
	if !config.DefaultConfig.IsInit {
		slog.Warn("系统未初始化,跳过DbMigrate")
		return
	}
	err := DbMigrate(config.DefaultConfig.DbType, config.DefaultConfig.DbDsn)
	if err != nil {
		slog.Error("DbMigrate error", "err_msg", err.Error())
	}
}

func DbMigrate(dbType, dsn string) error {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("DbMigrate error", "err_msg", err)
		}
	}()
	if dbType == "pgsql" {
		db, err := gorm.Open(pgsql.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Warn),
		})
		if err != nil {
			return err
		}
		err = db.Exec("select 1=1;").Error
		if err != nil {
			return err
		}
		Db = db
	}

	if dbType == "mysql" {
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Warn),
		})
		if err != nil {
			return err
		}
		err = db.Exec("select 1=1;").Error
		if err != nil {
			return err
		}
		Db = db
	}

	if Db == nil {
		return errors.New("请检查数据库链接")
	}

	// 自动迁移表结构
	// 作用：根据 struct 定义自动创建或更新数据库表
	// - 首次运行时自动创建 SSH 相关的表（t_ssh_conf, t_ssh_user, t_cmd_note 等）
	// - 代码更新添加新字段时自动添加列到表中
	// - 不是数据迁移，而是表结构（schema）的自动管理
	err := Db.AutoMigrate(SshConf{}, SshUser{}, CmdNote{}, NetFilter{}, PolicyConf{}, LoginAudit{})
	if err != nil {
		slog.Error("AutoMigrate error:", "err_msg", err.Error())
		return err
	}

	return nil
}
