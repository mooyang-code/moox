package domain

import "time"

// PeriodSeriesSnapshot is the immutable ordered series set for one Dataset period.
type PeriodSeriesSnapshot struct {
	Key           PeriodKey
	SeriesHash    string
	ExpectedCount uint32
	Entries       []PeriodSeriesSnapshotEntry
}

// PeriodSeriesSnapshotEntry persists one series in a PeriodSeriesSnapshot.
type PeriodSeriesSnapshotEntry struct {
	ID             int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID        string    `gorm:"column:c_space_id"`
	DatasetID      string    `gorm:"column:c_dataset_id"`
	Frequency      string    `gorm:"column:c_frequency"`
	PeriodTime     time.Time `gorm:"column:c_period_time"`
	SeriesIndex    uint32    `gorm:"column:c_series_index"`
	SeriesKey      string    `gorm:"column:c_series_key"`
	SubjectID      string    `gorm:"column:c_subject_id"`
	Provider       string    `gorm:"column:c_provider"`
	SourceID       string    `gorm:"column:c_source_id"`
	MarketType     string    `gorm:"column:c_market_type"`
	ProviderSymbol string    `gorm:"column:c_provider_symbol"`
	SeriesTag      string    `gorm:"column:c_series_tag"`
	SeriesHash     string    `gorm:"column:c_series_hash"`
	ExpectedCount  int       `gorm:"column:c_expected_count"`
	CreateTime     time.Time `gorm:"column:c_ctime"`
}

func (*PeriodSeriesSnapshotEntry) TableName() string { return "t_collector_task_period_series" }
