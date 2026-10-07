// Package metrics owns the monitor metrics bounded context: catalog ingestion,
// rule evaluation, and its dedicated persistence stores.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MetricMessageStore struct {
	db              *gorm.DB
	DedupeRetention time.Duration
}

const (
	// K-line freshness reads are scoped to configured Views and need only cover
	// the active subject set. Keep a hard cap so a malformed configuration cannot
	// turn the timer into an unbounded database read.
	defaultKlineLatestLimit = 20000
	maxKlineLatestLimit     = 100000
)

var viewDatasetMetricNames = map[string]struct{}{
	ViewOutputLatestMetric:         {},
	ViewOutputTrackedSinceMetric:   {},
	ViewOutputSubjectsMetric:       {},
	ViewOutputLaggingMetric:        {},
	ViewOutputLaggingSubjectMetric: {},
}

func NewMetricMessageStore(db *gorm.DB) *MetricMessageStore {
	return &MetricMessageStore{db: db, DedupeRetention: 7 * 24 * time.Hour}
}
func (r *MetricMessageStore) IsDuplicate(ctx context.Context, messageID string) (bool, error) {
	if r == nil || r.db == nil || strings.TrimSpace(messageID) == "" {
		return false, errors.New("message store is not initialized or message_id is empty")
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&MetricIngestMessage{}).Where("c_message_id = ?", messageID).Count(&count).Error
	return count > 0, err
}

// CommitIngest atomically records dedupe/catalog/latest state. Storage history
// is deliberately written before this method and is independently idempotent.
func (r *MetricMessageStore) CommitIngest(ctx context.Context, msg *eventpb.EventMessage, report *metricspb.MetricReport, samples []Sample) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("metrics store is not initialized")
	}
	if msg == nil || msg.GetEventId() == "" || report == nil {
		return false, errors.New("message_id is required")
	}
	retention := r.DedupeRetention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	now := time.Now().UTC()
	expires := now.Add(retention)
	var duplicate bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := &MetricIngestMessage{MessageID: msg.GetEventId(), ServiceName: report.GetServiceName(), InstanceID: report.GetInstanceId(), ProcessedAt: now, ExpiresAt: expires}
		if at := msg.GetOccurredAt(); at != nil {
			t := at.AsTime()
			row.OccurredAt = &t
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			duplicate = true
			return nil
		}
		serviceName, instanceID, bootID, nodeID, version := report.GetServiceName(), report.GetInstanceId(), report.GetBootId(), report.GetNodeId(), report.GetServiceVersion()
		if serviceName == "" {
			return errors.New("producer.service_name is required")
		}
		service := &MetricService{ServiceName: serviceName, InstanceID: instanceID, BootID: bootID, NodeID: nodeID, Version: version, LastSeenAt: now}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_service_name"}, {Name: "c_instance_id"}, {Name: "c_boot_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_node_id", "c_version", "c_last_seen_at", "c_mtime"})}).Create(service).Error; err != nil {
			return err
		}
		return upsertSamples(tx, samples)
	})
	if err != nil {
		return false, fmt.Errorf("commit metrics ingest: %w", err)
	}
	return duplicate, nil
}

// ingestBatchRows bounds the rows of one multi-row upsert. A report carries
// thousands of per-subject samples and monitor's SQLite has one connection, so
// the ingest transaction must be a handful of statements, not three per sample.
const ingestBatchRows = 500

