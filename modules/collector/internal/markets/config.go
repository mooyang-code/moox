package markets

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	marketfiles "github.com/mooyang-code/moox/modules/collector/config/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/log"
)

// ConfigDirEnv 指定一个目录来覆盖编进二进制的路由与数据源配置文件，布局与 config/markets 相同：
// <目录>/<市场>/route.yaml、<目录>/<市场>/sources/<数据源>.yaml。目录里没有的文件仍读编进去的内容。
// market.yaml 不在覆盖范围内，始终读编进去的。
const ConfigDirEnv = "MOOX_MARKET_CONFIG_DIR"

// ErrUnknownMarket 表示市场没有在 Collector 登记（没有 market.yaml 或没有标的规则）。
var ErrUnknownMarket = errors.New("市场未登记")

// 标的同步请求的出口方式。
const (
	SubjectEgressDirect = "direct"
	SubjectEgressProxy  = "proxy"
)

// EgressProbePublicIP 表示云函数出口探测只取出口公网 IP。
const EgressProbePublicIP = "public_ip"

// Config 是一个市场的数据文件 config/markets/<市场>/market.yaml：全是参数，没有行为。
type Config struct {
	MarketID string `yaml:"market_id"`
	// Currency 是成交额的计价币种；一个市场有多种计价币时不写（crypto）。
	Currency string `yaml:"currency"`
	// AllowedLatenessSeconds 是结果数据集的允许迟到时间默认值，必填。
	AllowedLatenessSeconds int `yaml:"allowed_lateness_seconds"`
	// Ingest 是这个市场 K 线任务的默认采集方式（stream、scf、local），必填；建任务没写采集方式时用它。
	Ingest string `yaml:"ingest"`
	// SeriesTag 是固定的序列标签；不写表示按数据源生成。
	SeriesTag string `yaml:"series_tag"`
	// RealtimeBatchNoCap 为真时实时批次不限标的数。
	RealtimeBatchNoCap bool `yaml:"realtime_batch_no_cap"`
	// EgressProbe 是云函数出口探测的方式：public_ip，或数据源名；不写就不探测。
	EgressProbe string `yaml:"egress_probe"`
	// SubjectEgress 是标的同步请求的出口：direct 直连，proxy 走出口代理。
	SubjectEgress string `yaml:"subject_egress"`
	// SubjectSync 是标的属性同步的默认任务；不写表示没有全市场标的同步。
	SubjectSync *SubjectSync `yaml:"subject_sync"`
	// TradingStatus 是交易状态检测的任务（每个 market_type 一条）；不写表示这个市场不检测。只在这里配，不能被部署配置覆盖。
	TradingStatus []TradingStatusJob `yaml:"trading_status"`
	// RefetchAfterCloseMinutes 是收市后多久把当天整天强制重取一遍（分钟）；不写就不重取。只对有交易时段的市场有意义。
	RefetchAfterCloseMinutes int `yaml:"refetch_after_close_minutes"`
	// SCFPlan 是采集计划的默认参数，由 bootstrap 按它的配置结构解析。
	SCFPlan yaml.Node `yaml:"scf_plan"`
}

// TradingStatusJob 是一个市场类型的交易状态检测：来源 tencent（股票）或 binance（crypto），按 cron 在 timezone 里触发。
type TradingStatusJob struct {
	MarketType string `yaml:"market_type"`
	Source     string `yaml:"source"`
	Cron       string `yaml:"cron"`
	Timezone   string `yaml:"timezone"`
}

// 交易状态检测的来源。
const (
	TradingStatusSourceTencent = "tencent"
	TradingStatusSourceBinance = "binance"
)

// SubjectSync 是标的属性同步的默认任务。
type SubjectSync struct {
	Sources        []string `yaml:"sources"`
	InstrumentType string   `yaml:"instrument_type"`
	Cron           string   `yaml:"cron"`
	Timezone       string   `yaml:"timezone"`
}

// AmountUnit 返回写入 K 线行的成交额单位（计价币种的小写）。
func (c Config) AmountUnit() string { return strings.ToLower(strings.TrimSpace(c.Currency)) }

func normalizeID(marketID string) string { return strings.ToLower(strings.TrimSpace(marketID)) }

var missingOverrides sync.Map

