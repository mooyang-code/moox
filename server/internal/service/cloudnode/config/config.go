package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/log"
)

// Config 心跳服务配置
type Config struct {
	Monitor MonitorConfig `yaml:"monitor"`
	Prober  ProberConfig  `yaml:"prober"`
	Alert   AlertConfig   `yaml:"alert"`
}

// MonitorConfig 监控器配置
type MonitorConfig struct {
	Enabled      bool          `yaml:"enabled"`       // 是否启用监控
	ScanInterval time.Duration `yaml:"scan_interval"` // 扫描间隔
}

// ProberConfig 探测器配置
type ProberConfig struct {
	Enabled       bool          `yaml:"enabled"`        // 是否启用探测
	ScanInterval  time.Duration `yaml:"scan_interval"`  // 扫描间隔
	MaxConcurrent int           `yaml:"max_concurrent"` // 最大并发探测数
}


// AlertConfig 告警集成配置
type AlertConfig struct {
	Enabled              bool   `yaml:"enabled"`                // 是否启用告警触发
	TimeoutAlertLevel    string `yaml:"timeout_alert_level"`    // 超时告警级别
	OfflineAlertLevel    string `yaml:"offline_alert_level"`    // 离线告警级别
	AbnormalAlertLevel   string `yaml:"abnormal_alert_level"`   // 异常告警级别
	ProbeFailedThreshold int    `yaml:"probe_failed_threshold"` // 探测失败N次后触发告警
}

// LoadConfig 加载配置
func LoadConfig() *Config {
	configPath := "./config/heartbeat.yaml"

	// 默认配置
	cfg := &Config{
		Monitor: MonitorConfig{
			Enabled:      true,
			ScanInterval: 10 * time.Second,
		},
		Prober: ProberConfig{
			Enabled:       true,
			ScanInterval:  30 * time.Second,
			MaxConcurrent: 10,
		},
		Alert: AlertConfig{
			Enabled:              true,
			TimeoutAlertLevel:    "warning",
			OfflineAlertLevel:    "error",
			AbnormalAlertLevel:   "warning",
			ProbeFailedThreshold: 3,
		},
	}

	// 尝试从文件加载配置
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Warnf("[HeartbeatConfig] 无法读取配置文件 %s: %v, 使用默认配置", configPath, err)
		return cfg
	}

	// 解析 YAML
	if err := yaml.Unmarshal(data, cfg); err != nil {
		log.Warnf("[HeartbeatConfig] 解析配置文件失败: %v, 使用默认配置", err)
		return cfg
	}

	// 验证配置
	if err := cfg.Validate(); err != nil {
		log.Warnf("[HeartbeatConfig] 配置验证失败: %v, 使用默认配置", err)
		return cfg
	}

	log.Infof("[HeartbeatConfig] 从文件加载配置成功: %s", configPath)
	return cfg
}

// Validate 验证配置
func (c *Config) Validate() error {
	if c.Monitor.ScanInterval <= 0 {
		return fmt.Errorf("monitor.scan_interval must be positive")
	}
	
	if c.Prober.ScanInterval <= 0 {
		return fmt.Errorf("prober.scan_interval must be positive")
	}
	
	if c.Prober.MaxConcurrent <= 0 {
		c.Prober.MaxConcurrent = 5 // 默认值
	}
	
	return nil
}

// String 返回配置的字符串表示
func (c *Config) String() string {
	data, _ := yaml.Marshal(c)
	return string(data)
}