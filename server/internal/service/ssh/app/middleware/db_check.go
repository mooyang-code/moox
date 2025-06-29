package middleware

import (
	"github.com/mooyang-code/moox/server/internal/service/ssh/app/config"
	"github.com/mooyang-code/moox/server/internal/service/ssh/app/model"
	"github.com/gin-gonic/gin"
)

func DbCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.DefaultConfig.IsInit {
			c.Next()
			return
		}
		if model.Db != nil {
			tx := model.Db.Exec("select now()")
			if tx.Error == nil {
				c.Next()
			} else {
				err := model.DbMigrate(config.DefaultConfig.DbType, config.DefaultConfig.DbDsn)
				if err != nil {
					c.Abort()
					c.JSON(500, gin.H{"code": 500, "msg": "数据库连接错误:" + err.Error()})
					return
				}
			}
		}
		c.Next()
	}
}
