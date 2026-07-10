package metrics

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// MetricCatalog provides bounded reads over the SQLite catalog. It never
// queries Storage without first resolving a finite set of series IDs.
type MetricCatalog struct {
	repo        *Repository
	noDataAfter time.Duration
}

func NewCatalog(repo *Repository) *MetricCatalog {
	return &MetricCatalog{repo: repo, noDataAfter: 2 * time.Minute}
}
func (c *MetricCatalog) SetNoDataAfter(d time.Duration) {
	if c != nil && d > 0 {
		c.noDataAfter = d
	}
}
func (c *MetricCatalog) NoDataAfter() time.Duration {
	if c == nil || c.noDataAfter <= 0 {
		return 2 * time.Minute
	}
	return c.noDataAfter
}

func (c *MetricCatalog) ListServices(ctx context.Context, spaceID string, offset, limit int) ([]MetricService, int64, error) {
	if c == nil || c.repo == nil || c.repo.db == nil {
		return nil, 0, ErrMetricsRepositoryUnavailable
	}
	offset, limit = boundedPage(offset, limit)
	var rows []MetricService
	q := c.repo.db.WithContext(ctx).Model(&MetricService{})
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_service_name <> ''")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("c_service_name ASC, c_instance_id ASC, c_boot_id ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	c.markServicesStale(rows)
	return rows, total, nil
}

func (c *MetricCatalog) markServicesStale(rows []MetricService) {
	cutoff := time.Now().UTC().Add(-c.noDataAfter)
	for i := range rows {
		if !rows[i].LastSeenAt.IsZero() && rows[i].LastSeenAt.Before(cutoff) {
			rows[i].IsStale = true
		}
	}
}

func (c *MetricCatalog) ListNames(ctx context.Context, serviceName string, offset, limit int) ([]MetricName, int64, error) {
	if c == nil || c.repo == nil || c.repo.db == nil {
		return nil, 0, ErrMetricsRepositoryUnavailable
	}
	offset, limit = boundedPage(offset, limit)
	type nameRow struct {
		ServiceName string    `gorm:"column:service_name"`
		MetricName  string    `gorm:"column:metric_name"`
		MetricType  string    `gorm:"column:metric_type"`
		SeriesCount int       `gorm:"column:series_count"`
		LastSeen    time.Time `gorm:"column:last_seen"`
	}
	var rows []MetricName
	q := c.repo.db.WithContext(ctx).Table("t_monitor_metric_series").Select("c_service_name AS service_name, c_metric_name AS metric_name, MAX(c_metric_type) AS metric_type, COUNT(*) AS series_count, MAX(c_last_seen_at) AS last_seen").Group("c_service_name, c_metric_name")
	if serviceName != "" {
		q = q.Where("c_service_name = ?", serviceName)
	}
	var total int64
	countQ := c.repo.db.WithContext(ctx).Table("t_monitor_metric_series").Select("COUNT(DISTINCT c_service_name || ':' || c_metric_name)")
	if serviceName != "" {
		countQ = countQ.Where("c_service_name = ?", serviceName)
	}
	if err := countQ.Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	var grouped []nameRow
	if err := q.Order("c_service_name ASC, c_metric_name ASC").Offset(offset).Limit(limit).Scan(&grouped).Error; err != nil {
		return nil, 0, err
	}
	for _, row := range grouped {
		rows = append(rows, MetricName{ServiceName: row.ServiceName, MetricName: row.MetricName, MetricType: row.MetricType, SeriesCount: row.SeriesCount, LastSeenAt: row.LastSeen})
	}
	return rows, total, nil
}

type MetricName struct {
	ServiceName, MetricName, MetricType string
	SeriesCount                         int
	LastSeenAt                          time.Time
}

func (c *MetricCatalog) ListSeries(ctx context.Context, serviceName, metricName, labelsJSON string, offset, limit int) ([]MetricSeries, int64, error) {
	if c == nil || c.repo == nil || c.repo.db == nil {
		return nil, 0, ErrMetricsRepositoryUnavailable
	}
	offset, limit = boundedPage(offset, limit)
	q := c.repo.db.WithContext(ctx).Model(&MetricSeries{})
	if serviceName != "" {
		q = q.Where("c_service_name = ?", serviceName)
	}
	if metricName != "" {
		q = q.Where("c_metric_name = ?", metricName)
	}
	if labelsJSON != "" {
		q = q.Where("c_labels_json = ?", canonicalJSON(labelsJSON))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []MetricSeries
	if err := q.Order("c_metric_name ASC, c_series_id ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	c.markSeriesStale(rows)
	return rows, total, nil
}

func (c *MetricCatalog) markSeriesStale(rows []MetricSeries) {
	cutoff := time.Now().UTC().Add(-c.noDataAfter)
	for i := range rows {
		if !rows[i].LastSeenAt.IsZero() && rows[i].LastSeenAt.Before(cutoff) {
			rows[i].IsStale = true
		}
	}
}

func (c *MetricCatalog) FindSeries(ctx context.Context, seriesID, serviceName, metricName, labelsJSON string, limit int) ([]MetricSeries, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	q := c.repo.db.WithContext(ctx).Model(&MetricSeries{})
	if seriesID != "" {
		q = q.Where("c_series_id = ?", seriesID)
	}
	if serviceName != "" {
		q = q.Where("c_service_name = ?", serviceName)
	}
	if metricName != "" {
		q = q.Where("c_metric_name = ?", metricName)
	}
	if labelsJSON != "" {
		q = q.Where("c_labels_json = ?", canonicalJSON(labelsJSON))
	}
	var rows []MetricSeries
	if err := q.Order("c_series_id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	c.markSeriesStale(rows)
	return rows, nil
}

func boundedPage(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return offset, limit
}

func canonicalJSON(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(b)
}

func sortedSeries(rows []MetricSeries) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].SeriesID < rows[j].SeriesID })
}
