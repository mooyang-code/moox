package command

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	dataAccessConfigEnv         = "MOOX_SKILL_CONFIG"
	defaultDataAccessConfigPath = "config/data-access.yaml"
)

type dataAccessConfig struct {
	Version   int                       `yaml:"version"`
	Gateway   dataGatewayConfig         `yaml:"gateway"`
	Storage   dataStorageAuthConfig     `yaml:"storage"`
	DataTypes map[string]dataTypeConfig `yaml:"data_types"`
}

type dataGatewayConfig struct {
	Target     string `yaml:"target"`
	TargetNode string `yaml:"target_node"`
	KeyID      string `yaml:"key_id"`
	Caller     string `yaml:"caller"`
	Secret     string `yaml:"secret"`
}

type dataStorageAuthConfig struct {
	AppID  string `yaml:"app_id"`
	AppKey string `yaml:"app_key"`
}

type dataTypeConfig struct {
	DefaultExchange string                    `yaml:"default_exchange"`
	Exchanges       map[string]exchangeConfig `yaml:"exchanges"`
}

type exchangeConfig struct {
	SpaceID       string            `yaml:"space_id"`
	SeriesTag     string            `yaml:"series_tag"`
	KlineDatasets map[string]string `yaml:"kline_datasets"`
}

type klineSelection struct {
	Exchange  string
	SpaceID   string
	DatasetID string
	SeriesTag string
	Interval  string
}

func resolveDataAccessConfigPath(explicit string) string {
	if path := strings.TrimSpace(explicit); path != "" {
		return path
	}
	if path := strings.TrimSpace(os.Getenv(dataAccessConfigEnv)); path != "" {
		return path
	}
	return defaultDataAccessConfigPath
}

func loadDataAccessConfig(explicitPath string) (dataAccessConfig, error) {
	path := resolveDataAccessConfigPath(explicitPath)
	info, err := os.Lstat(path)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("stat data access config %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return dataAccessConfig{}, fmt.Errorf("data access config %q must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return dataAccessConfig{}, fmt.Errorf("data access config %q must be a regular file", path)
	}
	if info.Mode().Perm() != 0o600 {
		return dataAccessConfig{}, fmt.Errorf("data access config %q must have permission 0600", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("open data access config %q: %w", path, err)
	}
	defer file.Close()

	var cfg dataAccessConfig
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return dataAccessConfig{}, fmt.Errorf("decode data access config %q (unknown or invalid field): %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return dataAccessConfig{}, fmt.Errorf("decode data access config %q: multiple YAML documents are not allowed", path)
		}
		return dataAccessConfig{}, fmt.Errorf("decode data access config %q: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return dataAccessConfig{}, fmt.Errorf("invalid data access config %q: %w", path, err)
	}
	return cfg, nil
}

func (cfg dataAccessConfig) validate() error {
	if cfg.Version != 1 {
		return fmt.Errorf("version must be 1")
	}
	required := []struct {
		name  string
		value string
	}{
		{"gateway.target", cfg.Gateway.Target},
		{"gateway.target_node", cfg.Gateway.TargetNode},
		{"gateway.key_id", cfg.Gateway.KeyID},
		{"gateway.caller", cfg.Gateway.Caller},
		{"gateway.secret", cfg.Gateway.Secret},
		{"storage.app_id", cfg.Storage.AppID},
		{"storage.app_key", cfg.Storage.AppKey},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if _, err := validateNativeGatewayTarget(cfg.Gateway.Target); err != nil {
		return fmt.Errorf("gateway.target %w", err)
	}
	if len(cfg.DataTypes) == 0 {
		return fmt.Errorf("data_types is required")
	}
	for dataType, dataTypeCfg := range cfg.DataTypes {
		if !isNormalizedCatalogKey(dataType) {
			return fmt.Errorf("data type key %q must be lowercase ASCII", dataType)
		}
		defaultExchange := dataTypeCfg.DefaultExchange
		if !isNormalizedCatalogKey(defaultExchange) {
			return fmt.Errorf("data type %q default_exchange must be lowercase ASCII", dataType)
		}
		if len(dataTypeCfg.Exchanges) == 0 {
			return fmt.Errorf("data type %q exchanges is required", dataType)
		}
		if _, ok := dataTypeCfg.Exchanges[defaultExchange]; !ok {
			return fmt.Errorf("data type %q default exchange %q is not configured", dataType, defaultExchange)
		}
		for exchange, exchangeCfg := range dataTypeCfg.Exchanges {
			if !isNormalizedCatalogKey(exchange) {
				return fmt.Errorf("exchange key %q must be lowercase ASCII", exchange)
			}
			if strings.TrimSpace(exchangeCfg.SpaceID) == "" || strings.TrimSpace(exchangeCfg.SeriesTag) == "" {
				return fmt.Errorf("exchange %q space_id and series_tag are required", exchange)
			}
			if len(exchangeCfg.KlineDatasets) == 0 {
				return fmt.Errorf("exchange %q kline_datasets is required", exchange)
			}
			for interval, datasetID := range exchangeCfg.KlineDatasets {
				if !isNormalizedCatalogKey(interval) {
					return fmt.Errorf("interval key %q must be lowercase ASCII", interval)
				}
				if strings.TrimSpace(datasetID) == "" {
					return fmt.Errorf("exchange %q interval %q dataset is required", exchange, interval)
				}
			}
		}
	}
	return nil
}

func (cfg dataAccessConfig) resolveKline(dataType, exchange, interval string) (klineSelection, error) {
	dataType = normalizeCatalogKey(dataType)
	if dataType == "" {
		return klineSelection{}, fmt.Errorf("data type is required")
	}
	dataTypeCfg, ok := cfg.DataTypes[dataType]
	if !ok {
		return klineSelection{}, fmt.Errorf("unsupported data type %q", dataType)
	}
	exchange = normalizeCatalogKey(exchange)
	if exchange == "" {
		exchange = dataTypeCfg.DefaultExchange
	}
	exchangeCfg, ok := dataTypeCfg.Exchanges[exchange]
	if !ok {
		return klineSelection{}, fmt.Errorf("unsupported exchange %q for data type %q", exchange, dataType)
	}
	interval = normalizeCatalogKey(interval)
	datasetID, ok := exchangeCfg.KlineDatasets[interval]
	if !ok {
		return klineSelection{}, fmt.Errorf("unsupported interval %q for data type %q and exchange %q", interval, dataType, exchange)
	}
	return klineSelection{
		Exchange:  exchange,
		SpaceID:   exchangeCfg.SpaceID,
		DatasetID: datasetID,
		SeriesTag: exchangeCfg.SeriesTag,
		Interval:  interval,
	}, nil
}

func normalizeCatalogKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isNormalizedCatalogKey(value string) bool {
	if value == "" || value != normalizeCatalogKey(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}
