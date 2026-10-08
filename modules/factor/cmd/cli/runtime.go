package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// runtimeConfig is the part of moox-factor-mgr's app.yaml the CLI needs: the
// database it edits offline, the gatewayclient for source columns and the
// Python used to check factor sources.
type runtimeConfig struct {
	DatabasePath  string
	GatewayClient gatewayclient.Config
	PythonBin     string
}

// loadRuntimeConfig 读取 factor-mgr 的 app.yaml；相对路径按组件目录（config/ 的上一级）解析，
// 与 factor-mgr 进程的工作目录一致。
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
	config := runtimeConfig{DatabasePath: "./data/factor/factor.db", PythonBin: "python3"}
	raw, err := os.ReadFile(absolute)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var source struct {
		Database struct {
			Path string `yaml:"path"`
		} `yaml:"database"`
		GatewayClient gatewayclient.Config `yaml:"gateway_client"`
		Python        struct {
			Bin string `yaml:"bin"`
		} `yaml:"python"`
	}
	if err := yaml.Unmarshal(raw, &source); err != nil {
		return runtimeConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if source.Database.Path != "" {
		config.DatabasePath = source.Database.Path
	}
	if config.DatabasePath != "" && !filepath.IsAbs(config.DatabasePath) {
		config.DatabasePath = filepath.Clean(filepath.Join(root, config.DatabasePath))
	}
	if config.GatewayClient, err = source.GatewayClient.ResolvePaths(root); err != nil {
		return runtimeConfig{}, err
	}
	if source.Python.Bin != "" {
		config.PythonBin = source.Python.Bin
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_DB_PATH")); value != "" {
		config.DatabasePath = value
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
