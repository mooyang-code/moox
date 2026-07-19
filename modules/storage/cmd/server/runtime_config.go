package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	"gopkg.in/yaml.v2"
	"trpc.group/trpc-go/trpc-go/log"
)

func configPathFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-conf=") {
			return strings.TrimPrefix(arg, "-conf=")
		}
		if strings.HasPrefix(arg, "--conf=") {
			return strings.TrimPrefix(arg, "--conf=")
		}
		if (arg == "-conf" || arg == "--conf") && i+1 < len(args) {
			return args[i+1]
		}
	}
	if path := os.Getenv("STORAGE_CONFIG_FILE"); path != "" {
		return path
	}
	if dir := os.Getenv("STORAGE_CONFIG_PATH"); dir != "" {
		return filepath.Join(dir, "trpc_go.yaml")
	}
	return filepath.Join("config", "trpc_go.yaml")
}

func storageConfigPathFromArgs(args []string, frameworkConfigPath string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-storage-conf=") {
			return strings.TrimPrefix(arg, "-storage-conf=")
		}
		if strings.HasPrefix(arg, "--storage-conf=") {
			return strings.TrimPrefix(arg, "--storage-conf=")
		}
		if (arg == "-storage-conf" || arg == "--storage-conf") && i+1 < len(args) {
			return args[i+1]
		}
	}
	if path := os.Getenv("MOOX_STORAGE_CONFIG"); path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_APP_CONFIG"); path != "" {
		return path
	}
	if dir := os.Getenv("STORAGE_CONFIG_PATH"); dir != "" {
		return filepath.Join(dir, "storage.yaml")
	}
	if frameworkConfigPath != "" {
		return filepath.Join(filepath.Dir(frameworkConfigPath), "storage.yaml")
	}
	return filepath.Join("config", "storage.yaml")
}

func loadStorageOptions(configPath string) storagesvc.Options {
	cfg, ok := loadStorageConfig(configPath)
	if !ok {
		return storagesvc.Options{}
	}
	return storageOptionsFromConfig(cfg.Storage)
}

func storageOptionsFromConfig(storage storageconfig.StorageConfig) storagesvc.Options {
	return storagesvc.Options{
		Root:               storage.Root,
		MetadataPath:       storage.Metadata.Path,
		PebblePath:         storage.Devices.PebblePath,
		PrimaryServiceName: storage.Primary.ServiceName,
		ShardID:            storage.Primary.ShardID,
	}
}

func loadRuntimeConfig(configPath string) (storageconfig.RuntimeConfig, error) {
	cfg, ok := loadStorageConfig(configPath)
	if !ok {
		return storageconfig.RuntimeConfig{}, fmt.Errorf("storage configuration is unavailable: %s", configPath)
	}
	return cfg, nil
}

func loadStorageConfig(configPath string) (storageconfig.RuntimeConfig, bool) {
	var cfg storageconfig.RuntimeConfig
	if configPath == "" {
		return cfg, false
	}
	dir := filepath.Dir(configPath)
	file := filepath.Base(configPath)
	loader := storageconfig.NewConfigLoader(dir)
	if err := loader.LoadConfigStrict(file, &cfg); err != nil {
		// Strict loading rejects framework keys in a combined tRPC config and
		// may leave the destination partially populated. Reset it before
		// decoding only the business `storage` document below.
		cfg = storageconfig.RuntimeConfig{}
		encoded, readErr := os.ReadFile(configPath)
		if readErr != nil {
			log.Warnf("加载 storage 配置失败: %v", err)
			return cfg, false
		}
		var document map[interface{}]interface{}
		if yamlErr := yaml.Unmarshal(encoded, &document); yamlErr != nil {
			log.Warnf("解析 storage 配置失败: %v", err)
			return cfg, false
		}
		storageDocument, ok := document["storage"]
		if !ok {
			log.Warnf("storage 配置缺少 storage 节点: %v", err)
			return cfg, false
		}
		combined, marshalErr := yaml.Marshal(map[interface{}]interface{}{"storage": storageDocument})
		if marshalErr != nil || yaml.UnmarshalStrict(combined, &cfg) != nil {
			log.Warnf("加载 combined storage 配置失败: %v", err)
			return cfg, false
		}
	}
	cfg.ApplyDefaults()
	return cfg, true
}

func clearSocketFiles() {
	files, err := filepath.Glob("./*")
	if err != nil {
		log.Errorf("读取目录失败: %v", err)
		return
	}

	for _, file := range files {
		baseFile := filepath.Base(file)
		if strings.HasPrefix(baseFile, "0.0.0.0") || strings.HasPrefix(baseFile, "127.0.0.1") {
			if err := os.Remove(file); err != nil {
				log.Errorf("删除文件 %s 失败: %v", file, err)
			}
		}
	}
}
