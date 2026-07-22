package metrics

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MetricCatalog provides bounded reads over the SQLite catalog. It never
// queries Storage without first resolving a finite set of series IDs.
type MetricCatalog struct {
	messageStore *MetricMessageStore
	noDataAfter  time.Duration
}

func NewCatalog(messageStore *MetricMessageStore) *MetricCatalog {
	return &MetricCatalog{messageStore: messageStore, noDataAfter: 2 * time.Minute}
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
	if c == nil || c.messageStore == nil || c.messageStore.db == nil {
		return nil, 0, ErrMetricsStoreUnavailable
	}
	offset, limit = boundedPage(offset, limit)
	var rows []MetricService
	q := c.messageStore.db.WithContext(ctx).Model(&MetricService{})
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

// ListServicesFor resolves only the finite Doctor selection. It must not page
// through unrelated catalog rows and silently turn truncation into "missing".
func (c *MetricCatalog) ListServicesFor(ctx context.Context, serviceNames []string, nodeID string, limit int) ([]MetricService, error) {
	return c.ListServicesForAt(ctx, serviceNames, nodeID, limit, time.Now().UTC())
}

func (c *MetricCatalog) ListServicesForAt(ctx context.Context, serviceNames []string, nodeID string, limit int, now time.Time) ([]MetricService, error) {
	if c == nil || c.messageStore == nil || c.messageStore.db == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	if len(serviceNames) == 0 {
		return []MetricService{}, nil
	}
	if limit <= 0 {
		return nil, fmt.Errorf("service limit must be positive")
	}
	q := c.messageStore.db.WithContext(ctx).Model(&MetricService{}).Where("c_service_name IN ?", serviceNames)
	if nodeID != "" {
		q = q.Where("c_node_id = ?", nodeID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, err
	}
	if total > int64(limit) {
		return nil, fmt.Errorf("selected metric services exceed limit %d", limit)
	}
	var rows []MetricService
	if err := q.Order("c_service_name ASC, c_instance_id ASC, c_boot_id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	c.markServicesStaleAt(rows, now)
	return rows, nil
}

func (c *MetricCatalog) markServicesStale(rows []MetricService) {
	c.markServicesStaleAt(rows, time.Now().UTC())
}

func (c *MetricCatalog) markServicesStaleAt(rows []MetricService, now time.Time) {
	cutoff := now.UTC().Add(-c.noDataAfter)
	for i := range rows {
		if !rows[i].LastSeenAt.IsZero() && rows[i].LastSeenAt.Before(cutoff) {
			rows[i].IsStale = true
		}
	}
}

func (c *MetricCatalog) ListNames(ctx context.Context, serviceName string, offset, limit int) ([]MetricName, int64, error) {
	if c == nil || c.messageStore == nil || c.messageStore.db == nil {
		return nil, 0, ErrMetricsStoreUnavailable
	}
	offset, limit = boundedPage(offset, limit)
	type nameRow struct {
		ServiceName string            `gorm:"column:service_name"`
		MetricName  string            `gorm:"column:metric_name"`
		MetricType  string            `gorm:"column:metric_type"`
		SeriesCount int               `gorm:"column:series_count"`
		LastSeen    metricCatalogTime `gorm:"column:last_seen"`
	}
	var rows []MetricName
	q := c.messageStore.db.WithContext(ctx).Table("t_monitor_metric_series").Select("c_service_name AS service_name, c_metric_name AS metric_name, MAX(c_metric_type) AS metric_type, COUNT(*) AS series_count, MAX(c_last_seen_at) AS last_seen").Group("c_service_name, c_metric_name")
	if serviceName != "" {
		q = q.Where("c_service_name = ?", serviceName)
	}
	var total int64
	countQ := c.messageStore.db.WithContext(ctx).Table("t_monitor_metric_series").Select("COUNT(DISTINCT c_service_name || ':' || c_metric_name)")
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
		rows = append(rows, MetricName{ServiceName: row.ServiceName, MetricName: row.MetricName, MetricType: row.MetricType, SeriesCount: row.SeriesCount, LastSeenAt: row.LastSeen.Time})
	}
	return rows, total, nil
}

type metricCatalogTime struct {
	time.Time
}

func (t metricCatalogTime) Value() (driver.Value, error) {
	return t.Time, nil
}

func (t *metricCatalogTime) Scan(value any) error {
	switch value := value.(type) {
	case time.Time:
		t.Time = value
		return nil
	case string:
		return t.parse(value)
	case []byte:
		return t.parse(string(value))
	case nil:
		t.Time = time.Time{}
		return nil
	default:
		return fmt.Errorf("unsupported metric catalog time type %T", value)
	}
}

func (t *metricCatalogTime) parse(value string) error {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999 -0700 MST",
	} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			t.Time = parsed
			return nil
		}
	}
	valueWithoutUTC := strings.TrimSuffix(value, "Z")
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999",
	} {
		parsed, err := time.ParseInLocation(layout, valueWithoutUTC, time.UTC)
		if err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("parse metric catalog time %q", value)
}

type MetricName struct {
	ServiceName, MetricName, MetricType string
	SeriesCount                         int
	LastSeenAt                          time.Time
}

func (c *MetricCatalog) ListSeries(ctx context.Context, serviceName, metricName, labelsJSON string, offset, limit int) ([]MetricSeries, int64, error) {
	if c == nil || c.messageStore == nil || c.messageStore.db == nil {
		return nil, 0, ErrMetricsStoreUnavailable
	}
	offset, limit = boundedPage(offset, limit)
	q := c.messageStore.db.WithContext(ctx).Model(&MetricSeries{})
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
	c.markSeriesStaleAt(rows, time.Now().UTC())
}

func (c *MetricCatalog) markSeriesStaleAt(rows []MetricSeries, now time.Time) {
	cutoff := now.UTC().Add(-c.noDataAfter)
	for i := range rows {
		if !rows[i].LastSeenAt.IsZero() && rows[i].LastSeenAt.Before(cutoff) {
			rows[i].IsStale = true
		}
	}
}

func (c *MetricCatalog) FindSeries(ctx context.Context, seriesID, serviceName, metricName, labelsJSON string, limit int) ([]MetricSeries, error) {
	return c.FindSeriesAt(ctx, seriesID, serviceName, metricName, labelsJSON, limit, time.Now().UTC())
}

func (c *MetricCatalog) FindSeriesAt(ctx context.Context, seriesID, serviceName, metricName, labelsJSON string, limit int, now time.Time) ([]MetricSeries, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	q := c.messageStore.db.WithContext(ctx).Model(&MetricSeries{})
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
	c.markSeriesStaleAt(rows, now)
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
