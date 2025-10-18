package api

import (
	"net/http"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RegisterCloudNodeRoutes 注册云节点相关路由
func RegisterCloudNodeRoutes(router *gin.RouterGroup, db *gorm.DB) {
	// 云节点路由
	nodeHandler := NewCloudNodeHandler(db)
	nodeGroup := router.Group("/cloud_node")
	{
		nodeGroup.GET("/list", nodeHandler.GetNodeList)
		nodeGroup.GET("/detail", nodeHandler.GetNodeDetail)
		nodeGroup.POST("/register", nodeHandler.RegisterNode)
		nodeGroup.PUT("/update", nodeHandler.UpdateNode)
		nodeGroup.DELETE("/remove", nodeHandler.RemoveNode)
		nodeGroup.POST("/heartbeat", nodeHandler.Heartbeat)
		nodeGroup.PUT("/update_load", nodeHandler.UpdateNodeLoad)
		nodeGroup.PUT("/update_function", nodeHandler.UpdateNodeFunction)
	}

	// 云账户路由
	accountHandler := NewCloudAccountHandler(db)
	accountGroup := router.Group("/cloud_account")
	{
		accountGroup.GET("/list", accountHandler.GetCloudAccountList)
		accountGroup.GET("/detail", accountHandler.GetCloudAccountDetail)
		accountGroup.POST("/create", accountHandler.CreateCloudAccount)
		accountGroup.PUT("/update", accountHandler.UpdateCloudAccount)
		accountGroup.DELETE("/delete", accountHandler.DeleteCloudAccount)
	}
}

// RegisterCloudNodeHTTPRoutes 注册需要特殊处理的HTTP路由
func RegisterCloudNodeHTTPRoutes(mux *http.ServeMux, db *gorm.DB, queueManager *queue.Manager) {
	// 文件上传路由
	scfNodeService := logic.NewSCFNodeServiceWithQueue(db, queueManager)
	fileUploadHandler := NewFileUploadHandler(scfNodeService)
	mux.HandleFunc("/api/v1/cloud-function/upload", fileUploadHandler.HandleFunctionUpload)

	// 云函数调用路由
	cloudFunctionInvokeService := NewCloudFunctionInvokeService(db)
	cloudFunctionInvokeService.RegisterCloudFunctionInvokeRoutes(mux)
}
