package fileserver

import (
	"fmt"
	"os"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// Server 文件服务器
type Server struct {
	config Config
	engine *gin.Engine
}

// NewServer 创建文件服务器实例
func NewServer(config Config) *Server {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.Default()

	return &Server{
		config: config,
		engine: engine,
	}
}

// Start 启动文件服务器
func (s *Server) Start() error {
	// 检查包目录是否存在
	if _, err := os.Stat(s.config.PackageDir); os.IsNotExist(err) {
		log.Warnf("[FileServer] 包目录不存在，正在创建: %s", s.config.PackageDir)
		if err := os.MkdirAll(s.config.PackageDir, 0755); err != nil {
			return fmt.Errorf("创建包目录失败: %v", err)
		}
	}

	// 设置路由
	s.setupRoutes()

	// 启动服务
	address := fmt.Sprintf("%s:%s", s.config.Address, s.config.Port)
	log.Infof("[FileServer] 启动文件下载服务，地址: %s", address)
	return s.engine.Run(address)
}

// StartFileDownloadService 启动文件下载服务（独立goroutine）
func StartFileDownloadService() {
	go func() {
		log.Info("[FileServer] 正在启动文件下载服务...")
		server := NewServer(DefaultConfig)
		if err := server.Start(); err != nil {
			log.Errorf("[FileServer] 文件下载服务启动失败: %v", err)
		}
	}()
}