// upsertSamples records the series catalog and the latest value of every
// sample with batched upserts. The latest value only advances to a newer
// observation, and a monotonic metric never moves back to a smaller value.
func upsertSamples(tx *gorm.DB, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	now := time.Now().UTC()
	series := make([]MetricSeries, 0, len(samples))
	var plain, monotonic []MetricLatest
	for _, sample := range samples {
		observedAt := sample.ObservedAt.UTC()
		series = append(series, MetricSeries{
			ServiceName: sample.ServiceName, InstanceID: sample.InstanceID, SeriesID: sample.SeriesID,
			MetricName: sample.MetricName, MetricType: sample.MetricType, LabelsJSON: sample.LabelsJSON,
			LastSeenAt: observedAt, CreatedAt: now, UpdatedAt: now,
		})
		latest := MetricLatest{
			SeriesID: sample.SeriesID, ServiceName: sample.ServiceName, InstanceID: sample.InstanceID,
			MetricName: sample.MetricName, MetricType: sample.MetricType, LabelsJSON: sample.LabelsJSON,
			Value: sample.Value, ObservedAt: observedAt, IntervalSeconds: int(sample.Interval / time.Second),
			MessageID: sample.MessageID, ProducerNodeID: sample.ProducerNodeID, ProducerVersion: sample.ProducerVersion,
			CreatedAt: now, UpdatedAt: now,
		}
		if monotonicMetric(sample.MetricName) {
			monotonic = append(monotonic, latest)
		} else {
			plain = append(plain, latest)
		}
	}
	seriesUpsert := clause.OnConflict{
		Columns: []clause.Column{{Name: "c_service_name"}, {Name: "c_instance_id"}, {Name: "c_series_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"c_metric_name":  gorm.Expr("excluded.c_metric_name"),
			"c_metric_type":  gorm.Expr("excluded.c_metric_type"),
			"c_labels_json":  gorm.Expr("excluded.c_labels_json"),
			"c_last_seen_at": gorm.Expr("MAX(c_last_seen_at, excluded.c_last_seen_at)"),
			"c_mtime":        gorm.Expr("CURRENT_TIMESTAMP"),
		}),
	}
	if err := tx.Clauses(seriesUpsert).CreateInBatches(&series, ingestBatchRows).Error; err != nil {
		return err
	}
	newer := clause.Expr{SQL: "excluded.c_observed_at > t_monitor_metric_latest.c_observed_at"}
	notSmaller := clause.Expr{SQL: "excluded.c_value >= t_monitor_metric_latest.c_value"}
	for _, group := range []struct {
		rows  []MetricLatest
		where []clause.Expression
	}{
		{rows: plain, where: []clause.Expression{newer}},
		// Reporter 重启后内存水位可能为空。此时保留已提交的最新值，
		// 避免较新的抓取结果携带旧业务水位并造成数值倒退。
		{rows: monotonic, where: []clause.Expression{newer, notSmaller}},
	} {
		if len(group.rows) == 0 {
			continue
		}
		latestUpsert := clause.OnConflict{
			Columns: []clause.Column{{Name: "c_series_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"c_service_name", "c_instance_id", "c_metric_name", "c_metric_type", "c_labels_json", "c_value",
				"c_observed_at", "c_interval_seconds", "c_message_id", "c_producer_node_id", "c_producer_version", "c_mtime",
			}),
			Where: clause.Where{Exprs: group.where},
		}
		if err := tx.Clauses(latestUpsert).CreateInBatches(&group.rows, ingestBatchRows).Error; err != nil {
			return err
		}
	}
	return nil
}

func monotonicMetric(name string) bool {
	return strings.HasSuffix(name, "_dataset_input_watermark_timestamp_seconds") ||
		strings.HasSuffix(name, "_dataset_output_watermark_timestamp_seconds") ||
		name == ViewOutputLatestMetric ||
		strings.HasSuffix(name, "_view_output_watermark_timestamp_seconds") ||
		strings.HasSuffix(name, "_business_watermark_timestamp_seconds") ||
		strings.HasSuffix(name, "_input_watermark_timestamp_seconds") ||
		strings.HasSuffix(name, "_last_success_timestamp_seconds") ||
		strings.HasSuffix(name, "_last_error_timestamp_seconds") ||
		strings.HasSuffix(name, "_metrics_errors_total")
}

func (r *MetricMessageStore) PruneDedupe(ctx context.Context, now time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("message store is not initialized")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return store.DeleteBefore(ctx, r.db, "t_monitor_metric_ingest_messages", "c_expires_at", now)
}

// RetiredSeriesAfter is how long a series may go unreported before it is
// dropped with its latest value: the View, subject or reporter behind it is
// gone. A series that is reported again is simply recreated.
const RetiredSeriesAfter = 24 * time.Hour

