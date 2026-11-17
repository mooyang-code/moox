package topology

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/mooyang-code/moox/packages/security/domainpolicy"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

// StorageRetention 是 [storage_retention]：各时序数据集按空间与频率保留多久，以及 View 每个序列保留多少根。
// 没写 defaults 或 spaces 时取 storagepolicy.Default 的推荐值；写了就原样使用，所以 defaults 要覆盖全部标准频率。
type StorageRetention struct {
	ViewBars uint64                       `toml:"view_rows_per_series" json:"view_rows_per_series"`
	Defaults map[string]string            `toml:"defaults" json:"defaults"`
	Spaces   map[string]map[string]string `toml:"spaces" json:"spaces"`
}

// ViewMaxAge 是 [storage_view.max_age]：View 按空间与频率最多保留多少天的历史，更旧的行不写进 View；数据集不受影响。
type ViewMaxAge struct {
	Defaults map[string]string            `toml:"defaults" json:"defaults"`
	Spaces   map[string]map[string]string `toml:"spaces" json:"spaces"`
}

// StorageView 是 [storage_view]：View 维护的检查节奏、单个 View 文件的上限与各频率的最长历史。
type StorageView struct {
	MaxAge                   ViewMaxAge `toml:"max_age" json:"max_age"`
	MaintenanceCheckInterval string     `toml:"maintenance_check_interval" json:"maintenance_check_interval"`
	MaxViewFileBytes         int64      `toml:"max_view_file_bytes" json:"max_view_file_bytes"`
}

// RecommendedSpaceRetention 是各空间推荐的保留期，只用于没写 spaces 时的缺省与配置页的「恢复推荐值」；
// Storage 不认识这些空间。交易时段市场同样的根数要保留更多自然天。
func RecommendedSpaceRetention() map[string]map[string]string {
	sessionMarket := func(minute string) map[string]string {
		return map[string]string{
			"1m": minute, "5m": "300d", "15m": "900d",
			"30m": storagepolicy.Forever, "1h": storagepolicy.Forever, "4h": storagepolicy.Forever,
		}
	}
	return map[string]map[string]string{
		"stockcn": sessionMarket("60d"),
		"stockhk": sessionMarket("45d"),
		"stockus": sessionMarket("40d"),
	}
}

// StoragePolicyFor 把保留期与 View 设置拼成 storage-policy.json 的内容（storage-primary 与 storage-view 启动时读它）。
func StoragePolicyFor(retention StorageRetention, view StorageView) storagepolicy.Policy {
	return storagepolicy.Policy{
		Retention: storagepolicy.Retention{Defaults: retention.Defaults, Spaces: retention.Spaces},
		View: storagepolicy.View{
			MaxAge:                   storagepolicy.Retention{Defaults: view.MaxAge.Defaults, Spaces: view.MaxAge.Spaces},
			Bars:                     retention.ViewBars,
			MaintenanceCheckInterval: view.MaintenanceCheckInterval,
			MaxViewFileBytes:         view.MaxViewFileBytes,
		},
	}
}

// StoragePolicy 是这份配置渲染出的存储策略。
func (c *Config) StoragePolicy() storagepolicy.Policy {
	return StoragePolicyFor(c.StorageRetention, c.StorageView)
}

// CollectorRetention 是 [collector_retention]：Collector 维护协程的节奏与终态执行记录的保留。
type CollectorRetention struct {
	MaintenanceInterval string `toml:"maintenance_interval" json:"maintenance_interval"`
	MaintenanceOffset   string `toml:"maintenance_offset" json:"maintenance_offset"`
	MaintenanceTimeout  string `toml:"maintenance_timeout" json:"maintenance_timeout"`
	MaxRowsPerPass      int    `toml:"max_rows_per_pass" json:"max_rows_per_pass"`
}

func (r *CollectorRetention) applyDefaults(md toml.MetaData) {
	if !md.IsDefined("collector_retention", "maintenance_interval") {
		r.MaintenanceInterval = "1m"
	}
	if !md.IsDefined("collector_retention", "maintenance_offset") {
		r.MaintenanceOffset = "35s"
	}
	if !md.IsDefined("collector_retention", "maintenance_timeout") {
		r.MaintenanceTimeout = "20s"
	}
	if !md.IsDefined("collector_retention", "max_rows_per_pass") {
		r.MaxRowsPerPass = 50000
	}
}

// Validate 检查维护节奏：每一轮必须在下一个整点之前结束，那时 Collector 的行情调度要独占它的 SQLite 连接。
func (r *CollectorRetention) Validate() error {
	interval, err := time.ParseDuration(strings.TrimSpace(r.MaintenanceInterval))
	if err != nil || interval <= 0 || interval > 24*time.Hour {
		return errors.New("config_invalid: collector_retention.maintenance_interval must be greater than 0 and at most 24h")
	}
	offset, err := time.ParseDuration(strings.TrimSpace(r.MaintenanceOffset))
	if err != nil || offset < 0 || offset >= interval {
		return errors.New("config_invalid: collector_retention.maintenance_offset must be at least 0 and less than maintenance_interval")
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(r.MaintenanceTimeout))
	if err != nil || timeout <= 0 || offset+timeout > interval {
		return errors.New("config_invalid: collector_retention.maintenance_timeout must be positive and maintenance_offset + maintenance_timeout must not exceed maintenance_interval")
	}
	if r.MaxRowsPerPass < 1 || r.MaxRowsPerPass > 50000 {
		return errors.New("config_invalid: collector_retention.max_rows_per_pass must be between 1 and 50000")
	}
	return nil
}

