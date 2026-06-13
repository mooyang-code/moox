// Package config 提供访问服务的配置管理功能
package config

import (
	"os"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/config"
)

// Config 元数据服务配置
type Config struct {
	// 缓存配置
	SchemaCaches map[string]string `yaml:"schemacaches"`
	// SchemaCachePollingIntervalSeconds 缓存刷新轮询间隔（秒）
	SchemaCachePollingIntervalSeconds int `yaml:"schema_cache_polling_interval_seconds"`
	// DuckDB配置
	DuckDB DuckDBConfig `yaml:"duckdb"`
	// Bleve配置
	Bleve BleveConfig `yaml:"bleve"`
	// CSV配置
	CSV CSVConfig `yaml:"csv"`
	// RocksDB配置
	RocksDB RocksDBConfig `yaml:"rocksdb"`
	// 数据操作限制配置
	Limits LimitsConfig `yaml:"limits"`
}

// DuckDBConfig DuckDB配置
type DuckDBConfig struct {
	// DataPath DuckDB数据文件路径（当connectInfo为localhost时使用）
	DataPath string `yaml:"data_path"`
	// MemoryLimit DuckDB内存限制（默认3GB）
	MemoryLimit string `yaml:"memory_limit"`
}

// BleveConfig Bleve配置
type BleveConfig struct {
	// IndexPath Bleve索引根路径（当connectInfo为localhost时使用）
	IndexPath string `yaml:"index_path"`
}

// CSVConfig CSV配置
type CSVConfig struct {
	// DataPath CSV数据文件根路径（当connectInfo为localhost时使用）
	DataPath string `yaml:"data_path"`
}

// RocksDBConfig RocksDB配置
type RocksDBConfig struct {
	// DataPath RocksDB数据文件路径（当connectInfo为localhost时使用）
	DataPath string `yaml:"data_path"`
	// BlockCacheMB 块缓存大小（MB）
	BlockCacheMB int64 `yaml:"block_cache_mb"`
}

// LimitsConfig 数据操作限制配置
type LimitsConfig struct {
	// MaxUpdateRows 单次set操作的最大行数限制
	MaxUpdateRows uint32 `yaml:"max_update_rows"`
}

var globalConfig *Config

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	// 优先使用环境变量指定的配置路径
	configPath := os.Getenv("STORAGE_CONFIG_PATH")
	if configPath == "" {
		configPath = "./config"
	}

	loader := config.NewConfigLoader(configPath)
	var cfg Config

	err := loader.LoadConfigWithDefaults("adapter.yaml", &cfg, func() {
		setDefaults(&cfg)
	})
	if err != nil {
		// 如果失败，尝试直接从当前目录加载
		loader = config.NewConfigLoader(".")
		err = loader.LoadConfigWithDefaults("adapter.yaml", &cfg, func() {
			setDefaults(&cfg)
		})
		if err != nil {
			return nil, err
		}
	}

	// 保存到全局配置
	globalConfig = &cfg
	return &cfg, nil
}

// setDefaults 设置默认值
func setDefaults(cfg *Config) {
	if cfg.DuckDB.DataPath == "" {
		cfg.DuckDB.DataPath = "../database/duckdb"
	}
	if cfg.DuckDB.MemoryLimit == "" {
		cfg.DuckDB.MemoryLimit = "3GB"
	}

	if cfg.Bleve.IndexPath == "" {
		cfg.Bleve.IndexPath = "../database/bleve"
	}

	if cfg.CSV.DataPath == "" {
		cfg.CSV.DataPath = "../database/csv"
	}

	if cfg.RocksDB.DataPath == "" {
		cfg.RocksDB.DataPath = "../database/rocksdb"
	}
	if cfg.RocksDB.BlockCacheMB == 0 {
		cfg.RocksDB.BlockCacheMB = 512 // 默认512MB
	}

	// 设置限制配置的默认值
	if cfg.Limits.MaxUpdateRows == 0 {
		cfg.Limits.MaxUpdateRows = 25 // 默认最大25行
	}
}

// GetGlobalConfig 获取全局配置
func GetGlobalConfig() *Config {
	return globalConfig
}
