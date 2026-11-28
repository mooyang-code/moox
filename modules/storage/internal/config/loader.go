// Package config 提供统一的配置加载工具
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/storagepolicy"
	"gopkg.in/yaml.v2"
)

const (
	DefaultViewBackfillPageSize        uint32 = 2000
	MaxViewBackfillPageSize            uint32 = 10000
	DefaultViewBackfillRequestInterval        = 100 * time.Millisecond
	// DefaultViewDeleteOldestBatchSize 是定时删除最旧数据时每批最多删除行数的默认值（2026-10-30 用线上 1m 因子索引实测，单批持锁约 30～50ms）。
	DefaultViewDeleteOldestBatchSize = 2000
	// MaxViewDeleteOldestBatchSize 是定时删除最旧数据时每批行数配置的上限。
	MaxViewDeleteOldestBatchSize = 20000
)

// ConfigLoader 配置加载器
type ConfigLoader struct {
	baseDir string
}

// RuntimeConfig 保存运行时加载出的完整业务配置。
type RuntimeConfig struct {
	Storage StorageConfig `yaml:"storage"`
}

// LoadedConfig 保存单次解析后的业务配置及配置来源路径，供服务端与 CLI 复用。
type LoadedConfig struct {
	ConfigPath string
	Storage    StorageConfig
}

// ViewSettings 保存校验后的 View 建索引与定时删旧行参数。
type ViewSettings struct {
	BackfillPageSize        uint32
	BackfillRequestInterval time.Duration
	DeleteOldestBatchSize   int
}

// Load 单次解析业务配置文件并应用默认值；configPath 为空时直接返回默认配置。
func Load(configPath string) (LoadedConfig, error) {
	trimmed := strings.TrimSpace(configPath)
	var cfg RuntimeConfig
	if trimmed == "" {
		cfg.ApplyDefaults()
		return LoadedConfig{Storage: cfg.Storage}, nil
	}
	loader := NewConfigLoader(filepath.Dir(trimmed))
	if err := loader.LoadConfigWithDefaults(filepath.Base(trimmed), &cfg, cfg.ApplyDefaults); err != nil {
		return LoadedConfig{}, fmt.Errorf("load storage config %s: %w", trimmed, err)
	}
	return LoadedConfig{ConfigPath: trimmed, Storage: cfg.Storage}, nil
}

// LoadPolicy 解析并加载 Storage 策略文件；未提供配置文件时返回内置默认策略。
func (c LoadedConfig) LoadPolicy() (storagepolicy.Policy, error) {
	if c.ConfigPath == "" {
		return storagepolicy.Default(), nil
	}
	policyPath := strings.TrimSpace(c.Storage.PolicyFile)
	if policyPath == "" {
		return storagepolicy.Policy{}, errors.New("storage.policy_file is required")
	}
	if !filepath.IsAbs(policyPath) {
		// MOOX_STORAGE_CONFIG 指向 <package>/config/*.yaml，相对 policy_file 以包根目录为基准。
		policyPath = filepath.Join(filepath.Dir(filepath.Dir(c.ConfigPath)), policyPath)
	}
	return storagepolicy.Load(policyPath)
}

// ViewSettings 校验并返回 View 的回填与定时删除最旧数据配置。
func (c LoadedConfig) ViewSettings() (ViewSettings, error) {
	if c.ConfigPath == "" {
		return ViewSettings{
			BackfillPageSize:        DefaultViewBackfillPageSize,
			BackfillRequestInterval: DefaultViewBackfillRequestInterval,
			DeleteOldestBatchSize:   DefaultViewDeleteOldestBatchSize,
		}, nil
	}
	pageSize := c.Storage.View.BackfillPageSize
	if pageSize == 0 || pageSize > MaxViewBackfillPageSize {
		return ViewSettings{}, fmt.Errorf("storage view backfill_page_size must be between 1 and %d", MaxViewBackfillPageSize)
	}
	interval, err := time.ParseDuration(strings.TrimSpace(c.Storage.View.BackfillRequestInterval))
	if err != nil || interval < 0 {
		return ViewSettings{}, errors.New("storage view backfill_request_interval must be a non-negative duration")
	}
	deleteBatch := c.Storage.View.DeleteOldestBatchSize
	if deleteBatch < 1 || deleteBatch > MaxViewDeleteOldestBatchSize {
		return ViewSettings{}, fmt.Errorf("storage.view.delete_oldest_batch_size 必须在 1～%d 之间，当前为 %d", MaxViewDeleteOldestBatchSize, deleteBatch)
	}
	return ViewSettings{
		BackfillPageSize:        pageSize,
		BackfillRequestInterval: interval,
		DeleteOldestBatchSize:   deleteBatch,
	}, nil
}