// PruneRetiredSeries removes the series not reported since RetiredSeriesAfter
// before now, together with their latest values.
func (r *MetricMessageStore) PruneRetiredSeries(ctx context.Context, now time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("message store is not initialized")
	}
	cutoff := now.UTC().Add(-RetiredSeriesAfter)
	var pruned int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM t_monitor_metric_latest WHERE c_series_id IN
			(SELECT c_series_id FROM t_monitor_metric_series WHERE c_last_seen_at < ?)`, cutoff).Error; err != nil {
			return err
		}
		result := tx.Where("c_last_seen_at < ?", cutoff).Delete(&MetricSeries{})
		pruned = result.RowsAffected
		return result.Error
	})
	return pruned, err
}

func (r *MetricMessageStore) GetLatest(ctx context.Context, seriesID string) (*MetricLatest, error) {
	if r == nil || r.db == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	var row MetricLatest
	err := r.db.WithContext(ctx).Where("c_series_id = ?", seriesID).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListLatestByMetricNames is intentionally limited to the generic View output
// watermark family. Scoped freshness evaluation should prefer
// ListLatestByViewScopes so unrelated View series are not loaded.
func (r *MetricMessageStore) ListLatestByMetricNames(ctx context.Context, names []string, limit int) ([]MetricLatest, error) {
	if r == nil || r.db == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	if limit <= 0 {
		limit = defaultKlineLatestLimit
	}
	if limit > maxKlineLatestLimit {
		return nil, fmt.Errorf("view dataset latest limit %d exceeds maximum %d", limit, maxKlineLatestLimit)
	}
	if len(names) == 0 {
		return nil, errors.New("view dataset metric names must not be empty")
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := viewDatasetMetricNames[name]; !ok {
			return nil, fmt.Errorf("metric name %q is not a supported View dataset metric", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("duplicate View dataset metric name %q", name)
		}
		seen[name] = struct{}{}
	}
	var rows []MetricLatest
	err := r.db.WithContext(ctx).
		Where("c_metric_name IN ?", names).
		Order("c_metric_name ASC, c_labels_json ASC, c_series_id ASC").
		Limit(limit + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, fmt.Errorf("view dataset latest result exceeds limit %d", limit)
	}
	return rows, nil
}

// ListLatestByViewScopes reads only the output watermark rows belonging to
// configured freshness Views. Keeping the scope in SQL avoids loading service
// metrics and unrelated Views into the K-line evaluator before filtering them.
func (r *MetricMessageStore) ListLatestByViewScopes(ctx context.Context, metricName string, scopes []ViewMetricScope, limit int) ([]MetricLatest, error) {
	if r == nil || r.db == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	if _, ok := viewDatasetMetricNames[metricName]; !ok {
		return nil, fmt.Errorf("metric name %q is not a supported View dataset metric", metricName)
	}
	if len(scopes) == 0 {
		return []MetricLatest{}, nil
	}
	if limit <= 0 {
		limit = defaultKlineLatestLimit
	}
	if limit > maxKlineLatestLimit {
		return nil, fmt.Errorf("view dataset latest limit %d exceeds maximum %d", limit, maxKlineLatestLimit)
	}
	conditions := make([]string, 0, len(scopes))
	args := make([]interface{}, 0, len(scopes)*4+1)
	for _, scope := range scopes {
		spaceID, viewID := strings.TrimSpace(scope.SpaceID), strings.TrimSpace(scope.ViewID)
		datasetID, frequency := strings.TrimSpace(scope.DatasetID), strings.TrimSpace(scope.Frequency)
		if spaceID == "" || viewID == "" || frequency == "" {
			continue
		}
		condition := "(json_extract(c_labels_json, '$.space_id') = ? AND json_extract(c_labels_json, '$.view_id') = ? AND json_extract(c_labels_json, '$.freq') = ?"
		args = append(args, spaceID, viewID, frequency)
		if datasetID != "" {
			condition += " AND json_extract(c_labels_json, '$.dataset_id') = ?"
			args = append(args, datasetID)
		}
		conditions = append(conditions, condition+")")
	}
	if len(conditions) == 0 {
		return []MetricLatest{}, nil
	}
	args = append([]interface{}{metricName}, args...)
	var rows []MetricLatest
	err := r.db.WithContext(ctx).
		Where("c_metric_name = ? AND ("+strings.Join(conditions, " OR ")+")", args...).
		Order("c_labels_json ASC, c_series_id ASC").
		Limit(limit + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, fmt.Errorf("view dataset latest result exceeds limit %d", limit)
	}
	return rows, nil
}

func (r *MetricMessageStore) ListSeries(ctx context.Context, serviceName, metricName string, limit int) ([]MetricSeries, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 500 {
		limit = 500
	}
	var rows []MetricSeries
	q := r.db.WithContext(ctx)
	if serviceName != "" {
		q = q.Where("c_service_name = ?", serviceName)
	}
	if metricName != "" {
		q = q.Where("c_metric_name = ?", metricName)
	}
	err := q.Order("c_metric_name ASC,c_series_id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}
