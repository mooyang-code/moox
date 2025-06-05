package config

import (
	"fmt"
	"os"

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

// Config 元数据服务配置
type Config struct {
	MetadataDatabase struct {
		StorageDevice string `yaml:"storage_device"`
	} `yaml:"metadata_database"`

	Storage struct {
		Target string `yaml:"target"`
	} `yaml:"storage"`

	Message *MessageConfig `yaml:"message"` // 消息服务配置
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	var config Config

	// 读取配置文件
	configPath := "tool.yaml"
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %+v", err)
	}

	// 解析YAML到Config结构
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, fmt.Errorf("解析YAML失败: %+v", err)
	}

	return &config, nil
}
