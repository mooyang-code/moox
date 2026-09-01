// Package config 提供统一的配置加载工具
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
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
	// MaxAckPending bounds the number of View events that may be in flight.
	// The subject dispatcher still serializes events for the same dataset/view,
	// so different dataset subjects can make progress concurrently.
	MaxAckPending int `yaml:"max_ack_pending"`
	AckWaitMS     int `yaml:"-"`
}

const (
	StorageViewConsumer         = events.StorageViewKlineConsumer
	StorageDefaultMaxAckPending = 8
	StorageViewMaxAckPending    = 256
	StorageViewAckWaitMS        = 120000
)

// StorageView 保存 View 服务消费与批处理配置。
type StorageView struct {
	MetadataServiceName      string `yaml:"metadata_service_name"`
	PrimaryStoreServiceName  string `yaml:"primary_store_service_name"`
	IndexServiceName         string `yaml:"index_service_name"`
	BatchSize                int    `yaml:"batch_size"`
	BatchWaitMS              int    `yaml:"batch_wait_ms"`
	FetchBatch               int    `yaml:"fetch_batch"`
	MaxWorkers               int    `yaml:"max_workers"`
	Ordering                 string `yaml:"ordering"`
	MaintenanceCheckInterval string `yaml:"maintenance_check_interval"`
	// RebuildLookback is the wall-clock fallback for legacy Views without a
	// frequency-specific completed-bar target.
	RebuildLookback        string                         `yaml:"rebuild_lookback"`
	RebuildLookbackPeriods map[string]uint64              `yaml:"rebuild_lookback_periods"`
	MaxViewFileBytes       int64                          `yaml:"max_view_file_bytes"`
	RebuildMaxPending      uint64                         `yaml:"rebuild_max_pending"`
	RebuildIdleChecks      uint32                         `yaml:"rebuild_idle_checks"`
	ConsumerPartitions     []StorageViewConsumerPartition `yaml:"consumer_partitions"`
	StorageRPC             StorageRPCConfig               `yaml:"storage_rpc"`
	MaintenancePolicyFile  string                         `yaml:"maintenance_policy_file"`
	maxViewFileBytesSet    bool
	rebuildMaxPendingSet   bool
	rebuildIdleChecksSet   bool
}

type ViewMaintenancePolicyOverride struct {
	SpaceID                string `json:"space_id"`
	ViewID                 string `json:"view_id"`
	RebuildLookbackPeriods uint64 `json:"rebuild_lookback_periods"`
	MaxPeriodsPerSeries    uint64 `json:"max_periods_per_series"`
	MaxViewFileBytes       int64  `json:"max_view_file_bytes"`
}

type ViewMaintenancePolicy struct {
	MaintenanceCheckInterval string                          `json:"maintenance_check_interval"`
	RebuildLookbackPeriods   uint64                          `json:"rebuild_lookback_periods"`
	MaxPeriodsPerSeries      uint64                          `json:"max_periods_per_series"`
	MaxViewFileBytes         int64                           `json:"max_view_file_bytes"`
	SystemMonitor            ViewMaintenancePolicyOverride   `json:"system_monitor"`
	Views                    []ViewMaintenancePolicyOverride `json:"views"`
}

func (p ViewMaintenancePolicy) ResolvePolicy(spaceID, viewID string) ViewMaintenancePolicy {
	resolved := p
	apply := func(override ViewMaintenancePolicyOverride) {
		if override.RebuildLookbackPeriods > 0 {
			resolved.RebuildLookbackPeriods = override.RebuildLookbackPeriods
		}
		if override.MaxPeriodsPerSeries > 0 {
			resolved.MaxPeriodsPerSeries = override.MaxPeriodsPerSeries
		}
		if override.MaxViewFileBytes > 0 {
			resolved.MaxViewFileBytes = override.MaxViewFileBytes
		}
	}
	if strings.TrimSpace(spaceID) == "moox_system" {
		apply(p.SystemMonitor)
	}
	for _, override := range p.Views {
		if strings.TrimSpace(override.SpaceID) == strings.TrimSpace(spaceID) && strings.TrimSpace(override.ViewID) == strings.TrimSpace(viewID) {
			apply(override)
		}
	}
	return resolved
}