// DefaultEgressHTTPDomains 是没写 [egress_proxy] http_domains 时出口代理的 HTTP 白名单。
func DefaultEgressHTTPDomains() []string {
	return []string{"*.binance.com", "data-api.binance.vision", "openapi.okx.com"}
}

// EgressProxy 是 [egress_proxy]：出口代理允许访问的上游域名。
type EgressProxy struct {
	HTTPDomains []string `toml:"http_domains"`
}

func (e *EgressProxy) Validate() error {
	if _, err := domainpolicy.New(e.HTTPDomains); err != nil {
		return fmt.Errorf("config_invalid: egress_proxy.http_domains: %w", err)
	}
	return nil
}

// Logging 是 [logging]：服务日志的去向。重要日志（Error 及以上、关键事件）总是发给 Monitor，不需要配置。
// 上报腾讯云日志服务用的写入密钥是秘密，在 moox.secrets.toml 的 [logging.cls] 里。
type Logging struct {
	// RetentionDays 是腾讯云日志服务里日志保留的天数（初始化时建的采集日志主题用它），默认 2；1～3600，3640 表示永久。
	RetentionDays int64      `toml:"retention_days"`
	CLS           LoggingCLS `toml:"cls"`
}

// LoggingCLS 是 [logging.cls]：把各服务警告及以上的日志和关键事件上报到腾讯云日志服务。
type LoggingCLS struct {
	Enabled bool `toml:"enabled"`
	// Endpoint 是日志服务的上报域名，例如 ap-guangzhou.cls.tencentcs.com。
	Endpoint string `toml:"endpoint"`
	// TopicID 是日志主题 moox-service 的 ID。
	TopicID string `toml:"topic_id"`
	// MinLevel 是上报的最低级别：info、warn（默认）、error。
	MinLevel string `toml:"min_level"`
}

func (l *Logging) validate() error {
	if l.RetentionDays == 0 {
		l.RetentionDays = DefaultLogRetentionDays
	}
	if l.RetentionDays < 1 || (l.RetentionDays > 3600 && l.RetentionDays != 3640) {
		return errors.New("config_invalid: logging.retention_days must be 1..3600, or 3640 for permanent")
	}
	c := &l.CLS
	c.Endpoint, c.TopicID = strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.TopicID)
	c.MinLevel = strings.ToLower(strings.TrimSpace(c.MinLevel))
	if c.MinLevel == "" {
		c.MinLevel = "warn"
	}
	if !slices.Contains([]string{"info", "warn", "error"}, c.MinLevel) {
		return errors.New("config_invalid: logging.cls.min_level must be info, warn or error")
	}
	if !c.Enabled {
		return nil
	}
	for name, value := range map[string]string{"endpoint": c.Endpoint, "topic_id": c.TopicID} {
		if value == "" {
			return fmt.Errorf("config_invalid: logging.cls.%s is required when logging.cls.enabled is true", name)
		}
	}
	if strings.Contains(c.Endpoint, "/") || strings.ContainsAny(c.Endpoint, " \t") {
		return errors.New("config_invalid: logging.cls.endpoint 只填域名，例如 ap-guangzhou.cls.tencentcs.com")
	}
	return nil
}

// Overrides 是 [overrides."<组件>"."<配置文件名>"]：每行“键路径 = 值”，部署渲染组件配置之后逐项覆盖，键路径不存在就报错。
// 只做最小版本：值只能是字符串、整数、小数或布尔，不支持删键、不支持数组元素。
type Overrides map[string]map[string]map[string]any

func (o Overrides) validate() error {
	if len(o) == 0 {
		return nil
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	// 少写一层时（[overrides."组件"] 下直接写键路径，或 [overrides] 下写 "组件" = 值），解析库把标量解进表类型会留下 nil，
	// 也不算没解析的键：这里必须拦下，否则这条调参悄悄不生效。
	layout := `写成 [overrides."<组件>"."<配置文件名>"] 下面的 "<键路径>" = 值`
	for component, files := range o {
		if _, ok := catalog.Component(component); !ok {
			return fmt.Errorf("config_invalid: [overrides.%q] 的组件不在服务目录里", component)
		}
		if files == nil {
			return fmt.Errorf("config_invalid: [overrides] 下的 %q 不是表：%s", component, layout)
		}
		for file, values := range files {
			if file == "" || file != path.Base(file) || file == "." || file == ".." || strings.ContainsAny(file, "\\\x00") {
				return fmt.Errorf("config_invalid: [overrides.%q.%q] 的配置文件名只写文件名，不带目录", component, file)
			}
			if values == nil {
				return fmt.Errorf("config_invalid: [overrides.%q] 下的 %q 不是表：少写了配置文件名这一层，%s", component, file, layout)
			}
			for key, value := range values {
				where := fmt.Sprintf("[overrides.%q.%q] 的 %q", component, file, key)
				if key == "" || slices.Contains(strings.Split(key, "."), "") {
					return fmt.Errorf("config_invalid: %s 不是合法的键路径（用点号分隔、不能有空段）", where)
				}
				switch value.(type) {
				case string, int64, float64, bool:
				default:
					return fmt.Errorf("config_invalid: %s 的值只能是字符串、整数、小数或布尔", where)
				}
			}
		}
	}
	return nil
}
