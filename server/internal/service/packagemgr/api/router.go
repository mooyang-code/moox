package api

import (
	asynctask "github.com/mooyang-code/moox/server/internal/service/asynctask"
	authutils "github.com/mooyang-code/moox/server/internal/service/auth/utils"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/logic"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterPackageManagerRoutes 注册包管理相关路由
// COS客户端不再在初始化时创建，而是在异步上传任务执行时动态获取
func RegisterPackageManagerRoutes(router *gin.RouterGroup, db *gorm.DB, asyncTaskService asynctask.Service) {
	// 创建包管理服务（不再需要预先传入COS客户端）
	packageDAO := dao.NewFunctionPackageDAO(db)
	packageService := logic.NewFunctionPackageService(packageDAO)

	// 设置异步任务服务
	packageService.SetAsyncTaskService(asyncTaskService)

	// 创建包管理处理器
	packageHandler := NewFunctionPackageHandler(packageService)

	// 注册包管理路由组
	packageGroup := router.Group("/function-packages")
	packageGroup.Use(authutils.ExtractUserMiddleware()) // 提取用户信息中间件
	{
		packageGroup.POST("/upload", packageHandler.UploadPackageAsync)
		packageGroup.GET("/upload-task/:task_id/status", packageHandler.GetUploadTaskStatus)
		packageGroup.GET("", packageHandler.GetPackageList)
		packageGroup.GET("/:id", packageHandler.GetPackageDetail)
		packageGroup.DELETE("/:id", packageHandler.DeletePackage)
		packageGroup.GET("/:id/download-url", packageHandler.GetPackageDownloadURL)
		packageGroup.GET("/options", packageHandler.GetPackageOptions)
	}

	log.Info("[Package Manager] 路由注册完成")
}
