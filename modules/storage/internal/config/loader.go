// Package config 提供统一的配置加载工具
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"
)

// ConfigLoader 配置加载器
type ConfigLoader struct {
	baseDir string
}

// RuntimeConfig 保存运行时加载出的完整业务配置。
type RuntimeConfig struct {
	Storage StorageConfig `yaml:"storage"`
}

// StorageConfig 保存 storage.yaml 中的业务配置。
type StorageConfig struct {
	Root     string          `yaml:"root"`
	Roles    []string        `yaml:"roles"`
	Metadata StorageMetadata `yaml:"metadata"`
	Devices  StorageDevices  `yaml:"devices"`
	Primary  StoragePrimary  `yaml:"primary"`
	EventBus StorageEventBus `yaml:"eventbus"`
	Deriver  StorageDeriver  `yaml:"deriver"`
}

// StorageMetadata 保存元数据存储与种子数据配置。
type StorageMetadata struct {
	Path string `yaml:"path"`
}

// StorageDevices 保存底层存储设备路径配置。
type StorageDevices struct {
	PebblePath  string `yaml:"pebble_path"`
	DuckDBPath  string `yaml:"duckdb_path"`
	BlevePath   string `yaml:"bleve_path"`
	ParquetPath string `yaml:"parquet_path"`
}

// StorageEventBus 保存事件总线传输配置。
type StorageEventBus struct {
	Type          string                  `yaml:"type"`
	NATSURL       string                  `yaml:"nats_url"`
	StreamName    string                  `yaml:"stream_name"`
	SubjectPrefix string                  `yaml:"subject_prefix"`
	ConsumerName  string                  `yaml:"consumer_name"`
	Embedded      StorageEmbeddedEventBus `yaml:"embedded"`
}

// StorageEmbeddedEventBus 保存本地内嵌事件总线服务配置。
type StorageEmbeddedEventBus struct {
	Enabled          bool   `yaml:"enabled"`
	Host             string `yaml:"host"`
	Port             int    `yaml:"port"`
	StoreDir         string `yaml:"store_dir"`
	StartupTimeoutMS int    `yaml:"startup_timeout_ms"`
}

// StorageDeriver 保存派生服务消费与批处理配置。
type StorageDeriver struct {
	AccessServiceName string `yaml:"access_service_name"`
	BatchSize         int    `yaml:"batch_size"`
	BatchWaitMS       int    `yaml:"batch_wait_ms"`
	MaxWorkers        int    `yaml:"max_workers"`
}

// StoragePrimary 保存主存服务访问配置。
type StoragePrimary struct {
	ServiceName string `yaml:"service_name"`
}

func (c *RuntimeConfig) ApplyDefaults() {
	c.Storage.ApplyDefaults()
}

func (c *StorageConfig) ApplyDefaults() {
	if c.Root == "" {
		c.Root = "./var/storage"
	}
	if len(c.Roles) == 0 {
		c.Roles = []string{"access", "deriver"}
	}
	if c.Metadata.Path == "" {
		c.Metadata.Path = filepath.Join(c.Root, "metadata", "storage_metadata.db")
	}
	if c.Devices.PebblePath == "" {
		c.Devices.PebblePath = filepath.Join(c.Root, "pebble")
	}
	if c.Devices.DuckDBPath == "" {
		c.Devices.DuckDBPath = filepath.Join(c.Root, "duckdb", "views.duckdb")
	}
	if c.Devices.BlevePath == "" {
		c.Devices.BlevePath = filepath.Join(c.Root, "bleve")
	}
	if c.Devices.ParquetPath == "" {
		c.Devices.ParquetPath = filepath.Join(c.Root, "archive")
	}
	if c.EventBus.Type == "" {
		c.EventBus.Type = "nats"
	}
	if c.EventBus.NATSURL == "" {
		c.EventBus.NATSURL = "nats://127.0.0.1:4222"
	}
	if c.EventBus.SubjectPrefix == "" {
		c.EventBus.SubjectPrefix = "moox.storage"
	}
	if c.EventBus.StreamName == "" {
		c.EventBus.StreamName = "MOOX_STORAGE"
	}
	if c.EventBus.ConsumerName == "" {
		c.EventBus.ConsumerName = "storage_deriver"
	}
	if c.EventBus.Embedded.Enabled {
		if c.EventBus.Embedded.Host == "" {
			c.EventBus.Embedded.Host = "127.0.0.1"
		}
		if c.EventBus.Embedded.Port == 0 {
			c.EventBus.Embedded.Port = 4222
		}
		if c.EventBus.Embedded.StoreDir == "" {
			c.EventBus.Embedded.StoreDir = filepath.Join(c.Root, "nats")
		}
		if c.EventBus.Embedded.StartupTimeoutMS <= 0 {
			c.EventBus.Embedded.StartupTimeoutMS = 10000
		}
	}
	if c.Deriver.AccessServiceName == "" {
		c.Deriver.AccessServiceName = "trpc.moox.storage.Access"
	}
	if c.Deriver.BatchSize <= 0 {
		c.Deriver.BatchSize = 500
	}
	if c.Deriver.BatchWaitMS <= 0 {
		c.Deriver.BatchWaitMS = 200
	}
	if c.Deriver.MaxWorkers <= 0 {
		c.Deriver.MaxWorkers = 4
	}
}

func (c *StorageConfig) HasRole(role string) bool {
	normalized := strings.ToLower(strings.TrimSpace(role))
	if normalized == "" {
		return false
	}
	for _, candidate := range c.Roles {
		if strings.ToLower(strings.TrimSpace(candidate)) == normalized {
			return true
		}
	}
	return false
}

// NewConfigLoader 创建配置加载器
func NewConfigLoader(baseDir string) *ConfigLoader {
	return &ConfigLoader{
		baseDir: baseDir,
	}
}

// LoadConfig 通用配置加载函数
func (c *ConfigLoader) LoadConfig(filename string, config interface{}) error {
	// 构建配置文件路径
	configPath := filepath.Join(c.baseDir, filename)

	// 读取配置文件
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败 %s: %w", configPath, err)
	}

	// 解析YAML到Config结构
	if err := yaml.Unmarshal(yamlFile, config); err != nil {
		return fmt.Errorf("解析YAML失败 %s: %w", configPath, err)
	}

	return nil
}

// LoadConfigWithDefaults 加载配置并应用默认值
func (c *ConfigLoader) LoadConfigWithDefaults(filename string, config interface{}, defaultsFunc func()) error {
	err := c.LoadConfig(filename, config)
	if err != nil {
		return err
	}

	// 应用默认值
	if defaultsFunc != nil {
		defaultsFunc()
	}

	return nil
}
