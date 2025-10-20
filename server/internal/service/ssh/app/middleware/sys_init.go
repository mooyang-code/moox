package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/service/ssh/app/config"
)

func SysInit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.DefaultConfig.IsInit {
			// 需要进行系统初始化
			c.Abort()
			c.JSON(401, gin.H{"code": 401, "msg": "请对系统进行初始化"})
			return
		}
		c.Next()
	}
}
