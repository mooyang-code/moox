package binance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"gopkg.in/yaml.v3"
)

type StorageBinding struct {
	SpaceID         string          `yaml:"space_id"`
	DataSourceID    string          `yaml:"data_source_id"`
	SubjectType     string          `yaml:"subject_type"`
	SubjectMarket   string          `yaml:"subject_market"`
	RecordDatasetID string          `yaml:"record_dataset_id"`
	KlineDatasetID  string          `yaml:"kline_dataset_id"`
	BindDatasetIDs  []string        `yaml:"bind_dataset_ids"`
	AuthInfo        StorageAuthInfo `yaml:"auth_info"`
}

type StorageAuthInfo struct {
	AppID     string `yaml:"app_id"`
	AppKey    string `yaml:"app_key"`
	Operator  string `yaml:"operator"`
	RequestID string `yaml:"request_id"`
}

type APIConfig struct {
	BaseURL     string `yaml:"base_url"`
	SpotBaseURL string `yaml:"spot_base_url"`
	SwapBaseURL string `yaml:"swap_base_url"`
}

type binanceSourceConfig struct {
	API     APIConfig `yaml:"api"`
	Storage struct {
		Bindings map[string]StorageBinding `yaml:"bindings"`
	} `yaml:"storage"`
}

func ResolveAPIConfig() (APIConfig, error) {
	source, err := loadBinanceSourceConfig()
	if err != nil {
		return APIConfig{}, err
	}
	cfg := source.API
	if cfg.SpotBaseURL == "" {
		cfg.SpotBaseURL = cfg.BaseURL
	}
	return cfg, nil
}

func ResolveStorageBinding(instType string) (StorageBinding, error) {
	key, defaultMarket, err := storageBindingKey(instType)
	if err != nil {
		return StorageBinding{}, err
	}

	source, err := loadBinanceSourceConfig()
	if err != nil {
		return StorageBinding{}, err
	}

	binding, ok := source.Storage.Bindings[key]
	if !ok {
		return StorageBinding{}, fmt.Errorf("未配置 Binance 存储绑定: %s", key)
	}
	applyBindingDefaults(&binding, defaultMarket)
	return binding, nil
}

func storageBindingKey(instType string) (string, string, error) {
	switch instType {
	case InstTypeSPOT:
		return "spot", "spot", nil
	case InstTypeSWAP:
		return "swap", "swap", nil
	default:
		return "", "", fmt.Errorf("不支持的产品类型: %s", instType)
	}
}

func applyBindingDefaults(binding *StorageBinding, subjectMarket string) {
	if binding.DataSourceID == "" {
		binding.DataSourceID = "binance"
	}
	if binding.SubjectType == "" {
		binding.SubjectType = "crypto_pair"
	}
	if binding.SubjectMarket == "" {
		binding.SubjectMarket = subjectMarket
	}
	binding.BindDatasetIDs = appendMissingDatasetIDs(binding.BindDatasetIDs, binding.RecordDatasetID, binding.KlineDatasetID)
}

func appendMissingDatasetIDs(ids []string, defaults ...string) []string {
	out := make([]string, 0, len(ids)+len(defaults))
	seen := make(map[string]struct{}, len(ids)+len(defaults))
	appendID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range ids {
		appendID(id)
	}
	for _, id := range defaults {
		appendID(id)
	}
	return out
}

func loadBinanceSourceConfig() (*binanceSourceConfig, error) {
	path, err := resolveBinanceSourceConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var source binanceSourceConfig
	if err := yaml.Unmarshal(data, &source); err != nil {
		return nil, err
	}
	return &source, nil
}

func resolveBinanceSourceConfigPath() (string, error) {
	for _, candidate := range binanceSourceConfigCandidates() {
		if candidate == "" {
			continue
		}
		if filepath.IsAbs(candidate) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
			continue
		}
		for _, full := range relativeConfigCandidates(candidate) {
			if _, err := os.Stat(full); err == nil {
				return full, nil
			}
		}
	}
	return "", fmt.Errorf("未找到 Binance 数据源配置")
}

func binanceSourceConfigCandidates() []string {
	candidates := []string{}
	if config.LocalAppConfig != nil && config.LocalAppConfig.Sources != nil {
		candidates = append(candidates, binanceConfigPaths(config.LocalAppConfig.Sources.Market)...)
	}

	if data, err := os.ReadFile("modules/collector/configs/config.yaml"); err == nil {
		cfg := config.DefaultConfig()
		if err := yaml.Unmarshal(data, cfg); err == nil && cfg.Sources != nil {
			candidates = append(candidates, binanceConfigPaths(cfg.Sources.Market)...)
		}
	}

	defaultCfg := config.DefaultConfig()
	if defaultCfg.Sources != nil {
		candidates = append(candidates, binanceConfigPaths(defaultCfg.Sources.Market)...)
	}
	candidates = append(candidates, "modules/collector/configs/sources/market/binance.yaml")
	return dedupeStrings(candidates)
}

func relativeConfigCandidates(candidate string) []string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	var out []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, prefix := range []string{"", "modules/collector", "modules/collector/configs"} {
			out = append(out, filepath.Clean(filepath.Join(dir, prefix, candidate)))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return dedupeStrings(out)
}

func binanceConfigPaths(sources []config.SourceConfig) []string {
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		if strings.EqualFold(source.Name, "binance") {
			out = append(out, source.Config)
		}
	}
	return out
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