// EventBusCredentialFile 返回生效的 EventBus 凭据文件路径（环境变量覆盖优先于配置文件）。
func (c LoadedConfig) EventBusCredentialFile(envOverride string) string {
	if path := strings.TrimSpace(envOverride); path != "" {
		return path
	}
	return strings.TrimSpace(c.Storage.EventBus.CredentialFile)
}

// MetadataDBPath 返回生效的元数据 SQLite 路径（环境变量覆盖优先于配置文件）。
func (c LoadedConfig) MetadataDBPath(envOverride string) string {
	if path := strings.TrimSpace(envOverride); path != "" {
		return path
	}
	if path := strings.TrimSpace(c.Storage.Metadata.Path); path != "" {
		return path
	}
	root := strings.TrimSpace(c.Storage.Root)
	if root == "" {
		root = "./var/storage"
	}
	return filepath.Join(root, "metadata", "storage_metadata.db")
}

// StorageConfig 保存 storage.yaml 中的业务配置。
type StorageConfig struct {
	Root string `yaml:"root"`
	// PolicyFile names storage-policy.json (Dataset retention and View
	// limits), relative to the package root unless absolute.
	PolicyFile string          `yaml:"policy_file"`
	Metadata   StorageMetadata `yaml:"metadata"`
	Devices    StorageDevices  `yaml:"devices"`
	EventBus   StorageEventBus `yaml:"eventbus"`
	View       StorageView     `yaml:"view"`
}

// StorageMetadata 保存元数据存储与种子数据配置。
type StorageMetadata struct {
	Path string `yaml:"path"`
}

// StorageDevices 保存底层存储设备路径配置。
type StorageDevices struct {
	PebblePath    string `yaml:"pebble_path"`
	ViewIndexRoot string `yaml:"view_index_root"`
}

// StorageEventBus 保存事件总线传输配置。
type StorageEventBus struct {
	CredentialFile string `yaml:"credential_file"`
}

// StorageView 保存 View 服务建索引与定时删除的配置。
type StorageView struct {
	// BackfillPageSize 限制首次建索引时每页读取的 Primary 历史行数，页越小点读和写索引的突发越小。
	BackfillPageSize        uint32 `yaml:"backfill_page_size"`
	BackfillRequestInterval string `yaml:"backfill_request_interval"`
	// DeleteOldestBatchSize 是定时删除最旧数据时每批最多删除的行数；未配置时取默认值 2000。
	DeleteOldestBatchSize int `yaml:"delete_oldest_batch_size"`
}

func (c *RuntimeConfig) ApplyDefaults() {
	c.Storage.ApplyDefaults()
}

func (c *StorageConfig) ApplyDefaults() {
	if c.Root == "" {
		c.Root = "./var/storage"
	}
	if c.Metadata.Path == "" {
		c.Metadata.Path = filepath.Join(c.Root, "metadata", "storage_metadata.db")
	}
	if c.Devices.PebblePath == "" {
		c.Devices.PebblePath = filepath.Join(c.Root, "pebble")
	}
	if c.Devices.ViewIndexRoot == "" {
		c.Devices.ViewIndexRoot = filepath.Join(c.Root, "view-indexes")
	}
	if c.View.BackfillPageSize == 0 {
		c.View.BackfillPageSize = DefaultViewBackfillPageSize
	}
	if c.View.DeleteOldestBatchSize == 0 {
		c.View.DeleteOldestBatchSize = DefaultViewDeleteOldestBatchSize
	}
	if strings.TrimSpace(c.View.BackfillRequestInterval) == "" {
		c.View.BackfillRequestInterval = DefaultViewBackfillRequestInterval.String()
	}
}

