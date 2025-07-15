package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// MessageConfig 消息服务配置
type MessageConfig struct {
	Server      string `yaml:"server"`        // 消息服务器连接信息，例如: nats:localhost:4222
	Stream      string `yaml:"stream"`        // 流名称
	Consumer    string `yaml:"consumer"`      // 消费者名称
	Subject     string `yaml:"subject"`       // 主题
	MaxWaitTime int    `yaml:"max_wait_time"` // 最大等待时间(毫秒)
}

// MooxConfig moox服务配置
type MooxConfig struct {
	AuthTarget string `yaml:"auth_target"` // 认证服务地址
}

// Config 元数据服务配置
type Config struct {
	MetadataDatabase struct {
		StorageDevice string `yaml:"storage_device"`
	} `yaml:"metadata_database"`

	Storage struct {
		Target string `yaml:"target"`
	} `yaml:"storage"`

	Moox *MooxConfig `yaml:"moox"` // moox服务配置

	Message *MessageConfig `yaml:"message"` // 消息服务配置
}

// getConfigPaths 获取可能的配置文件路径列表
func getConfigPaths() []string {
	paths := []string{
		// 当前目录
		"./config/cli.yaml",
		"./cli.yaml",
		// 相对路径（用于构建后的二进制）
		"../config/cli.yaml",
		// 系统配置目录
		"/etc/moox/cli.yaml",
		// 用户家目录
		filepath.Join(os.Getenv("HOME"), ".moox", "cli.yaml"),
	}

	// 添加环境变量指定的配置文件
	if configPath := os.Getenv("MOOX_CONFIG"); configPath != "" {
		paths = append([]string{configPath}, paths...)
	}

	return paths
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	var config Config
	var lastErr error

	// 尝试从多个可能的路径加载配置文件
	for _, configPath := range getConfigPaths() {
		yamlFile, err := os.ReadFile(configPath)
		if err != nil {
			lastErr = err
			continue // 尝试下一个路径
		}

		// 解析YAML到Config结构
		if err := yaml.Unmarshal(yamlFile, &config); err != nil {
			lastErr = fmt.Errorf("解析YAML失败 (%s): %v", configPath, err)
			continue
		}

		// 成功加载配置
		fmt.Printf("\033[32m✅ 成功加载配置文件: %s\033[0m\n", configPath)
		return &config, nil
	}

	// 所有路径都失败了
	return nil, fmt.Errorf("\033[91m⚠️  警告：加载配置失败: 无法找到配置文件，尝试的路径: %v，最后的错误: %v\033[0m", getConfigPaths(), lastErr)
}