func (p ViewMaintenancePolicy) Validate() error {
	interval, err := time.ParseDuration(strings.TrimSpace(p.MaintenanceCheckInterval))
	if err != nil || interval < 30*time.Second {
		return errors.New("maintenance_check_interval must be at least 30s")
	}
	if p.RebuildLookbackPeriods == 0 || p.RebuildLookbackPeriods > 1_000_000 || p.MaxPeriodsPerSeries == 0 || p.MaxPeriodsPerSeries > 1_000_000 || p.MaxPeriodsPerSeries <= p.RebuildLookbackPeriods || p.MaxViewFileBytes <= 0 {
		return errors.New("view maintenance limits are invalid")
	}
	seen := make(map[string]struct{}, len(p.Views))
	if p.SystemMonitor.SpaceID != "" || p.SystemMonitor.ViewID != "" {
		return errors.New("system_monitor override must not set space_id or view_id")
	}
	systemResolved := p.ResolvePolicy("moox_system", "")
	if systemResolved.MaxPeriodsPerSeries <= systemResolved.RebuildLookbackPeriods || systemResolved.MaxViewFileBytes <= 0 {
		return errors.New("system_monitor override has invalid limits")
	}
	for _, override := range p.Views {
		if strings.TrimSpace(override.SpaceID) == "" || strings.TrimSpace(override.ViewID) == "" {
			return errors.New("view maintenance override requires space_id and view_id")
		}
		key := strings.TrimSpace(override.SpaceID) + "/" + strings.TrimSpace(override.ViewID)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate view maintenance override %s", key)
		}
		seen[key] = struct{}{}
		if override.RebuildLookbackPeriods > 1_000_000 || override.MaxPeriodsPerSeries > 1_000_000 || override.MaxViewFileBytes < 0 {
			return fmt.Errorf("view maintenance override %s is out of range", key)
		}
		resolved := p.ResolvePolicy(override.SpaceID, override.ViewID)
		if resolved.MaxPeriodsPerSeries <= resolved.RebuildLookbackPeriods || resolved.MaxViewFileBytes <= 0 {
			return fmt.Errorf("view maintenance override %s has invalid limits", key)
		}
	}
	return nil
}

func LoadViewMaintenancePolicy(path string) (ViewMaintenancePolicy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ViewMaintenancePolicy{}, fmt.Errorf("read view maintenance policy: %w", err)
	}
	var policy ViewMaintenancePolicy
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return ViewMaintenancePolicy{}, fmt.Errorf("decode view maintenance policy: %w", err)
	}
	if strings.TrimSpace(policy.MaintenanceCheckInterval) == "" {
		policy.MaintenanceCheckInterval = "1m"
	}
	if policy.RebuildLookbackPeriods == 0 {
		policy.RebuildLookbackPeriods = 1000
	}
	if policy.MaxPeriodsPerSeries == 0 {
		policy.MaxPeriodsPerSeries = 2000
	}
	if policy.MaxViewFileBytes == 0 {
		policy.MaxViewFileBytes = 1 << 30
	}
	if err := policy.Validate(); err != nil {
		return ViewMaintenancePolicy{}, err
	}
	return policy, nil
}

// StorageViewConsumerRoute identifies the Dataset subjects owned by a
// consumer partition. A route may use dataset_ids: ["*"] as a metadata
// inventory expansion point; runtime rendering expands it to concrete
// datasets and always gives explicit routes precedence.
type StorageViewConsumerRoute struct {
	SpaceID    string   `yaml:"space_id"`
	DatasetIDs []string `yaml:"dataset_ids"`
}

// StorageViewConsumerPartition owns one independent JetStream durable and
// its delivery budget. A partition may contain several explicit routes, but a
// Dataset route must occur in exactly one partition.
type StorageViewConsumerPartition struct {
	ID               string                     `yaml:"id"`
	Durable          string                     `yaml:"durable"`
	SpaceID          string                     `yaml:"space_id"` // shorthand for one route
	DatasetIDs       []string                   `yaml:"dataset_ids"`
	Routes           []StorageViewConsumerRoute `yaml:"routes"`
	AckWaitMS        int                        `yaml:"ack_wait_ms"`
	FetchBatch       int                        `yaml:"fetch_batch"`
	MaxWorkers       int                        `yaml:"max_workers"`
	MaxAckPending    int                        `yaml:"max_ack_pending"`
	Ordering         string                     `yaml:"ordering"`
	DeliverPolicy    string                     `yaml:"deliver_policy"`
	MaxRetryAttempts int                        `yaml:"max_retry_attempts"`
}

