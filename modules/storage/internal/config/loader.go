// Package config 提供统一的配置加载工具
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v2"
)

// ConfigLoader 配置加载器
type ConfigLoader struct {
	baseDir string
}

// RuntimeConfig 保存运行时加载出的完整业务配置。
type RuntimeConfig struct {
	GatewayClient gatewayclient.FileConfig `yaml:"gateway_client"`
	Storage       StorageConfig            `yaml:"storage"`
}

// StorageConfig 保存 storage.yaml 中的业务配置。
type StorageConfig struct {
	Root string `yaml:"root"`
	// PolicyFile names storage-policy.json (Dataset retention and View
	// limits), relative to the package root unless absolute.
	PolicyFile string          `yaml:"policy_file"`
	Roles      []string        `yaml:"roles"`
	Metadata   StorageMetadata `yaml:"metadata"`
	Devices    StorageDevices  `yaml:"devices"`
	Primary    StoragePrimary  `yaml:"primary"`
	EventBus   StorageEventBus `yaml:"eventbus"`
	View       StorageView     `yaml:"view"`
	Health     StorageHealth   `yaml:"health"`
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

// StorageViewAckWaitMS 是 View 消费者未配置 ack_wait_ms 时的默认值。
const StorageViewAckWaitMS = 120000

// StorageView 保存 View 服务消费与批处理配置。
//
// View 只有一种消费方式：每个数据集一个动态 durable（view_<数据集哈希>），所有 durable 用同一套全局并发参数
// （fetch_batch、max_workers、max_ack_pending，默认都是 1：同一数据集内按顺序处理，避免同一行的两次更新先后颠倒）。
type StorageView struct {
	BatchSize     int `yaml:"batch_size"`
	FetchBatch    int `yaml:"fetch_batch"`
	MaxWorkers    int `yaml:"max_workers"`
	MaxAckPending int `yaml:"max_ack_pending"`
	AckWaitMS     int `yaml:"ack_wait_ms"`
	// BackfillPageSize bounds each Primary history page during a View rebuild.
	// Smaller pages reduce the instantaneous point-read and index-write burst.
	BackfillPageSize        uint32 `yaml:"backfill_page_size"`
	BackfillRequestInterval string `yaml:"backfill_request_interval"`
	// DeleteOldestBatchSize 是定时删除最旧数据时每批最多删除的行数；未配置时取默认值 2000。
	DeleteOldestBatchSize int    `yaml:"delete_oldest_batch_size"`
	RebuildMaxPending     uint64 `yaml:"rebuild_max_pending"`
	RebuildIdleChecks     uint32 `yaml:"rebuild_idle_checks"`
	rebuildMaxPendingSet  bool
	rebuildIdleChecksSet  bool
}

// ValidateConsumer 校验全局消费参数：都为正数，且单次拉取数不超过未确认上限。
func (v StorageView) ValidateConsumer() error {
	if v.FetchBatch < 1 || v.MaxWorkers < 1 || v.MaxAckPending < 1 || v.AckWaitMS < 1 {
		return errors.New("storage view fetch_batch, max_workers, max_ack_pending and ack_wait_ms must be positive")
	}
	if v.FetchBatch > v.MaxAckPending {
		return fmt.Errorf("storage view fetch_batch %d exceeds max_ack_pending %d", v.FetchBatch, v.MaxAckPending)
	}
	return nil
}

func (v StorageView) HasRebuildMaxPendingSetting() bool { return v.rebuildMaxPendingSet }
func (v StorageView) HasRebuildIdleChecksSetting() bool { return v.rebuildIdleChecksSet }

// UnmarshalYAML distinguishes omitted rebuild limits from explicit zero values.
func (v *StorageView) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type plain StorageView
	var decoded plain
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	var raw map[interface{}]interface{}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	*v = StorageView(decoded)
	_, v.rebuildMaxPendingSet = raw["rebuild_max_pending"]
	_, v.rebuildIdleChecksSet = raw["rebuild_idle_checks"]
	return nil
}

// StoragePrimary 保存主存服务访问配置。
type StoragePrimary struct {
	ServiceName string `yaml:"service_name"`
	NodeID      string `yaml:"node_id"`
}

// StorageHealth controls the lightweight HTTP health endpoint.
type StorageHealth struct {
	Addr string `yaml:"addr"`
}

func (c *RuntimeConfig) ApplyDefaults() {
	if c.GatewayClient.Caller == "" {
		c.GatewayClient.Caller = "storage-view"
	}
	if c.GatewayClient.KeyFile == "" {
		c.GatewayClient.KeyFile = "../../secrets/caller-storage-view.key"
	}
	c.Storage.ApplyDefaults()
}

func (c *StorageConfig) ApplyDefaults() {
	if c.Root == "" {
		c.Root = "./var/storage"
	}
	if len(c.Roles) == 0 {
		c.Roles = []string{"primary", "view"}
	}
	if c.Primary.NodeID == "" {
		c.Primary.NodeID = "storage-node-0"
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
	if c.View.BatchSize <= 0 {
		c.View.BatchSize = 500
	}
	if c.View.FetchBatch <= 0 {
		c.View.FetchBatch = 1
	}
	if c.View.MaxWorkers <= 0 {
		c.View.MaxWorkers = 1
	}
	if c.View.MaxAckPending <= 0 {
		c.View.MaxAckPending = c.View.FetchBatch
	}
	if c.View.AckWaitMS <= 0 {
		c.View.AckWaitMS = StorageViewAckWaitMS
	}
	if c.View.BackfillPageSize == 0 {
		c.View.BackfillPageSize = 2000
	}
	if c.View.DeleteOldestBatchSize == 0 {
		c.View.DeleteOldestBatchSize = 2000
	}
	if strings.TrimSpace(c.View.BackfillRequestInterval) == "" {
		c.View.BackfillRequestInterval = "100ms"
	}
	if c.View.RebuildMaxPending == 0 && !c.View.rebuildMaxPendingSet {
		c.View.RebuildMaxPending = 32
	}
	if c.View.RebuildIdleChecks == 0 && !c.View.rebuildIdleChecksSet {
		c.View.RebuildIdleChecks = 3
	}
	if c.Health.Addr == "" {
		c.Health.Addr = ":20210"
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

func (c *StorageConfig) HasRole(role string) bool {
	normalized := strings.ToLower(strings.TrimSpace(role))
	if normalized == "" {
		return false
	}
	for _, candidate := range c.Roles {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == normalized {
			return true
		}
	}
	return false
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
	for _, key := range []string{"storage", "gateway_client"} {
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
