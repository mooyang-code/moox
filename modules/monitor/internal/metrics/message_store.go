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
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MetricMessageStore struct {
	db              *gorm.DB
	DedupeRetention time.Duration
	limits          SeriesLimits
	diag            *ingestDiagnostics
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
	return &MetricMessageStore{db: db, DedupeRetention: 7 * 24 * time.Hour, diag: newIngestDiagnostics()}
}

// CommitIngest atomically records dedupe, service heartbeat/catalog, and latest metric state in one transaction.
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
	var dropped []droppedSeries
	var hourNew, dayNew int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := &MetricIngestMessage{MessageID: msg.GetEventId(), ServiceName: report.GetServiceName(), InstanceID: report.GetInstanceId(), ProcessedAt: now, ExpiresAt: expires}
		if at := msg.GetOccurredAt(); at > 0 {
			t := time.UnixMilli(at).UTC()
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
		if err := upsertMetricService(tx, report, reportSeenAt(msg, now)); err != nil {
			return err
		}
		// 新序列限额：已有序列照常更新，超出限额的新序列丢弃，快照里的其余样本照常入库。
		admitted, droppedInTx, hour, day, err := r.admitSamples(tx, samples, now)
		if err != nil {
			return fmt.Errorf("apply series limits: %w", err)
		}
		samples, dropped, hourNew, dayNew = admitted, droppedInTx, hour, day
		// 必须在更新最新值之前：要和上一份已提交的水位比较。
		if err := recordBarArrivals(tx, samples); err != nil {
			return fmt.Errorf("record bar arrivals: %w", err)
		}
		return upsertSamples(tx, samples)
	})
	if err != nil {
		return false, fmt.Errorf("commit metrics ingest: %w", err)
	}
	if !duplicate {
		r.recordSeriesLimit(report.GetServiceName(), dropped, hourNew, dayNew, now)
	}
	return duplicate, nil
}

// recordSeriesLimit 在入库事务提交之后记录限额的使用与丢弃情况（回滚的事务不计入）。
func (r *MetricMessageStore) recordSeriesLimit(service string, dropped []droppedSeries, hour, day int64, now time.Time) {
	if !r.limits.enabled() {
		return
	}
	seriesLimitCurrent.WithLabelValues("hour").Set(float64(hour))
	seriesLimitCurrent.WithLabelValues("day").Set(float64(day))
	for _, item := range dropped {
		seriesDropped.WithLabelValues(item.service).Add(float64(item.count))
		r.diag.recordDropped(item.service, item.metric, item.count, now)
	}
}

func reportSeenAt(msg *eventpb.EventMessage, receivedAt time.Time) time.Time {
	if at := msg.GetOccurredAt(); at > 0 && time.UnixMilli(at).Before(receivedAt) {
		return time.UnixMilli(at).UTC()
	}
	return receivedAt
}

func upsertMetricService(tx *gorm.DB, report *metricspb.MetricReport, seenAt time.Time) error {
	if report.GetServiceName() == "" {
		return errors.New("producer.service_name is required")
	}
	readiness, reason := readinessOf(report.Ready, report.GetNotReadyReason())
	rows := []*MetricService{{
		ServiceName: report.GetServiceName(), InstanceID: report.GetInstanceId(), BootID: report.GetBootId(),
		NodeID: report.GetNodeId(), Version: report.GetServiceVersion(), LastSeenAt: seenAt,
		Readiness: readiness, NotReadyReason: reason, LifecycleJSON: lifecycleJSON(report.GetLifecycle()),
		CreatedAt: seenAt, UpdatedAt: seenAt,
	}}
	// 管理端代报的外部程序（因子引擎、归档 worker）各记一行：时间取管理端最近一次接受它心跳的时间，
	// 主机留空，由概览按组件名对到它唯一的部署上。从没心跳过的不记，按“从未上报”处理。
	for _, delegated := range report.GetDelegated() {
		if delegated.GetServiceName() == "" || delegated.GetLastHeartbeatAt() <= 0 {
			continue
		}
		heartbeat := time.UnixMilli(delegated.GetLastHeartbeatAt()).UTC()
		if heartbeat.After(seenAt) {
			heartbeat = seenAt
		}
		readiness, reason := readinessOf(delegated.Ready, delegated.GetNotReadyReason())
		rows = append(rows, &MetricService{
			ServiceName: delegated.GetServiceName(), InstanceID: delegated.GetInstanceId(), BootID: delegated.GetBootId(),
			Version: delegated.GetServiceVersion(), LastSeenAt: heartbeat,
			Readiness: readiness, NotReadyReason: reason, LifecycleJSON: lifecycleJSON(delegated.GetLifecycle()),
			CreatedAt: heartbeat, UpdatedAt: heartbeat,
		})
	}
	// 就绪与生命周期只跟着更新的上报走：重投递的旧上报不覆盖它们，也不让 last_seen 后退。
	newer := func(column string) clause.Expr {
		return gorm.Expr("CASE WHEN excluded.c_last_seen_at >= c_last_seen_at THEN excluded." + column + " ELSE " + column + " END")
	}
	for _, row := range rows {
		err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "c_service_name"}, {Name: "c_instance_id"}, {Name: "c_boot_id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"c_node_id":          gorm.Expr("excluded.c_node_id"),
				"c_version":          gorm.Expr("excluded.c_version"),
				"c_readiness":        newer("c_readiness"),
				"c_not_ready_reason": newer("c_not_ready_reason"),
				"c_lifecycle_json":   newer("c_lifecycle_json"),
				"c_last_seen_at":     gorm.Expr("MAX(c_last_seen_at, excluded.c_last_seen_at)"),
				"c_mtime":            gorm.Expr("excluded.c_mtime"),
			}),
		}).Create(row).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// readinessOf 把上报里的就绪字段转成库里的取值；没有就绪字段时为空。
