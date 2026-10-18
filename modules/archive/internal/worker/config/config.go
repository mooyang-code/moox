// Package config loads and validates the moox-archive-worker configuration.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/pacing"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// Caller 是 worker 的外部身份。
const Caller = "archive-worker"

// ByteSize 解析 "5GB"、"512KB" 这类大小，也接受纯数字（字节）。
type ByteSize int64

// UnmarshalYAML 实现 yaml.Unmarshaler。
func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	value, err := ParseBytes(raw)
	if err != nil {
		return err
	}
	*b = ByteSize(value)
	return nil
}

// ParseBytes 解析大小字符串（KB/MB/GB/TB 按 1024 进制，大小写不敏感）。
func ParseBytes(raw string) (int64, error) {
	text := strings.ToUpper(strings.TrimSpace(raw))
	if text == "" {
		return 0, fmt.Errorf("size is empty")
	}
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		mult   int64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(text, unit.suffix) {
			text, multiplier = strings.TrimSpace(strings.TrimSuffix(text, unit.suffix)), unit.mult
			break
		}
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("invalid size %q", raw)
	}
	return int64(number * float64(multiplier)), nil
}

// Config 是 worker 的应用配置（config/app.yaml）。
type Config struct {
	GatewayClient   gatewayclient.ExternalFileConfig `yaml:"gateway_client"`
	StorageAuthFile string                           `yaml:"storage_auth_file"`
	Worker          WorkerSection                    `yaml:"worker"`
	Staging         StagingSection                   `yaml:"staging"`
	Pacing          PacingSection                    `yaml:"pacing"`
	Health          HealthSection                    `yaml:"health"`
	Maintenance     MaintenanceSection               `yaml:"maintenance"`
	sourcePath      string
}

