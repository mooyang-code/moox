package marketstorage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	InstTypeSPOT = "SPOT"
	InstTypeSWAP = "SWAP"
)

type StorageBinding struct {
	DataSourceID  string          `yaml:"data_source_id"`
	SubjectType   string          `yaml:"subject_type"`
	SubjectMarket string          `yaml:"subject_market"`
	AuthInfo      StorageAuthInfo `yaml:"auth_info"`
}

type StorageAuthInfo struct {
	AppID     string `yaml:"app_id"`
	AppKey    string `yaml:"app_key"`
	Operator  string `yaml:"operator"`
	RequestID string `yaml:"request_id"`
}

type sourceConfig struct {
	// The shared Binance source file also carries the app and api metadata used
	// by the provider runtime. Keep strict decoding here so malformed storage
	// bindings still fail closed without rejecting those unrelated sections.
	App map[string]string `yaml:"app"`
	// API contains provider-specific values that are not consumed by the
	// storage adapter. Keep it structurally permissive because providers can
	// declare scalar endpoints as well as ordered endpoint lists.
	API     map[string]any `yaml:"api"`
	Storage struct {
		Bindings map[string]StorageBinding `yaml:"bindings"`
	} `yaml:"storage"`
}

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

func ResolveStorageBinding(instType string) (StorageBinding, error) {
	key, defaultMarket, err := storageBindingKey(instType)
	if err != nil {
		return StorageBinding{}, err
	}
	path, err := resolveConfigPath()
	if err != nil {
		return StorageBinding{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return StorageBinding{}, err
	}
	defer file.Close()
	var source sourceConfig
	if err := decodeConfig(file, &source); err != nil {
		return StorageBinding{}, err
	}
	binding, ok := source.Storage.Bindings[key]
	if !ok {
		return StorageBinding{}, fmt.Errorf("storage binding %q is not configured in %s", key, path)
	}
	if binding.DataSourceID == "" {
		binding.DataSourceID = "binance"
	}
	if binding.SubjectType == "" {
		binding.SubjectType = "crypto_pair"
	}
	if binding.SubjectMarket == "" {
		binding.SubjectMarket = defaultMarket
	}
	return binding, nil
}

func storageBindingKey(instType string) (string, string, error) {
	switch strings.ToUpper(strings.TrimSpace(instType)) {
	case InstTypeSPOT:
		return "spot", "spot", nil
	case InstTypeSWAP:
		return "swap", "swap", nil
	default:
		return "", "", fmt.Errorf("unsupported product type %q", instType)
	}
}

func decodeConfig(reader io.Reader, target *sourceConfig) error {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	return decoder.Decode(target)
}

func resolveConfigPath() (string, error) {
	candidates := []string{
		strings.TrimSpace(os.Getenv("MOOX_STORAGE_MARKET_CONFIG")),
		"sources/market/binance.yaml",
		"config/sources/market/binance.yaml",
		"configs/sources/market/binance.yaml",
		"modules/collector/configs/sources/market/binance.yaml",
		"modules/collector/configs/scf/stockcn/sources/market/binance.yaml",
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if filepath.IsAbs(candidate) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("market storage binding config was not found")
}
