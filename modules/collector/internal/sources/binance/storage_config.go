package binance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	marketfiles "github.com/mooyang-code/moox/modules/collector/config/markets"
	"gopkg.in/yaml.v3"
)

const embeddedBinanceSourceConfig = "crypto/sources/binance.yaml"

// APIConfig 是按顺序故障转移的币安官方端点；没有写时沿用客户端内置的默认域名。
type APIConfig struct {
	SpotBaseURLs []string `yaml:"spot_base_urls"`
	SwapBaseURLs []string `yaml:"swap_base_urls"`
}

// InstTypeForMarket converts the scheduler's lower-case market label to the
// Binance API product type used by the existing collectors.
func InstTypeForMarket(market string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(market)) {
	case "spot", "现货":
		return InstTypeSPOT, nil
	case "swap", "futures", "future", "perpetual", "合约", "永续合约":
		return InstTypeSWAP, nil
	default:
		return "", fmt.Errorf("unsupported market type %q", market)
	}
}

type binanceSourceConfig struct {
	App struct {
		ID          string `yaml:"id"`
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Type        string `yaml:"type"`
	} `yaml:"app"`
	API    APIConfig    `yaml:"api"`
	Stream StreamConfig `yaml:"stream"`
}

// ResolveStreamConfig 读取 WS 推送的地址与限额（binance.yaml 的 stream 段），没写的字段用内置默认。
func ResolveStreamConfig() (StreamConfig, error) {
	source, err := loadBinanceSourceConfig()
	if err != nil {
		return StreamConfig{}, err
	}
	return source.Stream.withDefaults(), nil
}

func ResolveAPIConfig() (APIConfig, error) {
	source, err := loadBinanceSourceConfig()
	if err != nil {
		return APIConfig{}, err
	}
	return source.API, nil
}

func loadBinanceSourceConfig() (*binanceSourceConfig, error) {
	if explicit := strings.TrimSpace(os.Getenv("MOOX_STORAGE_MARKET_CONFIG")); explicit != "" {
		file, err := os.Open(explicit)
		if err == nil {
			defer file.Close()
			return decodeBinanceSourceConfig(file)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("MOOX_STORAGE_MARKET_CONFIG: %w", err)
		}
	}
	file, err := marketfiles.FS.Open(embeddedBinanceSourceConfig)
	if err != nil {
		return nil, fmt.Errorf("open embedded %s: %w", embeddedBinanceSourceConfig, err)
	}
	defer file.Close()
	return decodeBinanceSourceConfig(file)
}

func decodeBinanceSourceConfig(reader io.Reader) (*binanceSourceConfig, error) {
	var source binanceSourceConfig
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	if err := decoder.Decode(&source); err != nil {
		return nil, err
	}
	return &source, nil
}
