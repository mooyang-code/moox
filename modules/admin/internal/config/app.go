// Package config 提供统一的配置管理
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mooyang-code/moox/pkg/infraconfig"
)

// 全局配置实例
var (
	globalConfig *AppConfig
	configMutex  sync.RWMutex
)

// AppConfig 应用配置（总配置）
type AppConfig struct {
	Database       DatabaseConfig       `yaml:"database"`
	Storage        StorageConfig        `yaml:"storage"`
	Auth           AuthConfig           `yaml:"auth"`
	Worker         WorkerConfig         `yaml:"worker"`
	TaskManagement TaskManagementConfig `yaml:"task_management"`
	Log            LogConfig            `yaml:"log"`
	Security       SecurityConfig       `yaml:"security"`
	Monitor        MonitorConfig        `yaml:"monitor"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Type            string        `yaml:"type"`               // sqlite, mysql, postgres
	Path            string        `yaml:"path"`               // SQLite文件路径
	Host            string        `yaml:"host"`               // 数据库主机
	Port            int           `yaml:"port"`               // 数据库端口
	User            string        `yaml:"user"`               // 用户名
	Password        string        `yaml:"password"`           // 密码
	DBName          string        `yaml:"dbname"`             // 数据库名
	MaxIdleConns    int           `yaml:"max_idle_conns"`     // 最大空闲连接数
	MaxOpenConns    int           `yaml:"max_open_conns"`     // 最大打开连接数
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`  // 连接最大生命周期
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"` // 连接最大空闲时间
}

// StorageConfig 存储配置
type StorageConfig struct {
	COSBucket   string `yaml:"cos_bucket"`   // COS桶名
	COSRegion   string `yaml:"cos_region"`   // COS区域
	LocalPath   string `yaml:"local_path"`   // 本地存储路径
	CacheSize   int    `yaml:"cache_size"`   // 缓存大小（文件数量）
	CacheExpiry int    `yaml:"cache_expiry"` // 缓存过期时间（分钟）
	XDataURL    string `yaml:"xdata_url"`    // xData存储服务地址 (如 http://127.0.0.1:20201)
}

// AuthConfig 认证配置
type AuthConfig struct {
	JWTSecret        string        `yaml:"jwt_secret"`
	JWTExpiry        time.Duration `yaml:"jwt_expiry"`         // Token过期时间
	MaxLoginAttempts int           `yaml:"max_login_attempts"` // 最大登录尝试次数
	LockDuration     time.Duration `yaml:"lock_duration"`      // 锁定时长
}

// WorkerConfig Worker配置
type WorkerConfig struct {
	AsyncTaskWorkerCount      int `yaml:"async_task_worker_count"`      // 异步任务Worker数量
	NodeCreationWorkerCount   int `yaml:"node_creation_worker_count"`   // 节点创建Worker数量
	NodeDeletionWorkerCount   int `yaml:"node_deletion_worker_count"`   // 节点删除Worker数量
	NodeDeploymentWorkerCount int `yaml:"node_deployment_worker_count"` // 节点部署Worker数量
}

// TaskManagementConfig 任务管理配置
type TaskManagementConfig struct {
	Tolerance       int    `yaml:"tolerance"`        // 统一容差（秒），用于Pending超时判断
	DefaultInterval string `yaml:"default_interval"` // 默认interval（解析失败时使用）
}

// LogConfig 日志配置
type LogConfig struct {
	Level      string `yaml:"level"`       // debug, info, warn, error
	OutputPath string `yaml:"output_path"` // 日志输出路径
	MaxSize    int    `yaml:"max_size"`    // 单文件最大大小（MB）
	MaxBackups int    `yaml:"max_backups"` // 最多保留文件数
	MaxAge     int    `yaml:"max_age"`     // 最多保留天数
}

// SecurityConfig 安全配置
type SecurityConfig struct {
	EncryptionKey string `yaml:"encryption_key"` // 数据加密密钥（32字节）
}

// MonitorConfig 监控配置
type MonitorConfig struct {
	NodeExporterPort int `yaml:"node_exporter_port"` // Node Exporter 端口，默认 9100
	CollectTimeout   int `yaml:"collect_timeout"`    // 采集超时时间（秒），默认 10
	ConcurrentLimit  int `yaml:"concurrent_limit"`   // 并发采集限制，默认 20
}

// DefaultConfig 返回默认配置
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox.db",
			MaxIdleConns:    10,
			MaxOpenConns:    100,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Storage: StorageConfig{
			COSBucket:   "moox-packages",
			COSRegion:   "ap-guangzhou",
			LocalPath:   "./data/packages",
			CacheSize:   100,
			CacheExpiry: 30,
		},
		Auth: AuthConfig{
			JWTExpiry:        24 * time.Hour,
			MaxLoginAttempts: 5,
			LockDuration:     15 * time.Minute,
		},
		Worker: WorkerConfig{
			AsyncTaskWorkerCount:      3,
			NodeCreationWorkerCount:   3,
			NodeDeletionWorkerCount:   3,
			NodeDeploymentWorkerCount: 3,
		},
		TaskManagement: TaskManagementConfig{
			Tolerance:       30,
			DefaultInterval: "1m",
		},
		Log: LogConfig{
			Level:      "info",
			OutputPath: "./log/moox.log",
			MaxSize:    100,
			MaxBackups: 10,
			MaxAge:     30,
		},
		Security: SecurityConfig{
			EncryptionKey: "moox-cloud-secret-key-32bytes", // 默认密钥（仅用于开发环境）
		},
		Monitor: MonitorConfig{
			NodeExporterPort: 9100, // Node Exporter 默认端口
			CollectTimeout:   10,   // 10秒超时
			ConcurrentLimit:  20,   // 最多 20 个并发
		},
	}
}

