package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"gopkg.in/yaml.v3"
)

type runtimeConfig struct {
	DatabasePath   string
	GatewayTarget  string
	GatewayNodeID  string
	KeyID          string
	HMACKeyFile    string
	PythonBin      string
	WorkerPath     string
	FactorsDir     string
	PythonWorkers  int
	ReadWorkers    int
	ReadTimeout    time.Duration
	WriteBatchRows int
	TaskTimeout    time.Duration
	root           string
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
	config := runtimeConfig{
		DatabasePath: "./data/factor/factor.db", GatewayTarget: "ip://127.0.0.1:11003",
		PythonBin: "python3", WorkerPath: "./pyworker/worker.py", FactorsDir: "./factors",
		PythonWorkers: 1, ReadWorkers: 4, ReadTimeout: 20 * time.Second,
		WriteBatchRows: 1000, TaskTimeout: 30 * time.Second, root: root,
	}
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
		ArtifactsDir string `yaml:"artifacts_dir"`
		Engine       struct {
			PythonBin      string `yaml:"python_bin"`
			WorkerPath     string `yaml:"worker_path"`
			FactorsDir     string `yaml:"factors_dir"`
			PythonWorkers  int    `yaml:"python_workers"`
			ReadWorkers    int    `yaml:"read_workers"`
			ReadTimeoutMS  int    `yaml:"read_timeout_ms"`
			WriteBatchRows int    `yaml:"write_batch_rows"`
			TaskTimeoutMS  int    `yaml:"task_timeout_ms"`
		} `yaml:"engine"`
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
	if source.ArtifactsDir != "" {
		config.FactorsDir = source.ArtifactsDir
	}
	if source.Engine.FactorsDir != "" {
		config.FactorsDir = source.Engine.FactorsDir
	}
	if source.Engine.PythonBin != "" {
		config.PythonBin = source.Engine.PythonBin
	}
	if source.Engine.WorkerPath != "" {
		config.WorkerPath = source.Engine.WorkerPath
	}
	if source.Engine.PythonWorkers > 0 {
		config.PythonWorkers = source.Engine.PythonWorkers
	}
	if source.Engine.ReadWorkers > 0 {
		config.ReadWorkers = source.Engine.ReadWorkers
	}
	if source.Engine.ReadTimeoutMS > 0 {
		config.ReadTimeout = time.Duration(source.Engine.ReadTimeoutMS) * time.Millisecond
	}
	if source.Engine.WriteBatchRows > 0 {
		config.WriteBatchRows = source.Engine.WriteBatchRows
	}
	if source.Engine.TaskTimeoutMS > 0 {
		config.TaskTimeout = time.Duration(source.Engine.TaskTimeoutMS) * time.Millisecond
	}
	for _, path := range []*string{&config.DatabasePath, &config.WorkerPath, &config.FactorsDir, &config.HMACKeyFile} {
		if *path != "" && !filepath.IsAbs(*path) {
			*path = filepath.Clean(filepath.Join(root, *path))
		}
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_DB_PATH")); value != "" {
		config.DatabasePath = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET")); value != "" {
		config.GatewayTarget = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID")); value != "" {
		config.GatewayNodeID = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_RPC_KEY_ID")); value != "" {
		config.KeyID = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_RPC_HMAC_KEY_FILE")); value != "" {
		config.HMACKeyFile = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ENGINE_PYTHON_BIN")); value != "" {
		config.PythonBin = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ENGINE_WORKER_PATH")); value != "" {
		config.WorkerPath = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ENGINE_FACTORS_DIR")); value != "" {
		config.FactorsDir = value
	}
	return config, nil
}

func (cfg runtimeConfig) pythonProcess() process.Config {
	return process.Config{
		PythonBin: cfg.PythonBin, WorkerPath: cfg.WorkerPath,
		Args:        []string{"--factors-dir", cfg.FactorsDir},
		TaskTimeout: cfg.TaskTimeout, Limits: process.DefaultLimits(),
	}
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
