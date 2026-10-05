package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// runtimeConfig is the part of moox-factor-mgr's app.yaml the CLI needs: the
// database it edits offline, the Storage Gateway for source columns and the
// Python used to check factor sources.
type runtimeConfig struct {
	DatabasePath  string
	GatewayTarget string
	GatewayNodeID string
	KeyID         string
	HMACKeyFile   string
	PythonBin     string
}

func loadRuntimeConfig(path string) (runtimeConfig, error) {
	if strings.TrimSpace(path) == "" {
		path = "./config/app.yaml"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("resolve config path: %w", err)
	}
	root := filepath.Dir(absolute)
	if filepath.Base(root) == "config" {
		root = filepath.Dir(root)
	}
	config := runtimeConfig{DatabasePath: "./data/factor/factor.db", GatewayTarget: "ip://127.0.0.1:11003", PythonBin: "python3"}
	raw, err := os.ReadFile(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return runtimeConfig{}, fmt.Errorf("read config %s: %w", path, err)
		}
		return runtimeConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var source struct {
		Database struct {
			Path string `yaml:"path"`
		} `yaml:"database"`
		Storage struct {
			GatewayTarget string `yaml:"gateway_target"`
			GatewayNodeID string `yaml:"gateway_node_id"`
			KeyID         string `yaml:"key_id"`
			HMACKeyFile   string `yaml:"hmac_key_file"`
		} `yaml:"storage"`
		Python struct {
			Bin string `yaml:"bin"`
		} `yaml:"python"`
	}
	if err := yaml.Unmarshal(raw, &source); err != nil {
		return runtimeConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if source.Database.Path != "" {
		config.DatabasePath = source.Database.Path
	}
	if source.Storage.GatewayTarget != "" {
		config.GatewayTarget = source.Storage.GatewayTarget
	}
	config.GatewayNodeID, config.KeyID, config.HMACKeyFile = source.Storage.GatewayNodeID, source.Storage.KeyID, source.Storage.HMACKeyFile
	if source.Python.Bin != "" {
		config.PythonBin = source.Python.Bin
	}
	for _, path := range []*string{&config.DatabasePath, &config.HMACKeyFile} {
		if *path != "" && !filepath.IsAbs(*path) {
			*path = filepath.Clean(filepath.Join(root, *path))
		}
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_DB_PATH")); value != "" {
		config.DatabasePath = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_GATEWAY_TARGET")); value != "" {
		config.GatewayTarget = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_GATEWAY_NODE_ID")); value != "" {
		config.GatewayNodeID = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_KEY_ID")); value != "" {
		config.KeyID = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_HMAC_KEY_FILE")); value != "" {
		config.HMACKeyFile = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_PYTHON_BIN")); value != "" {
		config.PythonBin = value
	}
	return config, nil
}

func normalizeCLIList(name string, values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s contains an empty value", name)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}
