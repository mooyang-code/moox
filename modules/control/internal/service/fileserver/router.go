package fileserver

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// setupRoutes 设置路由
func (s *Server) setupRoutes() {
	// 添加CORS中间件（放在最前面，确保所有请求都支持跨域）
	s.engine.Use(s.corsMiddleware())

	// 添加安全中间件
	s.engine.Use(s.securityMiddleware())

	// 健康检查
	s.engine.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "MooX File Download Service",
			"version": "1.0.0",
			"status":  "running",
		})
	})

	// 文件下载服务 - 添加JWT验证中间件
	s.engine.GET("/files/*filepath", s.jwtAuthMiddleware(), s.fileDownloadHandler())
	log.Infof("[FileServer] 文件服务器设置完成，包目录: %s", s.config.PackageDir)
}