func readinessOf(ready *bool, reason string) (string, string) {
	switch {
	case ready == nil:
		return "", ""
	case *ready:
		return ReadinessReady, ""
	}
	return ReadinessNotReady, strings.TrimSpace(reason)
}

func lifecycleJSON(lifecycle *metricspb.ModuleLifecycle) string {
	if lifecycle == nil || lifecycle.GetStage() == "" {
		return ""
	}
	raw, err := protojson.Marshal(lifecycle)
	if err != nil {
		return ""
	}
	return string(raw)
}

// DecodeLifecycle 还原库里记的模块级生命周期；为空或格式不符时返回 nil。
func DecodeLifecycle(raw string) *metricspb.ModuleLifecycle {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var lifecycle metricspb.ModuleLifecycle
	if protojson.Unmarshal([]byte(raw), &lifecycle) != nil || lifecycle.GetStage() == "" {
		return nil
	}
	return &lifecycle
}

// ingestBatchRows bounds the rows of one multi-row upsert. A report carries
// thousands of per-subject samples and monitor's SQLite has one connection, so
// the ingest transaction must be a handful of statements, not three per sample.
const ingestBatchRows = 500

// upsertSamples 用批量 upsert 写每个样本的序列：最近一次上报时间总是前进；最新值只前进到更新的观测，
// 水位类指标（monotonicMetric）也不往小里退。
func upsertSamples(tx *gorm.DB, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	now := time.Now().UTC()
	var plain, monotonic []MetricSeries
	for _, sample := range samples {
		observedAt := sample.ObservedAt.UTC()
		row := MetricSeries{
			SeriesID: sample.SeriesID, ServiceName: sample.ServiceName, InstanceID: sample.InstanceID,
			MetricName: sample.MetricName, MetricType: sample.MetricType, LabelsJSON: sample.LabelsJSON, LastSeenAt: observedAt,
			Value: sample.Value, ObservedAt: observedAt, IntervalSeconds: int(sample.Interval / time.Second),
			MessageID: sample.MessageID, ProducerNodeID: sample.ProducerNodeID, ProducerVersion: sample.ProducerVersion,
			CreatedAt: now, UpdatedAt: now,
		}
		if monotonicMetric(sample.MetricName) {
			monotonic = append(monotonic, row)
		} else {
			plain = append(plain, row)
		}
	}
	const newer = "excluded.c_observed_at > t_monitor_metric_series.c_observed_at"
	for _, group := range []struct {
		rows   []MetricSeries
		accept string
	}{
		{rows: plain, accept: newer},
		// Reporter 重启后内存水位可能为空。此时保留已提交的最新值，
		// 避免较新的抓取结果携带旧业务水位并造成数值倒退。
		{rows: monotonic, accept: newer + " AND excluded.c_value >= t_monitor_metric_series.c_value"},
	} {
		if len(group.rows) == 0 {
			continue
		}
		assignments := map[string]interface{}{
			"c_metric_name":  gorm.Expr("excluded.c_metric_name"),
			"c_metric_type":  gorm.Expr("excluded.c_metric_type"),
			"c_labels_json":  gorm.Expr("excluded.c_labels_json"),
			"c_last_seen_at": gorm.Expr("MAX(t_monitor_metric_series.c_last_seen_at, excluded.c_last_seen_at)"),
			"c_mtime":        gorm.Expr("CURRENT_TIMESTAMP"),
		}
		// 最新值的各列只在这次观测被采纳时才换（采纳条件在 SET 之前按旧值求值）。
		for _, column := range []string{"c_value", "c_observed_at", "c_interval_seconds", "c_message_id", "c_producer_node_id", "c_producer_version"} {
			assignments[column] = gorm.Expr("CASE WHEN " + group.accept + " THEN excluded." + column + " ELSE t_monitor_metric_series." + column + " END")
		}
		upsert := clause.OnConflict{Columns: []clause.Column{{Name: "c_series_id"}}, DoUpdates: clause.Assignments(assignments)}
		if err := tx.Clauses(upsert).CreateInBatches(&group.rows, ingestBatchRows).Error; err != nil {
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

// RetiredSeriesAfter is how long a series or reporter may go unreported before
// it is dropped: the View, subject or service behind it is gone. A series or
// reporter that is reported again is simply recreated.
const RetiredSeriesAfter = 24 * time.Hour

// ReporterDeployment identifies a registered component without relying on probes.
type ReporterDeployment struct{ NodeID, ServiceName string }

// PruneRetiredSeries removes the series (catalog and latest value) not reported
// since RetiredSeriesAfter before now, together with retired reporter history.
// The newest reporter per registered deployment survives cleanup, so a stopped
// component cannot become "never reported". A nil registration list means
// discovery is still unknown; an empty non-nil list is authoritative.
// maxRegisteredDeployments 与拓扑上限一致（1024 台主机，每台最多部署全部组件）。
const maxRegisteredDeployments = 1024 * 24

func (r *MetricMessageStore) PruneRetiredSeries(ctx context.Context, now time.Time, registered []ReporterDeployment) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("message store is not initialized")
	}
	if len(registered) > maxRegisteredDeployments {
		return 0, fmt.Errorf("registered metric deployments exceed limit %d", maxRegisteredDeployments)
	}
	cutoff := now.UTC().Add(-RetiredSeriesAfter)
	var pruned int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("c_last_seen_at < ?", cutoff).Delete(&MetricSeries{})
		if result.Error != nil {
			return result.Error
		}
		pruned = result.RowsAffected
		if registered == nil {
			return nil
		}
		retired := tx.Where("c_last_seen_at < ?", cutoff)
		if len(registered) > 0 {
			pairs := make([][]any, 0, len(registered))
			for _, deployment := range registered {
				pairs = append(pairs, []any{deployment.NodeID, deployment.ServiceName})
			}
			latest := tx.Raw(`SELECT c_id FROM (
				SELECT c_id, ROW_NUMBER() OVER (
					PARTITION BY c_node_id, c_service_name
					ORDER BY c_last_seen_at DESC, c_id DESC
				) AS deployment_rank
				FROM t_monitor_metric_services
				WHERE (c_node_id, c_service_name) IN ?
			) WHERE deployment_rank = 1`, pairs)
			retired = retired.Where("c_id NOT IN (?)", latest)
		}
		return retired.Delete(&MetricService{}).Error
	})
	return pruned, err
}

