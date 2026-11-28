package metrics

import "time"

type MetricService struct {
	ID          uint64    `gorm:"column:c_id;primaryKey"`
	ServiceName string    `gorm:"column:c_service_name"`
	InstanceID  string    `gorm:"column:c_instance_id"`
	BootID      string    `gorm:"column:c_boot_id"`
	NodeID      string    `gorm:"column:c_node_id"`
	Version     string    `gorm:"column:c_version"`
	LastSeenAt  time.Time `gorm:"column:c_last_seen_at"`
	// Readiness 是最近一次上报的就绪判断：ready、not_ready，空表示上报方没有就绪判断（云函数）。
	Readiness      string `gorm:"column:c_readiness"`
	NotReadyReason string `gorm:"column:c_not_ready_reason"`
	// LifecycleJSON 是最近一次上报的模块级生命周期（metricspb.ModuleLifecycle 的 protojson），没有时为空。
	LifecycleJSON string `gorm:"column:c_lifecycle_json"`
	// IsStale is derived on read from LastSeenAt; it is not stored.
	IsStale bool `gorm:"-"`
	// CreatedAt 是这次启动第一次上报的时间（写入时取上报时间），反复重启按它数。
	CreatedAt time.Time `gorm:"column:c_ctime"`
	UpdatedAt time.Time `gorm:"column:c_mtime"`
}

func (MetricService) TableName() string { return "t_monitor_metric_services" }

// 就绪取值。
const (
	ReadinessReady    = "ready"
	ReadinessNotReady = "not_ready"
)

// MetricSeries 是一条指标序列：目录（名称、类型、标签、最近一次上报时间）与最新值在同一行（t_monitor_metric_series）。
type MetricSeries struct {
	ID          uint64 `gorm:"column:c_id;primaryKey"`
	SeriesID    string `gorm:"column:c_series_id"`
	ServiceName string `gorm:"column:c_service_name"`
	InstanceID  string `gorm:"column:c_instance_id"`
	MetricName  string `gorm:"column:c_metric_name"`
	MetricType  string `gorm:"column:c_metric_type"`
	LabelsJSON  string `gorm:"column:c_labels_json"`
	// LastSeenAt 是最近一次上报这条序列的时间；最新值只前进到更新的观测，ObservedAt 可能早于它。
	LastSeenAt time.Time `gorm:"column:c_last_seen_at"`
	// IsStale is derived on read from LastSeenAt; it is not stored.
	IsStale         bool      `gorm:"-"`
	Value           float64   `gorm:"column:c_value"`
	ObservedAt      time.Time `gorm:"column:c_observed_at"`
	IntervalSeconds int       `gorm:"column:c_interval_seconds"`
	MessageID       string    `gorm:"column:c_message_id"`
	CreatedAt       time.Time `gorm:"column:c_ctime"`
	UpdatedAt       time.Time `gorm:"column:c_mtime"`
}

func (MetricSeries) TableName() string { return "t_monitor_metric_series" }

type MetricIngestMessage struct {
	ID        uint64    `gorm:"column:c_id;primaryKey"`
	MessageID string    `gorm:"column:c_message_id"`
	ExpiresAt time.Time `gorm:"column:c_expires_at"`
}

func (MetricIngestMessage) TableName() string { return "t_monitor_metric_ingest_messages" }
