package markets

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	marketfiles "github.com/mooyang-code/moox/modules/collector/config/markets"
	"gopkg.in/yaml.v3"
)

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
	// SCFPlan 是采集计划的默认参数，由 bootstrap 按它的配置结构解析。
	SCFPlan yaml.Node `yaml:"scf_plan"`
}

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

// ReadFile 读一个市场的数据文件（编进二进制的内容），name 形如 route.yaml、sources/tdx.yaml。
func ReadFile(marketID, name string) ([]byte, error) {
	return marketfiles.FS.ReadFile(normalizeID(marketID) + "/" + name)
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
	if config.SubjectEgress != SubjectEgressDirect && config.SubjectEgress != SubjectEgressProxy {
		return Config{}, fmt.Errorf("subject_egress 必须是 %s 或 %s", SubjectEgressDirect, SubjectEgressProxy)
	}
	if job := config.SubjectSync; job != nil && len(job.Sources) == 0 {
		return Config{}, fmt.Errorf("subject_sync.sources 不能为空")
	}
	return config, nil
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
