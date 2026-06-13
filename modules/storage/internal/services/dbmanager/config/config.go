// Package config 提供admin服务的配置管理功能
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// Config admin服务配置
type Config struct {
	// 缓存配置
	Cache CacheConfig `yaml:"cache"`

	// Adapter服务配置
	Adapter AdapterConfig `yaml:"adapter"`
}

// CacheConfig 缓存配置
type CacheConfig struct {
	// 表缓存过期时间（秒）
	TableCacheExpire int `yaml:"table_cache_expire"`
}

// AdapterConfig Adapter服务配置
type AdapterConfig struct {
	// Adapter服务名称
	ServiceName string `yaml:"service_name"`
	// 默认实体ID（用于测试和简单场景）
	DefaultEntityID uint32 `yaml:"default_entity_id"`
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	// 优先使用环境变量指定的配置路径
	basePath := os.Getenv("STORAGE_CONFIG_PATH")
	if basePath == "" {
		basePath = "./config"
	}

	// 读取配置文件
	configPath := filepath.Join(basePath, "dbmanager.yaml")
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		// 如果失败，尝试直接从当前目录加载
		configPath = "dbmanager.yaml"
		yamlFile, err = os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("读取配置文件失败: %+v", err)
		}
	}

	// 解析YAML到Config结构
	var config Config
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, fmt.Errorf("解析YAML失败: %+v", err)
	}

	// 设置默认值
	if config.Cache.TableCacheExpire == 0 {
		config.Cache.TableCacheExpire = 3600 // 1小时
	}
	if config.Adapter.ServiceName == "" {
		config.Adapter.ServiceName = "trpc.storage.adapter.Adapter"
	}
	if config.Adapter.DefaultEntityID == 0 {
		config.Adapter.DefaultEntityID = 1 // 默认实体ID
	}
	return &config, nil
}