type StorageViewConsumerDataset struct {
	SpaceID   string
	DatasetID string
}

func (p StorageViewConsumerPartition) normalizedRoutes() []StorageViewConsumerRoute {
	routes := make([]StorageViewConsumerRoute, 0, len(p.Routes)+1)
	if strings.TrimSpace(p.SpaceID) != "" || len(p.DatasetIDs) != 0 {
		routes = append(routes, StorageViewConsumerRoute{SpaceID: p.SpaceID, DatasetIDs: p.DatasetIDs})
	}
	routes = append(routes, p.Routes...)
	return routes
}

func (p StorageViewConsumerPartition) Datasets() []StorageViewConsumerDataset {
	var out []StorageViewConsumerDataset
	for _, route := range p.normalizedRoutes() {
		spaceID := strings.TrimSpace(route.SpaceID)
		for _, datasetID := range route.DatasetIDs {
			out = append(out, StorageViewConsumerDataset{SpaceID: spaceID, DatasetID: strings.TrimSpace(datasetID)})
		}
	}
	return out
}

func (v *StorageView) applyConsumerPartitionDefaults() {
	if len(v.ConsumerPartitions) == 0 {
		v.ConsumerPartitions = []StorageViewConsumerPartition{
			{ID: "kline", Durable: events.StorageViewKlineConsumer, Routes: []StorageViewConsumerRoute{{SpaceID: "crypto_market", DatasetIDs: []string{"binance_spot_kline_1m"}}}, FetchBatch: 4, MaxWorkers: 2, MaxAckPending: 16},
			{ID: "factor", Durable: events.StorageViewFactorConsumer, Routes: []StorageViewConsumerRoute{{SpaceID: "crypto_market", DatasetIDs: []string{"binance_spot_kline_1m_factor"}}}, FetchBatch: 16, MaxWorkers: 8, MaxAckPending: 128},
			{ID: "system_metrics", Durable: events.StorageViewMetricsConsumer, Routes: []StorageViewConsumerRoute{{SpaceID: "moox_system", DatasetIDs: []string{"moox_service_metrics"}}}, FetchBatch: 16, MaxWorkers: 4, MaxAckPending: 64},
			{ID: "misc", Durable: events.StorageViewMiscConsumer, Routes: []StorageViewConsumerRoute{
				{SpaceID: "moox_system", DatasetIDs: []string{"host_disk_v1", "host_fs_v1", "host_net_v1", "host_resource_v1"}},
				{SpaceID: "stock_cn", DatasetIDs: []string{"financial_statement_metric", "financial_summary", "index_kline", "stock_cn_instruments", "stock_cn_kline"}},
			}, FetchBatch: 4, MaxWorkers: 2, MaxAckPending: 16},
		}
	}
	for i := range v.ConsumerPartitions {
		p := &v.ConsumerPartitions[i]
		p.ID = strings.TrimSpace(p.ID)
		p.Durable = strings.TrimSpace(p.Durable)
		if p.AckWaitMS <= 0 {
			p.AckWaitMS = StorageViewAckWaitMS
		}
		if p.FetchBatch <= 0 {
			p.FetchBatch = 1
		}
		if p.MaxWorkers <= 0 {
			p.MaxWorkers = 1
		}
		if p.MaxAckPending <= 0 {
			p.MaxAckPending = p.FetchBatch
		}
		if strings.TrimSpace(p.Ordering) == "" {
			p.Ordering = "dataset"
		}
		p.Ordering = strings.ToLower(strings.TrimSpace(p.Ordering))
		if strings.TrimSpace(p.DeliverPolicy) == "" {
			p.DeliverPolicy = "new"
		}
		p.DeliverPolicy = strings.ToLower(strings.TrimSpace(p.DeliverPolicy))
		if p.MaxRetryAttempts == 0 {
			p.MaxRetryAttempts = -1
		}
	}
}

