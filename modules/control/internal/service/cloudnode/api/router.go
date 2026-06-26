package api

import (
	"github.com/gin-gonic/gin"

	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask"
	authutils "github.com/mooyang-code/moox/modules/control/internal/service/auth/utils"
	cloudnodemgr "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode"
)

// RegisterCloudNodeRoutes 注册云节点相关路由（包括代码包管理）
func RegisterCloudNodeRoutes(router *gin.RouterGroup, service cloudnodemgr.Service, asyncService asynctask.Service) {
	// 云节点路由
	nodeHandler := NewCloudNodeHandlerWithService(service)
	nodeGroup := router.Group("/cloud_node")
	{
		nodeGroup.POST("/list", nodeHandler.GetNodeList) // 改为POST以支持JSON body参数
		nodeGroup.GET("/detail", nodeHandler.GetNodeDetail)
		nodeGroup.PUT("/update", nodeHandler.UpdateNode)
		nodeGroup.POST("/invoke", nodeHandler.InvokeFunction)
	}

	// 批量操作路由
	batchHandler := NewBatchOperationHandler(asyncService)
	batchGroup := router.Group("/cloud_node/batch")
	{
		batchGroup.POST("/create", batchHandler.BatchCreateNodes)
		batchGroup.POST("/delete", batchHandler.BatchDeleteNodes)
		batchGroup.POST("/deploy", batchHandler.BatchDeployNodes)
	}

	// 云账户路由
	accountHandler := NewCloudAccountHandler(service)
	accountGroup := router.Group("/cloud_account")
	{
		accountGroup.GET("/list", accountHandler.GetCloudAccountList)
		accountGroup.GET("/detail", accountHandler.GetCloudAccountDetail)
		accountGroup.POST("/create", accountHandler.CreateCloudAccount)
		accountGroup.PUT("/update", accountHandler.UpdateCloudAccount)
		accountGroup.DELETE("/delete", accountHandler.DeleteCloudAccount)
	}

	// 云地区路由
	regionHandler := NewCloudRegionHandler()
	regionGroup := router.Group("/cloud_region")
	{
		regionGroup.GET("/list", regionHandler.GetRegionList)
	}
}

// RegisterPackageManagerRoutes 注册包管理相关路由
func RegisterPackageManagerRoutes(router *gin.RouterGroup, service cloudnodemgr.Service) {
	// 创建包管理处理器
	packageHandler := NewFunctionPackageHandler(service)

	// 注册包管理路由组
	packageGroup := router.Group("/function-packages")
	packageGroup.Use(authutils.ExtractUserMiddleware()) // 提取用户信息中间件
	{
		packageGroup.GET("", packageHandler.GetPackageList)
		packageGroup.GET("/:package_id", packageHandler.GetPackageDetail)
		packageGroup.DELETE("/:package_id", packageHandler.DeletePackage)
		packageGroup.GET("/:package_id/download-url", packageHandler.GetPackageDownloadURL)
		packageGroup.GET("/options", packageHandler.GetPackageOptions)
		packageGroup.POST("/upload", packageHandler.UploadPackage) // 添加文件上传路由
	}
}

// RegisterHeartbeatRoutes 注册心跳服务的HTTP路由
func RegisterHeartbeatRoutes(r *gin.RouterGroup, heartbeatService cloudnodemgr.HeartbeatService) {
	// 创建处理器
	heartbeatHandler := NewHeartbeatHandler(heartbeatService)

	// 心跳管理路由组
	heartbeatGroup := r.Group("/heartbeat")
	{
		// 心跳上报
		heartbeatGroup.POST("/report", heartbeatHandler.ReportHeartbeat)
	}
}
