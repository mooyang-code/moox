// Package metrics owns the monitor metrics bounded context: catalog ingestion,
// rule evaluation, and its dedicated persistence stores.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MetricMessageStore struct {
	db              *gorm.DB
	DedupeRetention time.Duration
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
		service := &MetricService{ServiceName: serviceName, InstanceID: instanceID, BootID: bootID, NodeID: nodeID, Version: version, LastSeenAt: now, IsStale: false}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_service_name"}, {Name: "c_instance_id"}, {Name: "c_boot_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_node_id", "c_version", "c_last_seen_at", "c_is_stale", "c_mtime"})}).Create(service).Error; err != nil {
			return err
		}
		for _, sample := range samples {
			series := &MetricSeries{ServiceName: sample.ServiceName, InstanceID: sample.InstanceID, SeriesID: sample.SeriesID, MetricName: sample.MetricName, MetricType: sample.MetricType, LabelsJSON: sample.LabelsJSON, LastSeenAt: sample.ObservedAt, IsStale: false}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_service_name"}, {Name: "c_instance_id"}, {Name: "c_series_id"}}, DoUpdates: clause.Assignments(map[string]interface{}{
				"c_metric_name":  gorm.Expr("excluded.c_metric_name"),
				"c_metric_type":  gorm.Expr("excluded.c_metric_type"),
				"c_labels_json":  gorm.Expr("excluded.c_labels_json"),
				"c_last_seen_at": gorm.Expr("MAX(c_last_seen_at, excluded.c_last_seen_at)"),
				"c_is_stale":     gorm.Expr("CASE WHEN excluded.c_last_seen_at > c_last_seen_at THEN excluded.c_is_stale ELSE c_is_stale END"),
				"c_mtime":        gorm.Expr("CURRENT_TIMESTAMP"),
			})}).Create(series).Error; err != nil {
				return err
			}
			var latest MetricLatest
			find := tx.Where("c_series_id = ?", sample.SeriesID).First(&latest)
			if errors.Is(find.Error, gorm.ErrRecordNotFound) {
				latest = MetricLatest{SeriesID: sample.SeriesID}
				find = nil
			}
			if find != nil {
				return find.Error
			}
			if latest.ID != 0 && !sample.ObservedAt.After(latest.ObservedAt) {
				continue
			}
			if latest.ID != 0 && monotonicMetric(sample.MetricName) && sample.Value < latest.Value {
				// Reporter processes may restart with their in-memory watermark
				// maps empty. Keep the durable latest value instead of allowing a
				// stale business watermark to move backwards under a newer scrape.
				continue
			}
			latest.ServiceName, latest.InstanceID, latest.MetricName, latest.MetricType, latest.LabelsJSON = sample.ServiceName, sample.InstanceID, sample.MetricName, sample.MetricType, sample.LabelsJSON
			latest.Value, latest.ObservedAt, latest.IntervalSeconds, latest.MessageID, latest.ProducerNodeID, latest.ProducerVersion = sample.Value, sample.ObservedAt, int(sample.Interval/time.Second), sample.MessageID, sample.ProducerNodeID, sample.ProducerVersion
			if latest.ID == 0 {
				if err := tx.Create(&latest).Error; err != nil {
					return err
				}
			} else if err := tx.Model(&MetricLatest{}).Where("c_series_id = ? AND c_observed_at < ?", latest.SeriesID, sample.ObservedAt).Updates(map[string]any{"c_value": latest.Value, "c_service_name": latest.ServiceName, "c_instance_id": latest.InstanceID, "c_metric_name": latest.MetricName, "c_metric_type": latest.MetricType, "c_labels_json": latest.LabelsJSON, "c_observed_at": latest.ObservedAt, "c_interval_seconds": latest.IntervalSeconds, "c_message_id": latest.MessageID, "c_producer_node_id": latest.ProducerNodeID, "c_producer_version": latest.ProducerVersion}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("commit metrics ingest: %w", err)
	}
	return duplicate, nil
}

func monotonicMetric(name string) bool {
	return name == "moox_business_watermark_timestamp_seconds" ||
		name == "moox_module_watermark_timestamp_seconds" ||
		name == "moox_module_input_watermark_timestamp_seconds" ||
		name == "moox_module_metrics_errors_total" ||
		name == "moox_module_metrics_last_error_timestamp_seconds" ||
		name == "moox_module_last_error_timestamp_seconds"
}

func (r *MetricMessageStore) PruneDedupe(ctx context.Context, now time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("message store is not initialized")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	q := r.db.WithContext(ctx).Where("c_expires_at < ?", now).Delete(&MetricIngestMessage{})
	return q.RowsAffected, q.Error
}

func (r *MetricMessageStore) GetLatest(ctx context.Context, seriesID string) (*MetricLatest, error) {
	var row MetricLatest
	err := r.db.WithContext(ctx).Where("c_series_id = ?", seriesID).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}
func (r *MetricMessageStore) ListSeries(ctx context.Context, serviceName, metricName string, limit int) ([]MetricSeries, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 500 {
		limit = 500
	}
	var rows []MetricSeries
	q := r.db.WithContext(ctx).Where("c_is_stale = 0")
	if serviceName != "" {
		q = q.Where("c_service_name = ?", serviceName)
	}
	if metricName != "" {
		q = q.Where("c_metric_name = ?", metricName)
	}
	err := q.Order("c_metric_name ASC,c_series_id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}
