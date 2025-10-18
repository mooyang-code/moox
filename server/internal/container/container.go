// Package container 提供简单的依赖注入容器
package container

import (
	"fmt"
	"sync"
)

// Container 依赖注入容器
type Container struct {
	services map[string]interface{}
	mu       sync.RWMutex
}

// New 创建新的容器
func New() *Container {
	return &Container{
		services: make(map[string]interface{}),
	}
}

// Register 注册服务
func (c *Container) Register(name string, service interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services[name] = service
}

// Get 获取服务
func (c *Container) Get(name string) (interface{}, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	service, ok := c.services[name]
	if !ok {
		return nil, fmt.Errorf("service not found: %s", name)
	}
	return service, nil
}

// MustGet 获取服务（如果不存在则panic）
func (c *Container) MustGet(name string) interface{} {
	service, err := c.Get(name)
	if err != nil {
		panic(err)
	}
	return service
}

// Has 检查服务是否存在
func (c *Container) Has(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.services[name]
	return ok
}

// Remove 移除服务
func (c *Container) Remove(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.services, name)
}

// Clear 清空所有服务
func (c *Container) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services = make(map[string]interface{})
}

// Services 获取所有服务名称
func (c *Container) Services() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := make([]string, 0, len(c.services))
	for name := range c.services {
		names = append(names, name)
	}
	return names
}

// 全局默认容器
var defaultContainer = New()

// Register 注册到默认容器
func Register(name string, service interface{}) {
	defaultContainer.Register(name, service)
}

// Get 从默认容器获取
func Get(name string) (interface{}, error) {
	return defaultContainer.Get(name)
}

// MustGet 从默认容器获取（不存在则panic）
func MustGet(name string) interface{} {
	return defaultContainer.MustGet(name)
}

// Has 检查默认容器中是否存在
func Has(name string) bool {
	return defaultContainer.Has(name)
}

// 服务名称常量（避免拼写错误）
const (
	ServiceDB               = "db"
	ServiceConfig           = "config"
	ServiceLogger           = "logger"
	ServiceAuthService      = "authService"
	ServiceAsyncTaskService = "asyncTaskService"
	ServiceCloudNodeService = "cloudNodeService"
	ServicePackageService   = "packageService"
	ServiceQueueManager     = "queueManager"
	ServiceHeartbeatManager = "heartbeatManager"
)
