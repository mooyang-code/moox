// Package config 提供统一的配置加载工具
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// ConfigLoader 配置加载器
type ConfigLoader struct {
	baseDir string
}

type RuntimeConfig struct {
	Storage StorageConfig `yaml:"storage"`
}

type StorageConfig struct {
	Root     string          `yaml:"root"`
	Metadata StorageMetadata `yaml:"metadata"`
	Devices  StorageDevices  `yaml:"devices"`
	Primary  StoragePrimary  `yaml:"primary"`
	EventBus StorageEventBus `yaml:"eventbus"`
}

type StorageMetadata struct {
	Path string `yaml:"path"`
}

type StorageDevices struct {
	PebblePath  string `yaml:"pebble_path"`
	DuckDBPath  string `yaml:"duckdb_path"`
	BlevePath   string `yaml:"bleve_path"`
	ParquetPath string `yaml:"parquet_path"`
}

type StorageEventBus struct {
	Type               string `yaml:"type"`
	NATSURL            string `yaml:"nats_url"`
	StreamName         string `yaml:"stream_name"`
	SubjectPrefix      string `yaml:"subject_prefix"`
	RowsChangedSubject string `yaml:"rows_changed_subject"`
	ConsumerName       string `yaml:"consumer_name"`
}

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
		c.EventBus.Type = "memory"
	}
	if c.EventBus.SubjectPrefix == "" {
		c.EventBus.SubjectPrefix = "moox.storage"
	}
	if c.EventBus.RowsChangedSubject == "" {
		c.EventBus.RowsChangedSubject = c.EventBus.SubjectPrefix + ".fact.rows_changed.v1"
	}
	if c.EventBus.Type == "nats" && c.EventBus.StreamName == "" {
		c.EventBus.StreamName = "MOOX_STORAGE"
	}
	if c.EventBus.Type == "nats" && c.EventBus.ConsumerName == "" {
		c.EventBus.ConsumerName = "storage_rows_changed_deriver"
	}
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
