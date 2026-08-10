// Package config 提供统一的配置加载工具
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// ConfigLoader 配置加载器
type ConfigLoader struct {
	baseDir string
}

// RuntimeConfig 保存运行时加载出的完整业务配置。
type RuntimeConfig struct {
	Storage StorageConfig `yaml:"storage"`
}

// StorageConfig 保存 storage.yaml 中的业务配置。
type StorageConfig struct {
	Root        string             `yaml:"root"`
	Roles       []string           `yaml:"roles"`
	Metadata    StorageMetadata    `yaml:"metadata"`
	Devices     StorageDevices     `yaml:"devices"`
	Primary     StoragePrimary     `yaml:"primary"`
	EventBus    StorageEventBus    `yaml:"eventbus"`
	View        StorageView        `yaml:"view"`
	Maintenance StorageMaintenance `yaml:"maintenance"`
	Health      StorageHealth      `yaml:"health"`
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
	Consumer       string `yaml:"-"`
	MaxAckPending  int    `yaml:"-"`
	AckWaitMS      int    `yaml:"-"`
}

const (
	StorageViewConsumer      = "storage_view_period_v1"
	StorageViewMaxAckPending = 1
	StorageViewAckWaitMS     = 120000
)

// StorageView 保存 View 服务消费与批处理配置。
type StorageView struct {
	MetadataServiceName     string                 `yaml:"metadata_service_name"`
	PrimaryStoreServiceName string                 `yaml:"primary_store_service_name"`
	IndexServiceName        string                 `yaml:"index_service_name"`
	BatchSize               int                    `yaml:"batch_size"`
	BatchWaitMS             int                    `yaml:"batch_wait_ms"`
	FetchBatch              int                    `yaml:"fetch_batch"`
	MaxWorkers              int                    `yaml:"max_workers"`
	Ordering                string                 `yaml:"ordering"`
	Maintenance             StorageViewMaintenance `yaml:"maintenance"`
	StorageRPC              StorageRPCConfig       `yaml:"storage_rpc"`
}

type StorageRPCConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
}

type StorageViewMaintenance struct {
	Enabled          *bool                        `yaml:"enabled"`
	OwnerID          string                       `yaml:"owner_id"`
	LeaseTTL         string                       `yaml:"lease_ttl"`
	RunBudget        string                       `yaml:"run_budget"`
	PageSize         int                          `yaml:"page_size"`
	MaxEntries       int                          `yaml:"max_entries"`
	TargetEntries    int                          `yaml:"target_entries"`
	MaxPhysicalBytes int64                        `yaml:"max_physical_bytes"`
	MinFreeDiskBytes int64                        `yaml:"min_free_disk_bytes"`
	MinReadyEntries  int                          `yaml:"min_ready_entries"`
	AllowedLag       string                       `yaml:"allowed_lag"`
	OverlapWindow    string                       `yaml:"overlap_window"`
	RemoveGrace      string                       `yaml:"remove_grace"`
	TimeSeries       StorageTimeSeriesMaintenance `yaml:"time_series"`
	Record           StorageRecordMaintenance     `yaml:"record"`
}

// StorageMaintenance owns maintenance that applies to authoritative Storage facts.
type StorageMaintenance struct {
	HostMetricsCleanup HostMetricsCleanupConfig `yaml:"host_metrics_cleanup"`
}

// HostMetricsCleanupConfig controls bounded deletion of expired host metric facts.
type HostMetricsCleanupConfig struct {
	Enabled          *bool    `yaml:"enabled"`
	DatasetIDs       []string `yaml:"dataset_ids"`
	MaxAge           string   `yaml:"max_age"`
	BatchSize        uint32   `yaml:"batch_size"`
	MaxBatchesPerRun int      `yaml:"max_batches_per_run"`
}