// WorkerSection 是 worker 本身的设置。
type WorkerSection struct {
	WorkerID          string        `yaml:"worker_id"`
	RootDir           string        `yaml:"root_dir"`
	ExportFormats     []string      `yaml:"export_formats"`
	MinFreeBytes      ByteSize      `yaml:"min_free_bytes"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	PullInterval      time.Duration `yaml:"pull_interval"`
}

// StagingSection 是 DuckDB 暂存库的资源设置。
type StagingSection struct {
	MemoryLimit  string  `yaml:"memory_limit"`
	Threads      int     `yaml:"threads"`
	RebuildRatio float64 `yaml:"rebuild_ratio"`
}

// PacingSection 是节奏与并发。
type PacingSection struct {
	RequestSeconds [2]int `yaml:"request_seconds"`
	// MinuteWindow 控制是否只在每分钟的 request_seconds 区间内发请求，默认 true；设为 false 则整分钟都可发（整点静默仍生效）。
	MinuteWindow       bool          `yaml:"minute_window"`
	HourQuiet          [2]string     `yaml:"hour_quiet"`
	RequestInterval    time.Duration `yaml:"request_interval"`
	MaxBytesPerSecond  ByteSize      `yaml:"max_bytes_per_second"`
	MaxConcurrentRuns  int           `yaml:"max_concurrent_runs"`
	MaxRunsPerDataNode int           `yaml:"max_runs_per_data_node"`
	PageRows           int           `yaml:"page_rows"`
}

// HealthSection 是健康检查监听地址。
type HealthSection struct {
	Addr string `yaml:"addr"`
}

// MaintenanceSection 是每天的维护时刻（本机时区）。
type MaintenanceSection struct {
	At string `yaml:"at"`
}

// Default 返回带默认值的配置。
func Default() *Config {
	host, _ := os.Hostname()
	return &Config{
		StorageAuthFile: "../secrets/storage-primary-auth.secret",
		Worker: WorkerSection{
			WorkerID: Caller + "@" + host, ExportFormats: []string{"csv.gz"}, MinFreeBytes: ByteSize(5 << 30),
			HeartbeatInterval: 10 * time.Second, PullInterval: 5 * time.Second,
		},
		Staging: StagingSection{MemoryLimit: "1GB", Threads: 2, RebuildRatio: 2},
		Pacing: PacingSection{
			RequestSeconds: [2]int{25, 55}, MinuteWindow: true, HourQuiet: [2]string{"58:00", "04:00"}, RequestInterval: 200 * time.Millisecond,
			MaxBytesPerSecond: ByteSize(512 << 10), MaxConcurrentRuns: 4, MaxRunsPerDataNode: 2, PageRows: 500,
		},
		Health:      HealthSection{Addr: "127.0.0.1:11418"},
		Maintenance: MaintenanceSection{At: "04:17:43"},
	}
}

// Load 读取并校验配置；未知字段与多文档都被拒绝。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 worker 配置 %s 失败: %w", path, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	cfg.sourcePath = absolute
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析 worker 配置 %s 失败: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("worker 配置只能包含一个 YAML 文档")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate 校验配置。
func (c *Config) Validate() error {
	if c.GatewayClient.Caller != Caller {
		return fmt.Errorf("gateway_client.caller 必须是 %s", Caller)
	}
	if err := c.GatewayClient.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.StorageAuthFile) == "" {
		return fmt.Errorf("storage_auth_file 不能为空")
	}
	w := c.Worker
	if strings.TrimSpace(w.WorkerID) == "" || strings.ContainsAny(w.WorkerID, " \t\r\n/\\") {
		return fmt.Errorf("worker.worker_id 不能为空且不能包含空白或斜杠")
	}
	if !filepath.IsAbs(w.RootDir) {
		return fmt.Errorf("worker.root_dir 必须是绝对路径")
	}
	if strings.Contains(w.RootDir, string(filepath.Separator)+"Documents"+string(filepath.Separator)) {
		return fmt.Errorf("worker.root_dir 不能放在 ~/Documents 下")
	}
	if len(w.ExportFormats) != 1 || w.ExportFormats[0] != "csv.gz" {
		return fmt.Errorf("worker.export_formats 目前只支持 [csv.gz]")
	}
	if w.MinFreeBytes < 0 {
		return fmt.Errorf("worker.min_free_bytes 不能为负")
	}
	if w.HeartbeatInterval < time.Second || w.HeartbeatInterval > 15*time.Second {
		return fmt.Errorf("worker.heartbeat_interval 必须在 1s 到 15s 之间（mgr 45 秒无心跳即接管）")
	}
	if w.PullInterval < time.Second {
		return fmt.Errorf("worker.pull_interval 不能小于 1s")
	}
	if c.Staging.Threads < 1 || c.Staging.Threads > 8 {
		return fmt.Errorf("staging.threads 必须在 1 到 8 之间")
	}
	if c.Staging.RebuildRatio < 1.2 {
		return fmt.Errorf("staging.rebuild_ratio 不能小于 1.2")
	}
	if c.Staging.MemoryLimit == "" {
		return fmt.Errorf("staging.memory_limit 不能为空")
	}
	p := c.Pacing
	if err := c.PacingConfig().Validate(); err != nil {
		return err
	}
	if p.MaxConcurrentRuns < 1 || p.MaxConcurrentRuns > 16 {
		return fmt.Errorf("pacing.max_concurrent_runs 必须在 1 到 16 之间")
	}
	if p.MaxRunsPerDataNode < 1 || p.MaxRunsPerDataNode > p.MaxConcurrentRuns {
		return fmt.Errorf("pacing.max_runs_per_data_node 必须在 1 到 max_concurrent_runs 之间")
	}
	if p.PageRows < 1 || p.PageRows > 2000 {
		return fmt.Errorf("pacing.page_rows 必须在 1 到 2000 之间（Storage 单页上限 2000）")
	}
	if strings.TrimSpace(c.Health.Addr) == "" {
		return fmt.Errorf("health.addr 不能为空")
	}
	if _, err := ParseClock(c.Maintenance.At); err != nil {
		return fmt.Errorf("maintenance.at: %w", err)
	}
	return nil
}

// PacingConfig 转成节奏包的配置。
func (c *Config) PacingConfig() pacing.Config {
	return pacing.Config{
		RequestSeconds: c.Pacing.RequestSeconds, DisableMinuteWindow: !c.Pacing.MinuteWindow, HourQuiet: c.Pacing.HourQuiet,
		RequestInterval: c.Pacing.RequestInterval, MaxBytesPerSecond: int64(c.Pacing.MaxBytesPerSecond),
	}
}

// ParseClock 解析 "HH:MM:SS"。
func ParseClock(value string) (time.Duration, error) {
	t, err := time.Parse("15:04:05", strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%q 必须是 HH:MM:SS", value)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second, nil
}

// StatePath 返回暂存库路径。
func (c *Config) StatePath() string {
	return filepath.Join(c.Worker.RootDir, ".state", "staging.duckdb")
}

// StorageSecretPath 返回 Storage 鉴权文件的绝对路径（相对配置文件所在目录）。
func (c *Config) StorageSecretPath() string {
	if filepath.IsAbs(c.StorageAuthFile) {
		return c.StorageAuthFile
	}
	return filepath.Join(filepath.Dir(c.sourcePath), c.StorageAuthFile)
}

// OpenGateway 打开经 Access 的外部网关客户端。
func (c *Config) OpenGateway() (*gatewayclient.Client, error) {
	if c.sourcePath == "" {
		return nil, fmt.Errorf("worker 网关客户端需要先载入配置文件")
	}
	return c.GatewayClient.OpenExternal(c.sourcePath)
}