// ReadFile 读一个市场的数据文件（name 形如 route.yaml、sources/tdx.yaml）：
// market.yaml 始终读编进二进制的内容，不在目录覆盖范围内；
// 其余文件优先读环境变量 MOOX_MARKET_CONFIG_DIR 指定的覆盖目录，没有时回退到编进二进制的内容并按路径记录一次警告。
func ReadFile(marketID, name string) ([]byte, error) {
	id := normalizeID(marketID)
	if id == "" || strings.TrimSpace(name) == "" {
		return nil, fs.ErrNotExist
	}
	if name == "market.yaml" {
		return marketfiles.FS.ReadFile(id + "/" + name)
	}
	override := ""
	if dir := strings.TrimSpace(os.Getenv(ConfigDirEnv)); dir != "" {
		override = filepath.Join(dir, id, filepath.FromSlash(name))
		raw, err := os.ReadFile(override)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return raw, err
		}
	}
	raw, err := marketfiles.FS.ReadFile(id + "/" + name)
	if err == nil && override != "" {
		if _, warned := missingOverrides.LoadOrStore(override, struct{}{}); !warned {
			log.Warnf("环境变量 %s 指定的目录里没有 %s，改用编进二进制的 %s/%s", ConfigDirEnv, override, id, name)
		}
	}
	return raw, err
}

// configs 缓存解析好的 market.yaml：内容编在二进制里，进程内不会变。
var configs sync.Map

// ConfigFor 返回市场的参数；没有 market.yaml 的市场返回 ErrUnknownMarket。
func ConfigFor(marketID string) (Config, error) {
	id := normalizeID(marketID)
	if cached, ok := configs.Load(id); ok {
		return cached.(Config), nil
	}
	raw, err := ReadFile(id, "market.yaml")
	if errors.Is(err, fs.ErrNotExist) || id == "" {
		return Config{}, fmt.Errorf("%w：%q 没有 market.yaml", ErrUnknownMarket, marketID)
	}
	if err != nil {
		return Config{}, err
	}
	config, err := parseConfig(id, raw)
	if err != nil {
		return Config{}, fmt.Errorf("市场 %s 的 market.yaml：%w", id, err)
	}
	configs.Store(id, config)
	return config, nil
}

func parseConfig(marketID string, raw []byte) (Config, error) {
	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(config.MarketID) != marketID {
		return Config{}, fmt.Errorf("market_id 必须是 %q", marketID)
	}
	if config.AllowedLatenessSeconds <= 0 {
		return Config{}, fmt.Errorf("allowed_lateness_seconds 必填且为正")
	}
	if err := domain.ValidateIngest(config.Ingest); err != nil {
		return Config{}, fmt.Errorf("ingest：%w", err)
	}
	if config.SubjectEgress != SubjectEgressDirect && config.SubjectEgress != SubjectEgressProxy {
		return Config{}, fmt.Errorf("subject_egress 必须是 %s 或 %s", SubjectEgressDirect, SubjectEgressProxy)
	}
	if job := config.SubjectSync; job != nil && len(job.Sources) == 0 {
		return Config{}, fmt.Errorf("subject_sync.sources 不能为空")
	}
	for _, job := range config.TradingStatus {
		if job.MarketType == "" || job.Cron == "" || job.Timezone == "" ||
			(job.Source != TradingStatusSourceTencent && job.Source != TradingStatusSourceBinance) {
			return Config{}, fmt.Errorf("trading_status 每条都要写 market_type、cron、timezone，source 只能是 %s 或 %s", TradingStatusSourceTencent, TradingStatusSourceBinance)
		}
	}
	if config.RefetchAfterCloseMinutes < 0 {
		return Config{}, fmt.Errorf("refetch_after_close_minutes 不能为负")
	}
	return config, nil
}

// DefaultIngest 返回空间（与市场同名）K 线任务的默认采集方式；空间不是登记的市场时返回空串。
// 它注入给任务仓库（store.Options.DefaultIngest），是市场默认值的唯一出口。
func DefaultIngest(spaceID string) string {
	config, err := ConfigFor(spaceID)
	if err != nil {
		return ""
	}
	return config.Ingest
}

// IDs 返回有 market.yaml 的全部市场，按 ID 排序。
func IDs() []string {
	entries, err := fs.ReadDir(marketfiles.FS, ".")
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := fs.Stat(marketfiles.FS, entry.Name()+"/market.yaml"); err == nil {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids
}
