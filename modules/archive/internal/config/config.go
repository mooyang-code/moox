// Package config loads and validates the moox-archive-mgr configuration.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// Caller 是 mgr 经网关访问 Storage 的内部调用方身份。
const Caller = "archive-mgr"

// Config 是 mgr 的应用配置（config/app.yaml）。
type Config struct {
	GatewayClient gatewayclient.FileConfig `yaml:"gateway_client"`
	sourcePath    string                   `yaml:"-"`
	Database      DatabaseConfig           `yaml:"database"`
	Run           RunConfig                `yaml:"run"`
	Worker        WorkerConfig             `yaml:"worker"`
	Schedule      ScheduleConfig           `yaml:"schedule"`
	Retention     RetentionConfig          `yaml:"retention"`
	// StagingRetention 是 worker 暂存区保留最近多久的数据，之后分区定稿。
	StagingRetention time.Duration `yaml:"staging_retention"`
	// Spaces 是各空间的时区与本地收盘时刻；没有配置的空间按 UTC、全天交易处理。
	Spaces map[string]SpaceConfig `yaml:"spaces"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// RunConfig 控制运行的租约。
type RunConfig struct {
	// LeaseTTL 是运行租约的有效期，进度上报时续租。
	LeaseTTL time.Duration `yaml:"lease_ttl"`
}

// WorkerConfig 控制 worker 的租约。
type WorkerConfig struct {
	// TakeoverWait 是旧 worker 停止心跳多久之后新 worker 才能接管，同时也是心跳续租的有效期。
	TakeoverWait time.Duration `yaml:"takeover_wait"`
}

// ScheduleConfig 是执行计划的规则。
type ScheduleConfig struct {
	MinInterval time.Duration `yaml:"min_interval"`
	MinLookback time.Duration `yaml:"min_lookback"`
}

// RetentionConfig 是 mgr 库中运行记录的保留期。
type RetentionConfig struct {
	ScheduledSuccess time.Duration `yaml:"scheduled_success"`
	Other            time.Duration `yaml:"other"`
}

// SpaceConfig 描述一个空间的交易日历属性。
type SpaceConfig struct {
	TZ         string `yaml:"tz"`
	LocalClose string `yaml:"local_close"`
}

// Load 读取并校验配置文件；未知字段与多文档都会被拒绝。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取归档配置 %s 失败: %w", path, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析归档配置路径失败: %w", err)
	}
	cfg := Default()
	cfg.sourcePath = absolute
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析归档配置 %s 失败: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("归档配置只能包含一个 YAML 文档")
	}
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Default 返回带默认值的配置。
func Default() *Config {
	return &Config{
		GatewayClient:    gatewayclient.FileConfig{Caller: Caller, KeyFile: "../../secrets/caller-archive-mgr.key"},
		Database:         DatabaseConfig{Path: "./data/archive-mgr/archive.db"},
		Run:              RunConfig{LeaseTTL: 10 * time.Minute},
		Worker:           WorkerConfig{TakeoverWait: 45 * time.Second},
		Schedule:         ScheduleConfig{MinInterval: 5 * time.Minute, MinLookback: 15 * time.Minute},
		Retention:        RetentionConfig{ScheduledSuccess: 30 * 24 * time.Hour, Other: 90 * 24 * time.Hour},
		StagingRetention: 72 * time.Hour,
		Spaces: map[string]SpaceConfig{
			"crypto":  {TZ: "UTC"},
			"stockcn": {TZ: "Asia/Shanghai", LocalClose: "15:00"},
			"stockhk": {TZ: "Asia/Hong_Kong", LocalClose: "16:10"},
			"stockus": {TZ: "America/New_York", LocalClose: "16:00"},
		},
	}
}

func (c *Config) applyEnv() {
	if value := strings.TrimSpace(os.Getenv("MOOX_ARCHIVE_DB_PATH")); value != "" {
		c.Database.Path = value
	}
}

// Validate 校验配置。
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("归档配置不能为空")
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		return fmt.Errorf("database.path 不能为空")
	}
	if c.GatewayClient.Caller != Caller {
		return fmt.Errorf("gateway_client.caller 必须是 %s", Caller)
	}
	if err := c.GatewayClient.Validate(); err != nil {
		return err
	}
	if c.Run.LeaseTTL < time.Minute {
		return fmt.Errorf("run.lease_ttl 不能小于 1 分钟")
	}
	if c.Worker.TakeoverWait < 15*time.Second {
		return fmt.Errorf("worker.takeover_wait 不能小于 15 秒")
	}
	if c.Schedule.MinInterval < 5*time.Minute {
		return fmt.Errorf("schedule.min_interval 不能小于 5 分钟")
	}
	if c.Schedule.MinLookback < 15*time.Minute {
		return fmt.Errorf("schedule.min_lookback 不能小于 15 分钟")
	}
	if c.Retention.ScheduledSuccess < 24*time.Hour || c.Retention.Other < c.Retention.ScheduledSuccess {
		return fmt.Errorf("retention.scheduled_success 至少 1 天，retention.other 不能小于它")
	}
	if c.StagingRetention < 24*time.Hour {
		return fmt.Errorf("staging_retention 不能小于 1 天")
	}
	for space, cfg := range c.Spaces {
		if strings.TrimSpace(space) != strings.ToLower(strings.TrimSpace(space)) || space == "" {
			return fmt.Errorf("spaces 的键必须是小写空间 ID: %q", space)
		}
		if _, err := time.LoadLocation(cfg.TZ); err != nil || cfg.TZ == "" {
			return fmt.Errorf("spaces.%s.tz %q 无效", space, cfg.TZ)
		}
		if cfg.LocalClose != "" {
			if _, err := time.Parse("15:04", cfg.LocalClose); err != nil {
				return fmt.Errorf("spaces.%s.local_close %q 必须是 HH:MM", space, cfg.LocalClose)
			}
		}
	}
	return nil
}

// Space 返回空间的时区与本地收盘时刻；没有配置时按 UTC、全天交易。
func (c *Config) Space(spaceID string) SpaceConfig {
	if cfg, ok := c.Spaces[strings.ToLower(strings.TrimSpace(spaceID))]; ok {
		return cfg
	}
	return SpaceConfig{TZ: "UTC"}
}

// OpenGateway 载入 mgr 的进程级签名身份与服务目录。
func (c *Config) OpenGateway(onRefreshError func(error)) (*gatewayclient.Client, error) {
	if c == nil || c.sourcePath == "" {
		return nil, fmt.Errorf("归档网关客户端需要先载入配置文件")
	}
	return c.GatewayClient.OpenInternal(c.sourcePath, filepath.Dir(c.Database.Path), onRefreshError)
}