// ApplyHomeRoot rebases standard local storage paths when deployment overrides
// the storage home directory.
func (c *StorageConfig) ApplyHomeRoot(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	oldRoot := c.Root
	if oldRoot == "" {
		oldRoot = "./var/storage"
	}
	c.Root = root
	c.Metadata.Path = rebaseStoragePath(c.Metadata.Path, oldRoot, root, filepath.Join("metadata", "storage_metadata.db"))
	c.Devices.PebblePath = rebaseStoragePath(c.Devices.PebblePath, oldRoot, root, "pebble")
	c.Devices.ViewIndexRoot = rebaseStoragePath(c.Devices.ViewIndexRoot, oldRoot, root, "view-indexes")
}

func rebaseStoragePath(path string, oldRoot string, newRoot string, defaultRel string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return filepath.Join(newRoot, defaultRel)
	}
	if filepath.IsAbs(path) {
		if filepath.IsAbs(oldRoot) {
			if rebased, ok := rebasePathWithinRoot(path, oldRoot, newRoot); ok {
				return rebased
			}
		}
		return path
	}
	if filepath.IsAbs(oldRoot) {
		return path
	}
	if rebased, ok := rebasePathWithinRoot(path, oldRoot, newRoot); ok {
		return rebased
	}
	return path
}

func rebasePathWithinRoot(path string, oldRoot string, newRoot string) (string, bool) {
	cleanPath := filepath.Clean(path)
	cleanOldRoot := filepath.Clean(oldRoot)
	if cleanPath == cleanOldRoot {
		return newRoot, true
	}
	rel, err := filepath.Rel(cleanOldRoot, cleanPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(newRoot, rel), true
}

// NewConfigLoader 创建配置加载器
func NewConfigLoader(baseDir string) *ConfigLoader {
	return &ConfigLoader{
		baseDir: baseDir,
	}
}

// LoadConfig 通用配置加载函数
func (c *ConfigLoader) LoadConfig(filename string, config interface{}) error {
	// 构建配置文件路径
	configPath := filepath.Join(c.baseDir, filename)

	// 读取配置文件
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败 %s: %w", configPath, err)
	}

	if err := validateStorageSubtreeStrict(yamlFile, config); err != nil {
		return fmt.Errorf("解析YAML失败 %s: %w", configPath, err)
	}
	// 解析YAML到Config结构
	if err := yaml.Unmarshal(yamlFile, config); err != nil {
		return fmt.Errorf("解析YAML失败 %s: %w", configPath, err)
	}

	return nil
}

func validateStorageSubtreeStrict(raw []byte, config interface{}) error {
	var document map[interface{}]interface{}
	if err := yaml.UnmarshalStrict(raw, &document); err != nil {
		return err
	}
	public := map[interface{}]interface{}{}
	for _, key := range []string{"storage"} {
		if value, exists := document[key]; exists {
			public[key] = value
		}
	}
	if len(public) == 0 {
		return nil
	}
	subtree, err := yaml.Marshal(public)
	if err != nil {
		return err
	}
	return yaml.UnmarshalStrict(subtree, config)
}

// LoadConfigWithDefaults 加载配置并应用默认值
func (c *ConfigLoader) LoadConfigWithDefaults(filename string, config interface{}, defaultsFunc func()) error {
	err := c.LoadConfig(filename, config)
	if err != nil {
		return err
	}

	// 应用默认值
	if defaultsFunc != nil {
		defaultsFunc()
	}

	return nil
}
