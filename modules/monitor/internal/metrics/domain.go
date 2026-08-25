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
	IsStale     bool      `gorm:"column:c_is_stale"`
	CreatedAt   time.Time `gorm:"column:c_ctime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime"`
}

func (MetricService) TableName() string { return "t_monitor_metric_services" }

type MetricSeries struct {
	ID          uint64    `gorm:"column:c_id;primaryKey"`
	ServiceName string    `gorm:"column:c_service_name"`
	InstanceID  string    `gorm:"column:c_instance_id"`
	SeriesID    string    `gorm:"column:c_series_id"`
	MetricName  string    `gorm:"column:c_metric_name"`
	MetricType  string    `gorm:"column:c_metric_type"`
	LabelsJSON  string    `gorm:"column:c_labels_json"`
	LastSeenAt  time.Time `gorm:"column:c_last_seen_at"`
	IsStale     bool      `gorm:"column:c_is_stale"`
	CreatedAt   time.Time `gorm:"column:c_ctime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime"`
}

func (MetricSeries) TableName() string { return "t_monitor_metric_series" }

type MetricLatest struct {
	ID              uint64    `gorm:"column:c_id;primaryKey"`
	SeriesID        string    `gorm:"column:c_series_id"`
	ServiceName     string    `gorm:"column:c_service_name"`
	InstanceID      string    `gorm:"column:c_instance_id"`
	MetricName      string    `gorm:"column:c_metric_name"`
	MetricType      string    `gorm:"column:c_metric_type"`
	LabelsJSON      string    `gorm:"column:c_labels_json"`
	Value           float64   `gorm:"column:c_value"`
	ObservedAt      time.Time `gorm:"column:c_observed_at"`
	IntervalSeconds int       `gorm:"column:c_interval_seconds"`
	MessageID       string    `gorm:"column:c_message_id"`
	ProducerNodeID  string    `gorm:"column:c_producer_node_id"`
	ProducerVersion string    `gorm:"column:c_producer_version"`
	CreatedAt       time.Time `gorm:"column:c_ctime"`
	UpdatedAt       time.Time `gorm:"column:c_mtime"`
}

func (MetricLatest) TableName() string { return "t_monitor_metric_latest" }

type MetricIngestMessage struct {
	ID          uint64     `gorm:"column:c_id;primaryKey"`
	MessageID   string     `gorm:"column:c_message_id"`
	ServiceName string     `gorm:"column:c_service_name"`
	InstanceID  string     `gorm:"column:c_instance_id"`
	OccurredAt  *time.Time `gorm:"column:c_occurred_at"`
	ProcessedAt time.Time  `gorm:"column:c_processed_at"`
	ExpiresAt   time.Time  `gorm:"column:c_expires_at"`
}

func (MetricIngestMessage) TableName() string { return "t_monitor_metric_ingest_messages" }
