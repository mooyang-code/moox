// Package config 提供统一的配置管理
package config

import (
	"fmt"
	"log"
	"sync"

	"trpc.group/trpc-go/trpc-go"
)

// ServiceConfig 服务配置【读取trpc_go中的一些配置】
type ServiceConfig struct {
	GatewayPort int `yaml:"gateway_port"` // HTTP网关服务端口号
}

// 全局服务配置实例
var (
	globalServiceConfig *ServiceConfig
	serviceConfigMutex  sync.RWMutex
)

// LoadServiceConfig 从trpc配置中加载服务配置
func LoadServiceConfig() (*ServiceConfig, error) {
	cfg := trpc.GlobalConfig()

	if cfg == nil || cfg.Server.Service == nil || len(cfg.Server.Service) == 0 {
		log.Fatal("server config or service list is nil or empty")
	}

	targetServiceName := "trpc.moox.gateway.stdhttp"
	var targetPort int

	for _, svc := range cfg.Server.Service {
		if svc.Name == targetServiceName {
			targetPort = int(svc.Port)
			log.Printf("Service [%s] found, port: %d", targetServiceName, targetPort)
			break
		}
	}

	if targetPort == 0 {
		log.Printf("Service [%s] not found in configuration", targetServiceName)
		return nil, fmt.Errorf("service %s not found in configuration", targetServiceName)
	}

	return &ServiceConfig{
		GatewayPort: targetPort,
	}, nil
}

// SetGlobalServiceConfig 设置全局服务配置
func SetGlobalServiceConfig(cfg *ServiceConfig) {
	serviceConfigMutex.Lock()
	defer serviceConfigMutex.Unlock()
	globalServiceConfig = cfg
}

// GetGlobalServiceConfig 获取全局服务配置
func GetGlobalServiceConfig() *ServiceConfig {
	serviceConfigMutex.RLock()
	defer serviceConfigMutex.RUnlock()
	return globalServiceConfig
}

// GetGatewayPort 获取HTTP网关服务端口号
func GetGatewayPort() int {
	cfg := GetGlobalServiceConfig()
	if cfg == nil {
		return 0
	}
	return cfg.GatewayPort
}
