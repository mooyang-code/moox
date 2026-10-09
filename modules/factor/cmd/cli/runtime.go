package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/bootstrap"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// runtimeConfig shares the Manager's strict configuration and deployment identity.
type runtimeConfig struct {
	DatabasePath  string
	PythonBin     string
	ConfigPath    string
	GatewayClient gatewayclient.FileConfig
}

func loadRuntimeConfig(path string) (runtimeConfig, error) {
	if strings.TrimSpace(path) == "" {
		path = "./config/app.yaml"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("resolve config path: %w", err)
	}
	manager, err := bootstrap.Load(absolute)
	if err != nil {
		return runtimeConfig{}, err
	}
	root := filepath.Dir(absolute)
	if filepath.Base(root) == "config" {
		root = filepath.Dir(root)
	}
	databasePath := manager.Database.Path
	if strings.TrimSpace(os.Getenv("MOOX_FACTOR_DB_PATH")) == "" && !filepath.IsAbs(databasePath) {
		databasePath = filepath.Clean(filepath.Join(root, databasePath))
	}
	return runtimeConfig{DatabasePath: databasePath, PythonBin: manager.Python.Bin, ConfigPath: absolute, GatewayClient: manager.GatewayClient}, nil
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
