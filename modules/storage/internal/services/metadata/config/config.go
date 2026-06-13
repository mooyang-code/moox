// Package config 提供元数据服务的配置管理功能，支持元数据数据库配置的加载和解析
package config

import (
	"os"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/config"
)

// Config 元数据服务配置
type Config struct {
	MetadataDatabase struct {
		StorageDevice string `yaml:"storage_device"`
	} `yaml:"metadata_database"`
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	// 优先使用环境变量指定的配置路径
	configPath := os.Getenv("STORAGE_CONFIG_PATH")
	if configPath == "" {
		configPath = "./config"
	}

	loader := config.NewConfigLoader(configPath)
	var cfg Config

	err := loader.LoadConfig("metadata.yaml", &cfg)
	if err != nil {
		// 如果失败，尝试直接从当前目录加载
		loader = config.NewConfigLoader(".")
		err = loader.LoadConfig("metadata.yaml", &cfg)
		if err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}
