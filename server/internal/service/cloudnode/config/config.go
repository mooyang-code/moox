package config

import (
	"os"
	"sync"

	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/log"
)

var (
	cachedConfig *Config
	loadOnce     sync.Once
)

// Config 云节点模块相关的配置
type Config struct {
	Prober        ProberConfig        `yaml:"prober"`
	Heartbeat     HeartbeatConfig     `yaml:"heartbeat"`
	CloudFunction CloudFunctionConfig `yaml:"cloudfunction"`
	CloudRegions  CloudRegionsConfig  `yaml:"cloud_regions"`
}

// RegionInfo 地区信息
type RegionInfo struct {
	Code string `yaml:"code" json:"code"`
	Name string `yaml:"name" json:"name"`
	Tag  string `yaml:"tag" json:"tag"` // 标签（国内/海外）
}

// CloudRegionsConfig 云厂商地区配置
type CloudRegionsConfig struct {
	Tencent []RegionInfo `yaml:"tencent"`
	// 未来可扩展其他云厂商
	// Aliyun []RegionInfo `yaml:"aliyun"`
	// AWS    []RegionInfo `yaml:"aws"`
}

// ProberConfig 探测器配置
type ProberConfig struct {
	MaxConcurrent int `yaml:"max_concurrent"` // 最大并发探测数
}

// HeartbeatConfig 心跳配置
type HeartbeatConfig struct {
	DefaultTimeoutThreshold  int `yaml:"default_timeout_threshold"`  // 默认超时阈值（秒）
	DefaultHeartbeatInterval int `yaml:"default_heartbeat_interval"` // 默认心跳间隔（秒）
}

// CloudFunctionConfig 云函数配置
type CloudFunctionConfig struct {
	ZipFilePath       string            `yaml:"zip_file_path"`
	DefaultTimeout    int               `yaml:"default_timeout"`
	DefaultMemorySize int               `yaml:"default_memory_size"`
	DefaultEnvVars    map[string]string `yaml:"default_env_vars"`
}

// Get 获取配置（缓存）
func Get() *Config {
	loadOnce.Do(func() {
		cachedConfig = LoadConfig()
	})
	return cachedConfig
}

// LoadConfig 加载配置
func LoadConfig() *Config {
	configPath := "./config/cloudnode.yaml"

	// 默认配置
	cfg := &Config{
		Prober: ProberConfig{
			MaxConcurrent: 10,
		},
		Heartbeat: HeartbeatConfig{
			DefaultTimeoutThreshold:  50, // 默认50秒超时
			DefaultHeartbeatInterval: 10, // 默认10秒心跳间隔
		},
		CloudFunction: CloudFunctionConfig{
			ZipFilePath:       "/tmp/collector-scf.zip",
			DefaultTimeout:    30,
			DefaultMemorySize: 128,
			DefaultEnvVars: map[string]string{
				"NODE_ENV": "production",
			},
		},
	}

	// 尝试从文件加载配置
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Warnf("[CloudNodeConfig] 无法读取配置文件 %s: %v, 使用默认配置", configPath, err)
		return cfg
	}

	// 解析 YAML
	if err := yaml.Unmarshal(data, cfg); err != nil {
		log.Warnf("[CloudNodeConfig] 解析配置文件失败: %v, 使用默认配置", err)
		return cfg
	}

	// 验证配置
	if err := cfg.Validate(); err != nil {
		log.Warnf("[CloudNodeConfig] 配置验证失败: %v, 使用默认配置", err)
		return cfg
	}

	log.Infof("[CloudNodeConfig] 从文件加载配置成功: %s", configPath)
	return cfg
}

// Validate 验证配置
func (c *Config) Validate() error {
	if c.Prober.MaxConcurrent <= 0 {
		c.Prober.MaxConcurrent = 10 // 默认值
	}

	if c.Heartbeat.DefaultTimeoutThreshold <= 0 {
		c.Heartbeat.DefaultTimeoutThreshold = 50 // 默认50秒
	}

	if c.Heartbeat.DefaultHeartbeatInterval <= 0 {
		c.Heartbeat.DefaultHeartbeatInterval = 10 // 默认10秒
	}

	if c.CloudFunction.ZipFilePath == "" {
		c.CloudFunction.ZipFilePath = "/tmp/collector-scf.zip"
	}

	if c.CloudFunction.DefaultTimeout <= 0 {
		c.CloudFunction.DefaultTimeout = 30
	}

	if c.CloudFunction.DefaultMemorySize <= 0 {
		c.CloudFunction.DefaultMemorySize = 128
	}

	if c.CloudFunction.DefaultEnvVars == nil {
		c.CloudFunction.DefaultEnvVars = make(map[string]string)
	}
	return nil
}

// String 返回配置的字符串表示
func (c *Config) String() string {
	data, _ := yaml.Marshal(c)
	return string(data)
}

// GetTencentRegionCodes 获取腾讯云地区代码列表
func (c *Config) GetTencentRegionCodes() []string {
	var codes []string
	for _, r := range c.CloudRegions.Tencent {
		codes = append(codes, r.Code)
	}
	return codes
}
