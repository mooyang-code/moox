package model

import (
	"errors"
	"github.com/mooyang-code/moox/server/internal/service/ssh/app/config"
	"github.com/mooyang-code/moox/server/internal/service/ssh/gorm"
	"github.com/mooyang-code/moox/server/internal/service/ssh/gorm/driver/mysql"
	"github.com/mooyang-code/moox/server/internal/service/ssh/gorm/driver/pgsql"
	"github.com/mooyang-code/moox/server/internal/service/ssh/gorm/logger"
	_ "github.com/mooyang-code/moox/server/internal/service/ssh/mysql"
	_ "github.com/mooyang-code/moox/server/internal/service/ssh/pgsql"
	"log/slog"
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

	err := Db.AutoMigrate(SshConf{}, SshUser{}, CmdNote{}, NetFilter{}, PolicyConf{}, LoginAudit{})
	if err != nil {
		slog.Error("AutoMigrate error:", "err_msg", err.Error())
		return err
	}

	return nil
}
