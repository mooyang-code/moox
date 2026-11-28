package markets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"
)

// SourceFile 是 config/markets/<市场>/sources/<数据源>.yaml 的统一解码结构。
// 三段核心配置均可选：provider（K 线/附加字段运行参数与限速）、api（各品种故障转移地址）、stream（WS 推送参数）。
// 严格校验未知字段，不再靠嗅探区分文件格式。
type SourceFile struct {
	Provider *SourceProviderConfig `yaml:"provider"`
	API      *SourceAPIConfig      `yaml:"api"`
	Stream   *SourceStreamConfig   `yaml:"stream"`
}

// SourceProviderConfig 是数据源的 K 线或附加字段运行参数与限速配置。
type SourceProviderConfig struct {
	ID           string              `yaml:"id"`
	Market       string              `yaml:"market"`
	EnabledFeeds []string            `yaml:"enabled_feeds"`
	Hosts        []string            `yaml:"hosts"`
	Kline        SourceKlineConfig   `yaml:"kline"`
	RateLimit    SourceRateLimitFile `yaml:"rate_limit"`
}

// SourceKlineConfig 是数据源配置文件里的 K 线一段。
type SourceKlineConfig struct {
	Endpoint          string `yaml:"endpoint"`
	MaxBarsPerRequest int    `yaml:"max_bars_per_request"`
}

// SourceRateLimitFile 是数据源配置文件里的限速一段。
type SourceRateLimitFile struct {
	Scope             string  `yaml:"scope"`
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
	MaxConcurrent     int     `yaml:"max_concurrent"`
	Cooldown          string  `yaml:"cooldown"`
	RequestTimeout    string  `yaml:"request_timeout"`
}

// SourceAPIConfig 是各品种按顺序故障转移的官方 HTTP 端点。
type SourceAPIConfig struct {
	SpotBaseURLs []string `yaml:"spot_base_urls"`
	SwapBaseURLs []string `yaml:"swap_base_urls"`
}

// SourceStreamConfig 是 WS 推送的地址与连接限额。
type SourceStreamConfig struct {
	SpotEndpoints     []string `yaml:"spot_endpoints"`
	SwapEndpoints     []string `yaml:"swap_endpoints"`
	StreamsPerConn    int      `yaml:"streams_per_conn"`
	StreamsPerMessage int      `yaml:"streams_per_message"`
	MessagesPerSecond int      `yaml:"messages_per_second"`
	MaxConnAgeHours   int      `yaml:"max_conn_age_hours"`
}

// LoadSourceFile 读取并严格解析 config/markets/<marketID>/sources/<id>.yaml（支持 MOOX_MARKET_CONFIG_DIR 覆盖）。
// 文件不存在时 found 为 false、err 为 nil。
func LoadSourceFile(marketID, id string) (SourceFile, bool, error) {
	marketID = normalizeID(marketID)
	id = strings.ToLower(strings.TrimSpace(id))
	if marketID == "" || id == "" {
		return SourceFile{}, false, nil
	}
	name := "sources/" + id + ".yaml"
	path := marketID + "/" + name
	raw, err := ReadFile(marketID, name)
	if errors.Is(err, fs.ErrNotExist) {
		return SourceFile{}, false, nil
	}
	if err != nil {
		return SourceFile{}, false, err
	}
	var file SourceFile
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			return SourceFile{}, false, fmt.Errorf("解析数据源配置 %s：文件内容为空", path)
		}
		return SourceFile{}, false, fmt.Errorf("解析数据源配置 %s：%w", path, err)
	}
	return file, true, nil
}