func (r *MetricMessageStore) GetLatest(ctx context.Context, seriesID string) (*MetricSeries, error) {
	if r == nil || r.db == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	var row MetricSeries
	err := r.db.WithContext(ctx).Where("c_series_id = ?", seriesID).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListLatestByMetricNames is intentionally limited to the generic View output
// watermark family. Scoped freshness evaluation should prefer
// ListLatestByViewScopes so unrelated View series are not loaded.
func (r *MetricMessageStore) ListLatestByMetricNames(ctx context.Context, names []string, limit int) ([]MetricSeries, error) {
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
	var rows []MetricSeries
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
func (r *MetricMessageStore) ListLatestByViewScopes(ctx context.Context, metricName string, scopes []ViewMetricScope, limit int) ([]MetricSeries, error) {
	if r == nil || r.db == nil {
		return nil, ErrMetricsStoreUnavailable
	}
	if _, ok := viewDatasetMetricNames[metricName]; !ok {
		return nil, fmt.Errorf("metric name %q is not a supported View dataset metric", metricName)
	}
	if len(scopes) == 0 {
		return []MetricSeries{}, nil
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
		spaceID, datasetID := strings.TrimSpace(scope.SpaceID), strings.TrimSpace(scope.DatasetID)
		frequency := strings.TrimSpace(scope.Frequency)
		if spaceID == "" || datasetID == "" || frequency == "" {
			continue
		}
		conditions = append(conditions, "(json_extract(c_labels_json, '$.space_id') = ? AND json_extract(c_labels_json, '$.dataset_id') = ? AND json_extract(c_labels_json, '$.freq') = ?)")
		args = append(args, spaceID, datasetID, frequency)
	}
	if len(conditions) == 0 {
		return []MetricSeries{}, nil
	}
	args = append([]interface{}{metricName}, args...)
	var rows []MetricSeries
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