// ValidateConsumerPartitions validates partition topology. When managed is
// non-nil it additionally requires an exact one-to-one assignment with the
// View source Dataset inventory.
func (v StorageView) ValidateConsumerPartitions(managed []StorageViewConsumerDataset) error {
	if len(v.ConsumerPartitions) == 0 {
		return errors.New("storage view consumer_partitions must not be empty")
	}
	partitionIDs := make(map[string]struct{}, len(v.ConsumerPartitions))
	durables := make(map[string]struct{}, len(v.ConsumerPartitions))
	routes := make(map[string]string)
	for _, partition := range v.ConsumerPartitions {
		id := strings.TrimSpace(partition.ID)
		durable := strings.TrimSpace(partition.Durable)
		if id == "" || durable == "" {
			return errors.New("storage view consumer partition id and durable are required")
		}
		if !validConsumerName(id) || !validConsumerName(durable) {
			return fmt.Errorf("storage view consumer partition %q has an invalid id or durable name", id)
		}
		if durable != events.StorageViewKlineConsumer && durable != events.StorageViewFactorConsumer && durable != events.StorageViewMetricsConsumer && durable != events.StorageViewMiscConsumer {
			return fmt.Errorf("storage view durable %q is not one of the managed partition durables", durable)
		}
		if _, exists := partitionIDs[id]; exists {
			return fmt.Errorf("storage view consumer partition %q is duplicated", id)
		}
		if _, exists := durables[durable]; exists {
			return fmt.Errorf("storage view durable %q is duplicated", durable)
		}
		partitionIDs[id] = struct{}{}
		durables[durable] = struct{}{}
		if partition.FetchBatch < 1 || partition.MaxWorkers < 1 || partition.MaxAckPending < 1 || partition.AckWaitMS < 1 {
			return fmt.Errorf("storage view consumer partition %q has non-positive delivery settings", id)
		}
		if partition.FetchBatch > partition.MaxAckPending {
			return fmt.Errorf("storage view consumer partition %q fetch_batch %d exceeds max_ack_pending %d", id, partition.FetchBatch, partition.MaxAckPending)
		}
		if partition.Ordering != "" && strings.ToLower(strings.TrimSpace(partition.Ordering)) != "dataset" {
			return fmt.Errorf("storage view consumer partition %q ordering must be dataset", id)
		}
		policy := strings.ToLower(strings.TrimSpace(partition.DeliverPolicy))
		if policy != "" && policy != "all" && policy != "new" {
			return fmt.Errorf("storage view consumer partition %q deliver_policy %q is unsupported", id, partition.DeliverPolicy)
		}
		if partition.MaxRetryAttempts < -1 {
			return fmt.Errorf("storage view consumer partition %q max_retry_attempts must be -1 or positive", id)
		}
		datasets := partition.Datasets()
		if len(datasets) == 0 {
			return fmt.Errorf("storage view consumer partition %q has no Dataset routes", id)
		}
		for _, dataset := range datasets {
			if dataset.SpaceID == "" || dataset.DatasetID == "" {
				return fmt.Errorf("storage view consumer partition %q contains an incomplete Dataset route", id)
			}
			if dataset.DatasetID == "*" {
				continue
			}
			key := dataset.SpaceID + "\x00" + dataset.DatasetID
			if previous, exists := routes[key]; exists {
				return fmt.Errorf("Dataset %s/%s is assigned to partitions %q and %q", dataset.SpaceID, dataset.DatasetID, previous, id)
			}
			routes[key] = id
		}
	}
	if managed != nil {
		for _, dataset := range managed {
			key := strings.TrimSpace(dataset.SpaceID) + "\x00" + strings.TrimSpace(dataset.DatasetID)
			if _, exists := routes[key]; !exists {
				if _, wildcard := routes[strings.TrimSpace(dataset.SpaceID)+"\x00*"]; wildcard {
					continue
				}
				return fmt.Errorf("managed Dataset %s/%s is not assigned to a consumer partition", dataset.SpaceID, dataset.DatasetID)
			}
		}
		// Configuration is intentionally an allow-list for Views that may be
		// created later (for example a Factor result Dataset). Only the active
		// metadata inventory must be covered; rejecting configured-but-not-yet
		// created routes would make a fresh deployment unable to start before
		// those optional Views exist.
	}
	// The four durable consumers are an intentional topology contract, not
	// optional tuning knobs. A partial or overlapping config would silently put
	// Kline back behind system metrics, defeating the partitioning guarantee.
	if len(durables) != 4 {
		return fmt.Errorf("storage view consumer topology must define exactly four durables (kline, factor, metrics, misc); got %d", len(durables))
	}
	requiredRoutes := map[string]struct {
		durable string
		space   string
		dataset string
	}{
		"kline":   {durable: events.StorageViewKlineConsumer, space: "crypto_market", dataset: "binance_spot_kline_1m"},
		"factor":  {durable: events.StorageViewFactorConsumer, space: "crypto_market", dataset: "binance_spot_kline_1m_factor"},
		"metrics": {durable: events.StorageViewMetricsConsumer, space: "moox_system", dataset: "moox_service_metrics"},
	}
	for name, required := range requiredRoutes {
		partitionID, ok := routes[required.space+"\x00"+required.dataset]
		if !ok {
			return fmt.Errorf("storage view %s route %s/%s is missing", name, required.space, required.dataset)
		}
		partitionIndex := -1
		for i := range v.ConsumerPartitions {
			if v.ConsumerPartitions[i].ID == partitionID {
				partitionIndex = i
				break
			}
		}
		if partitionIndex < 0 || v.ConsumerPartitions[partitionIndex].Durable != required.durable {
			return fmt.Errorf("storage view %s route %s/%s must use durable %s", name, required.space, required.dataset, required.durable)
		}
	}
	return nil
}