func (c HostMetricsCleanupConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// Validate checks cleanup bounds before the timer is registered.
func (c HostMetricsCleanupConfig) Validate() error {
	maxAge, err := time.ParseDuration(c.MaxAge)
	if err != nil || maxAge <= 0 {
		return fmt.Errorf("storage maintenance.host_metrics_cleanup.max_age must be a positive duration")
	}
	if c.BatchSize < 1 || c.BatchSize > 1000 {
		return fmt.Errorf("storage maintenance.host_metrics_cleanup.batch_size must be between 1 and 1000")
	}
	if c.MaxBatchesPerRun <= 0 {
		return fmt.Errorf("storage maintenance.host_metrics_cleanup.max_batches_per_run must be positive")
	}
	if len(c.DatasetIDs) == 0 {
		return fmt.Errorf("storage maintenance.host_metrics_cleanup.dataset_ids must not be empty")
	}
	seen := make(map[string]struct{}, len(c.DatasetIDs))
	for _, datasetID := range c.DatasetIDs {
		datasetID = strings.TrimSpace(datasetID)
		if datasetID == "" {
			return fmt.Errorf("storage maintenance.host_metrics_cleanup.dataset_ids must not contain blanks")
		}
		if _, ok := seen[datasetID]; ok {
			return fmt.Errorf("storage maintenance.host_metrics_cleanup.dataset_ids contains duplicate %q", datasetID)
		}
		seen[datasetID] = struct{}{}
	}
	return nil
}

type StorageTimeSeriesMaintenance struct {
	DefaultKeepDuration string            `yaml:"default_keep_duration"`
	KeepByFreq          map[string]string `yaml:"keep_by_freq"`
}

type StorageRecordMaintenance struct {
	KeepDuration string `yaml:"keep_duration"`
}

func (m StorageViewMaintenance) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// StoragePrimary 保存主存服务访问配置。
type StoragePrimary struct {
	ServiceName string        `yaml:"service_name"`
	NodeID      string        `yaml:"node_id"`
	Outbox      StorageOutbox `yaml:"outbox"`
}

type StorageOutbox struct {
	FlushBatchSize  int `yaml:"flush_batch_size"`
	FlushMaxBytes   int `yaml:"flush_max_bytes"`
	FlushIntervalMS int `yaml:"flush_interval_ms"`
	MaxRows         int `yaml:"max_rows"`
	MaxBytes        int `yaml:"max_bytes"`
	MaxAgeHours     int `yaml:"max_age_hours"`
	BackoffBaseMS   int `yaml:"backoff_base_ms"`
	BackoffMaxMS    int `yaml:"backoff_max_ms"`
}

// StorageHealth controls the lightweight HTTP health endpoint.
type StorageHealth struct {
	Addr string `yaml:"addr"`
}

func (c *RuntimeConfig) ApplyDefaults() {
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
	if c.View.StorageRPC.GatewayTarget == "" {
		c.View.StorageRPC.GatewayTarget = "ip://127.0.0.1:11003"
	}
	if c.View.StorageRPC.KeyID == "" {
		c.View.StorageRPC.KeyID = "storage-view"
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
	cleanup := &c.Maintenance.HostMetricsCleanup
	if cleanup.Enabled == nil {
		enabled := true
		cleanup.Enabled = &enabled
	}
	if len(cleanup.DatasetIDs) == 0 {
		cleanup.DatasetIDs = []string{"host_resource_v1", "host_fs_v1", "host_disk_v1", "host_net_v1"}
	}
	if cleanup.MaxAge == "" {
		cleanup.MaxAge = "48h"
	}
	if cleanup.BatchSize == 0 {
		cleanup.BatchSize = 1000
	}
	if cleanup.MaxBatchesPerRun == 0 {
		cleanup.MaxBatchesPerRun = 10
	}
	if c.EventBus.Consumer == "" {
		c.EventBus.Consumer = StorageViewConsumer
	}
	if c.EventBus.MaxAckPending == 0 {
		c.EventBus.MaxAckPending = StorageViewMaxAckPending
	}
	if c.EventBus.AckWaitMS == 0 {
		c.EventBus.AckWaitMS = StorageViewAckWaitMS
	}
	if c.Primary.Outbox.FlushBatchSize <= 0 {
		c.Primary.Outbox.FlushBatchSize = 100
	}
	if c.Primary.Outbox.FlushMaxBytes <= 0 {
		c.Primary.Outbox.FlushMaxBytes = 1 << 20
	}
	if c.Primary.Outbox.FlushIntervalMS <= 0 {
		c.Primary.Outbox.FlushIntervalMS = 200
	}
	if c.Primary.Outbox.MaxRows <= 0 {
		c.Primary.Outbox.MaxRows = 100000
	}
	if c.Primary.Outbox.MaxBytes <= 0 {
		c.Primary.Outbox.MaxBytes = 256 * 1024 * 1024
	}
	if c.Primary.Outbox.MaxAgeHours <= 0 {
		c.Primary.Outbox.MaxAgeHours = 24
	}
	if c.Primary.Outbox.BackoffBaseMS <= 0 {
		c.Primary.Outbox.BackoffBaseMS = 200
	}
	if c.Primary.Outbox.BackoffMaxMS <= 0 {
		c.Primary.Outbox.BackoffMaxMS = 30000
	}
	if c.View.MetadataServiceName == "" {
		c.View.MetadataServiceName = "trpc.moox.storage.Metadata"
	}
	if c.View.PrimaryStoreServiceName == "" {
		c.View.PrimaryStoreServiceName = "trpc.moox.storage.PrimaryStore"
	}
	if c.View.IndexServiceName == "" {
		c.View.IndexServiceName = "trpc.moox.storage.ViewIndex"
	}
	if c.View.BatchSize <= 0 {
		c.View.BatchSize = 500
	}
	if c.View.BatchWaitMS <= 0 {
		c.View.BatchWaitMS = 200
	}
	if c.View.FetchBatch <= 0 {
		c.View.FetchBatch = 1
	}
	if c.View.MaxWorkers <= 0 {
		c.View.MaxWorkers = 1
	}
	if c.View.Ordering == "" {
		c.View.Ordering = "subject"
	}
	if c.View.Maintenance.Enabled == nil {
		enabled := true
		c.View.Maintenance.Enabled = &enabled
	}
	if c.View.Maintenance.LeaseTTL == "" {
		c.View.Maintenance.LeaseTTL = "90s"
	}
	if c.View.Maintenance.RunBudget == "" {
		c.View.Maintenance.RunBudget = "20s"
	}
	if c.View.Maintenance.PageSize <= 0 {
		c.View.Maintenance.PageSize = 500
	}
	if c.View.Maintenance.MaxEntries <= 0 {
		c.View.Maintenance.MaxEntries = 200000
	}
	if c.View.Maintenance.TargetEntries <= 0 || c.View.Maintenance.TargetEntries >= c.View.Maintenance.MaxEntries {
		c.View.Maintenance.TargetEntries = c.View.Maintenance.MaxEntries * 3 / 4
		if c.View.Maintenance.TargetEntries <= 0 {
			c.View.Maintenance.TargetEntries = 1
		}
	}
	if c.View.Maintenance.MaxPhysicalBytes <= 0 {
		c.View.Maintenance.MaxPhysicalBytes = 512 * 1024 * 1024
	}
	if c.View.Maintenance.MinFreeDiskBytes <= 0 {
		c.View.Maintenance.MinFreeDiskBytes = 1024 * 1024 * 1024
	}
	if c.View.Maintenance.MinReadyEntries <= 0 {
		c.View.Maintenance.MinReadyEntries = 1000
	}
	if c.View.Maintenance.AllowedLag == "" {
		c.View.Maintenance.AllowedLag = "2m"
	}
	if c.View.Maintenance.OverlapWindow == "" {
		c.View.Maintenance.OverlapWindow = "30m"
	}
	if c.View.Maintenance.RemoveGrace == "" {
		c.View.Maintenance.RemoveGrace = "60s"
	}
	if c.View.Maintenance.TimeSeries.DefaultKeepDuration == "" {
		c.View.Maintenance.TimeSeries.DefaultKeepDuration = "7d"
	}
	if c.View.Maintenance.TimeSeries.KeepByFreq == nil {
		c.View.Maintenance.TimeSeries.KeepByFreq = map[string]string{}
	}
	if c.View.Maintenance.TimeSeries.KeepByFreq["1m"] == "" {
		c.View.Maintenance.TimeSeries.KeepByFreq["1m"] = "24h"
	}
	if c.View.Maintenance.TimeSeries.KeepByFreq["1h"] == "" {
		c.View.Maintenance.TimeSeries.KeepByFreq["1h"] = "90d"
	}
	if c.View.Maintenance.TimeSeries.KeepByFreq["1d"] == "" {
		c.View.Maintenance.TimeSeries.KeepByFreq["1d"] = "730d"
	}
	if c.View.Maintenance.Record.KeepDuration == "" {
		c.View.Maintenance.Record.KeepDuration = "30d"
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
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return err
	}
	storage, exists := document["storage"]
	if !exists {
		return nil
	}
	subtree, err := yaml.Marshal(map[interface{}]interface{}{"storage": storage})
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
