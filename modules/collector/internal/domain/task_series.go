package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// TaskSeries is one normalized Dataset series in the current CollectionTask
// Tag union. SeriesIndex is dense and meaningful only within SeriesHash.
type TaskSeries struct {
	ID             int       `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID        string    `gorm:"column:c_space_id"`
	TaskID         string    `gorm:"column:c_task_id"`
	SeriesIndex    uint32    `gorm:"column:c_series_index"`
	SeriesKey      string    `gorm:"column:c_series_key"`
	SubjectID      string    `gorm:"column:c_subject_id"`
	Provider       string    `gorm:"column:c_provider"`
	SourceID       string    `gorm:"column:c_source_id"`
	MarketType     string    `gorm:"column:c_market_type"`
	ProviderSymbol string    `gorm:"column:c_provider_symbol"`
	SeriesTag      string    `gorm:"column:c_series_tag"`
	CreateTime     time.Time `gorm:"column:c_ctime"`
	ModifyTime     time.Time `gorm:"column:c_mtime"`
}

func (*TaskSeries) TableName() string { return "t_collector_task_series" }

func CanonicalSeriesKey(provider, sourceID, marketType, subjectID, seriesTag string) string {
	parts := []string{provider, sourceID, marketType, subjectID, seriesTag}
	for i := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(parts[i]))
	}
	return strings.Join(parts, "\x00")
}

func SeriesSetHash(keys []string) string {
	h := sha256.New()
	for _, key := range keys {
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