func validConsumerName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func (v StorageView) HasRebuildMaxPendingSetting() bool { return v.rebuildMaxPendingSet }
func (v StorageView) HasRebuildIdleChecksSetting() bool { return v.rebuildIdleChecksSet }

// UnmarshalYAML remembers whether max_view_file_bytes was explicitly present.
// This lets defaults preserve omitted legacy configuration while allowing the
// server to reject an explicit zero/negative value instead of silently
// replacing it with the default watermark.
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
	_, v.maxViewFileBytesSet = raw["max_view_file_bytes"]
	_, v.rebuildMaxPendingSet = raw["rebuild_max_pending"]
	_, v.rebuildIdleChecksSet = raw["rebuild_idle_checks"]
	return nil
}

type StorageRPCConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
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
		c.EventBus.MaxAckPending = StorageDefaultMaxAckPending
		if c.HasRole("view") {
			c.EventBus.MaxAckPending = StorageViewMaxAckPending
		}
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
		c.View.Ordering = "dataset"
	}
	if strings.TrimSpace(c.View.MaintenanceCheckInterval) == "" {
		c.View.MaintenanceCheckInterval = "1m"
	}
	if c.View.MaxViewFileBytes <= 0 && !c.View.maxViewFileBytesSet {
		c.View.MaxViewFileBytes = 1 << 30
	}
	if strings.TrimSpace(c.View.RebuildLookback) == "" {
		c.View.RebuildLookback = "24h"
	}
	if len(c.View.RebuildLookbackPeriods) == 0 {
		c.View.RebuildLookbackPeriods = map[string]uint64{
			"1m":      1000,
			"1h":      1000,
			"1d":      1000,
			"default": 1000,
		}
	} else {
		normalized := make(map[string]uint64, len(c.View.RebuildLookbackPeriods)+1)
		for frequency, periods := range c.View.RebuildLookbackPeriods {
			frequency = strings.ToLower(strings.TrimSpace(frequency))
			if frequency != "" {
				normalized[frequency] = periods
			}
		}
		if normalized["default"] == 0 {
			normalized["default"] = 1000
		}
		c.View.RebuildLookbackPeriods = normalized
	}
	if c.View.RebuildMaxPending == 0 && !c.View.rebuildMaxPendingSet {
		c.View.RebuildMaxPending = 32
	}
	if c.View.RebuildIdleChecks == 0 && !c.View.rebuildIdleChecksSet {
		c.View.RebuildIdleChecks = 3
	}
	c.View.applyConsumerPartitionDefaults()
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
