package api

import (
	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterDNSProxyRoutes 注册DNS代理相关路由
func RegisterDNSProxyRoutes(router *gin.RouterGroup) {
	// DNS解析记录路由
	dnsRecordHandler := NewDNSRecordHandler()
	dnsRecordGroup := router.Group("/dns-record")
	{
		dnsRecordGroup.GET("/list", dnsRecordHandler.GetDNSRecordList)
		dnsRecordGroup.GET("/:domain", dnsRecordHandler.GetDNSRecordDetail)
	}

	log.Info("[DNSProxy] DNS解析记录路由注册完成")
}
