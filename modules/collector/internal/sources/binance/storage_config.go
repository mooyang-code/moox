package binance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

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
	API     APIConfig      `yaml:"api"`
	Storage map[string]any `yaml:"storage"`
}

func ResolveAPIConfig() (APIConfig, error) {
	source, err := loadBinanceSourceConfig()
	if err != nil {
		return APIConfig{}, err
	}
	return source.API, nil
}

func loadBinanceSourceConfig() (*binanceSourceConfig, error) {
	path, err := resolveBinanceSourceConfigPath()
	if err != nil {
		return nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
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

// resolveBinanceSourceConfigPath finds the Binance source file: the SCF
// package root first, then the repository layout used by local runs.
func resolveBinanceSourceConfigPath() (string, error) {
	for _, candidate := range []string{"sources/market/binance.yaml", "configs/sources/market/binance.yaml"} {
		for _, full := range relativeConfigCandidates(candidate) {
			if _, err := os.Stat(full); err == nil {
				return full, nil
			}
		}
	}
	return "", fmt.Errorf("未找到 Binance 数据源配置")
}

func relativeConfigCandidates(candidate string) []string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	var out []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, prefix := range []string{"", "collector", "collector/configs", "modules/collector", "modules/collector/configs"} {
			out = append(out, filepath.Clean(filepath.Join(dir, prefix, candidate)))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return dedupeStrings(out)
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
