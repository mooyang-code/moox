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
	Root       string            `yaml:"root"`
	Metadata   StorageMetadata   `yaml:"metadata"`
	Devices    StorageDevices    `yaml:"devices"`
	Changefeed StorageChangefeed `yaml:"changefeed"`
}

type StorageMetadata struct {
	Path       string `yaml:"path"`
	SchemaPath string `yaml:"schema_path"`
}

type StorageDevices struct {
	PebblePath  string `yaml:"pebble_path"`
	DuckDBPath  string `yaml:"duckdb_path"`
	BlevePath   string `yaml:"bleve_path"`
	ParquetPath string `yaml:"parquet_path"`
}

type StorageChangefeed struct {
	Type    string `yaml:"type"`
	NATSURL string `yaml:"nats_url"`
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
	if c.Metadata.SchemaPath == "" {
		c.Metadata.SchemaPath = "../../schema/storage_metadata.sql"
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
	if c.Changefeed.Type == "" {
		c.Changefeed.Type = "memory"
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
