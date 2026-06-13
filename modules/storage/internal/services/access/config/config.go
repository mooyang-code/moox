// Package config 提供访问服务的配置管理功能，包括缓存配置和消息队列配置
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// Config 元数据服务配置
type Config struct {
	// SchemaCaches 配置表缓存（key为表名，value为表数据的api接口url）
	SchemaCaches map[string]string `yaml:"schemacaches"`
	// SchemaCachePollingIntervalSeconds 缓存刷新轮询间隔（秒）
	SchemaCachePollingIntervalSeconds int `yaml:"schema_cache_polling_interval_seconds"`
	// MsgSvrConf 消息队列配置
	MsgSvrConf MessageServerConf `yaml:"message_server"`
	// TableScheduler 数据库表定时任务配置
	TableScheduler TableSchedulerConfig `yaml:"table_scheduler"`
	// NotificationSettings 变更通知设置
	NotificationSettings NotificationSettings `yaml:"notification_settings"`
}

// MessageServerConf 消息队列配置
type MessageServerConf struct {
	Enable                  bool   `yaml:"enable"`                     // 是否启用消息通知功能
	Name                    string `yaml:"name"`                       // 消息组件名
	Host                    string `yaml:"host"`                       // 消息服务器IP
	Port                    int    `yaml:"port"`                       // 消息服务器端口
	DataPath                string `yaml:"data_path"`                  // 消息存储路径
	ObjectModifySubject     string `yaml:"object_modify_subject"`      // 数据对象变更主题
	DataDetailModifySubject string `yaml:"data_detail_modify_subject"` // 数据详情变更主题
}

// TableSchedulerConfig 数据库表定时任务配置
type TableSchedulerConfig struct {
	Enable             bool   `yaml:"enable"`               // 是否启用定时任务
	CronExpression     string `yaml:"cron_expression"`      // Cron表达式，默认每20秒执行一次
	CacheTTLMinutes    int    `yaml:"cache_ttl_minutes"`    // 表创建状态缓存过期时间（分钟）
	MaxConcurrency     int    `yaml:"max_concurrency"`      // 最大并发处理数
	MetaServiceName    string `yaml:"meta_service_name"`    // 元数据服务名
	AdapterServiceName string `yaml:"adapter_service_name"` // 适配器服务名
	AccessServiceName  string `yaml:"access_service_name"`  // 访问服务名
}

// NotificationSettings 变更通知设置
type NotificationSettings struct {
	// EnabledProjectIDs 允许发送变更通知的项目ID列表（默认关闭发送）
	EnabledProjectIDs []int32 `yaml:"enabled_project_ids"`
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	// 优先使用环境变量指定的配置路径
	basePath := os.Getenv("STORAGE_CONFIG_PATH")
	if basePath == "" {
		basePath = "./config"
	}

	// 读取配置文件
	configPath := filepath.Join(basePath, "access.yaml")
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		// 如果失败，尝试直接从当前目录加载
		configPath = "access.yaml"
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

	// 设置定时任务配置默认值
	setTableSchedulerDefaults(&config.TableScheduler)

	// 设置通知配置默认值
	setNotificationDefaults(&config.NotificationSettings)

	// 如果设置了数据库路径环境变量，使用绝对路径
	if dbPath := os.Getenv("STORAGE_DATABASE_PATH"); dbPath != "" {
		config.MsgSvrConf.DataPath = dbPath
	}

	return &config, nil
}

// setTableSchedulerDefaults 设置定时任务配置默认值
func setTableSchedulerDefaults(config *TableSchedulerConfig) {
	if config.CronExpression == "" {
		config.CronExpression = "*/20 * * * * *" // 默认每20秒执行一次
	}
	if config.CacheTTLMinutes <= 0 {
		config.CacheTTLMinutes = 60 // 默认1小时
	}
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = 10 // 默认10个并发
	}
	if config.MetaServiceName == "" {
		config.MetaServiceName = "trpc.storage.metadata.MetaAdmin"
	}
	if config.AdapterServiceName == "" {
		config.AdapterServiceName = "trpc.storage.adapter.Adapter"
	}
	if config.AccessServiceName == "" {
		config.AccessServiceName = "trpc.storage.access.Access"
	}
}

// setNotificationDefaults 设置通知配置默认值
func setNotificationDefaults(config *NotificationSettings) {
	// 如果没有配置，默认为空列表（即默认不发送通知）
	if config.EnabledProjectIDs == nil {
		config.EnabledProjectIDs = []int32{}
	}
}