// Load 从文件加载配置
func Load(configPath string) (*AppConfig, error) {
	// 1. 加载默认配置
	cfg := DefaultConfig()

	// 2. 如果配置文件存在，从文件加载
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("failed to read config file: %w", err)
			}
			// 文件不存在，使用默认配置
		} else {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("failed to parse config file: %w", err)
			}
		}
	}

	// 3. 从环境变量覆盖
	cfg.applyEnv()

	// 4. 验证配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// applyEnv 从环境变量覆盖配置
func (c *AppConfig) applyEnv() {
	// Database
	if v := os.Getenv("DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("DB_HOST"); v != "" {
		c.Database.Host = v
	}
	if v := os.Getenv("DB_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Database.Port)
	}
	if v := os.Getenv("DB_USER"); v != "" {
		c.Database.User = v
	}
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		c.Database.Password = v
	}
	if v := os.Getenv("DB_NAME"); v != "" {
		c.Database.DBName = v
	}

	// Storage
	if v := os.Getenv("COS_BUCKET"); v != "" {
		c.Storage.COSBucket = v
	}
	if v := os.Getenv("COS_REGION"); v != "" {
		c.Storage.COSRegion = v
	}
	if v := os.Getenv("LOCAL_STORAGE_PATH"); v != "" {
		c.Storage.LocalPath = v
	}

	// Auth
	if v := os.Getenv("JWT_SECRET"); v != "" {
		c.Auth.JWTSecret = v
	}

	// Security
	if v := os.Getenv("MOOX_ENCRYPTION_KEY"); v != "" {
		c.Security.EncryptionKey = v
	}

	// Monitor
	if v := os.Getenv("NODE_EXPORTER_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Monitor.NodeExporterPort)
	}
	if v := os.Getenv("MONITOR_COLLECT_TIMEOUT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Monitor.CollectTimeout)
	}
	if v := os.Getenv("MONITOR_CONCURRENT_LIMIT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Monitor.ConcurrentLimit)
	}
}

// Validate 验证配置
func (c *AppConfig) Validate() error {
	// 验证必填项

	if c.Database.Type == "sqlite" {
		if c.Database.Path == "" {
			return fmt.Errorf("database path is required for SQLite")
		}
		// 确保目录存在
		dir := filepath.Dir(c.Database.Path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create database directory: %w", err)
		}
	}

	// 确保存储目录存在
	if err := os.MkdirAll(c.Storage.LocalPath, 0755); err != nil {
		return fmt.Errorf("failed to create storage directory: %w", err)
	}

	// 确保日志目录存在
	if c.Log.OutputPath != "" {
		dir := filepath.Dir(c.Log.OutputPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
	}

	return nil
}

// SetGlobalConfig 设置全局配置（由 bootstrap 在启动时调用）
func SetGlobalConfig(cfg *AppConfig) {
	configMutex.Lock()
	defer configMutex.Unlock()
	globalConfig = cfg
}

// GetGlobalConfig 获取全局配置
func GetGlobalConfig() *AppConfig {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return globalConfig
}

// GetXDataURL 获取 xData 存储服务地址。
// 优先取中央基础设施配置 infra/infra*.yaml 的 xdata 端点（dev/仓库内），
// 缺失时回退到 app.yaml 的 storage.xdata_url（部署环境由 deploy 脚本渲染注入）。
func GetXDataURL() string {
	if url := infraconfig.XDataURL(); url != "" {
		return url
	}
	cfg := GetGlobalConfig()
	if cfg == nil || cfg.Storage.XDataURL == "" {
		return ""
	}
	return cfg.Storage.XDataURL
}

// GetMetadataURL 获取 storage Metadata 服务 HTTP 地址。
// 优先从 storage access URL 推导（20201 -> 20200），否则回退默认本地端口。
func GetMetadataURL() string {
	if url := infraconfig.StorageAccessURL(); url != "" {
		if metadataURL := metadataURLFromAccessURL(url); metadataURL != "" {
			return metadataURL
		}
	}
	if url := GetXDataURL(); url != "" {
		if metadataURL := metadataURLFromAccessURL(url); metadataURL != "" {
			return metadataURL
		}
	}
	return "http://127.0.0.1:20200"
}

func metadataURLFromAccessURL(accessURL string) string {
	accessURL = strings.TrimSpace(accessURL)
	if accessURL == "" {
		return ""
	}
	if strings.Contains(accessURL, ":20201") {
		return strings.Replace(accessURL, ":20201", ":20200", 1)
	}
	return ""
}

// GetMonitorConfig 获取监控配置
func GetMonitorConfig() MonitorConfig {
	cfg := GetGlobalConfig()
	if cfg == nil {
		// 返回默认配置
		return MonitorConfig{
			NodeExporterPort: 9100,
			CollectTimeout:   10,
			ConcurrentLimit:  20,
		}
	}
	return cfg.Monitor
}
