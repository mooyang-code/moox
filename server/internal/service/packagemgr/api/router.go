package api

import (
	"github.com/gin-gonic/gin"
	asynctasklogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/logic"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterPackageManagerRoutes 注册包管理相关路由
func RegisterPackageManagerRoutes(router *gin.RouterGroup, db *gorm.DB, cosProvider provider.ClientWithCOS, cosBucket string, asyncTaskService asynctasklogic.AsyncTaskService) {
	// 创建包管理服务
	packageService := logic.NewFunctionPackageService(db, cosProvider, cosBucket)
	
	// 设置异步任务服务
	packageService.SetAsyncTaskService(asyncTaskService)
	
	// 创建包管理处理器
	packageHandler := NewFunctionPackageHandler(packageService)
	
	// 注册包管理路由组
	packageGroup := router.Group("/function-packages")
	{
		packageGroup.POST("/upload", packageHandler.UploadPackageAsync)
		packageGroup.GET("/upload-task/:task_id/status", packageHandler.GetUploadTaskStatus)
		packageGroup.GET("", packageHandler.GetPackageList)
		packageGroup.GET("/:id", packageHandler.GetPackageDetail)
		packageGroup.DELETE("/:id", packageHandler.DeletePackage)
		packageGroup.GET("/:id/download-url", packageHandler.GetPackageDownloadURL)
		packageGroup.GET("/:id/download-local", packageHandler.DownloadLocalPackage)
		packageGroup.GET("/options", packageHandler.GetPackageOptions)
	}

	log.Info("[Package Manager] 路由注册完成")
}