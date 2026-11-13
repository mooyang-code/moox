package observability

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

// ViewMetrics 是 View 与 Outbox 的指标：输出水位、就绪事件发布、删除最旧数据、恢复与跟随落后。
type ViewMetrics struct {
	outboxPendingEntries          prometheus.Gauge
	outboxOldestAge               prometheus.Gauge
	outboxPublishErrorsTotal      prometheus.Counter
	outboxDuplicatePublish        prometheus.Counter
	outboxPublisherReady          prometheus.Gauge
	outboxPublisherUnavailableAge prometheus.GaugeFunc
	outboxLastPublishSuccess      prometheus.Gauge
	outboxLastReconnect           prometheus.Gauge
	outboxReconnectAttempts       *prometheus.CounterVec
	viewOutputWatermark           *prometheus.GaugeVec
	// viewOutputArrival 是 View 输出水位最近一次前进时的墙上时间，Monitor 用它算最新一根 K 线的到达延迟。
	viewOutputArrival *prometheus.GaugeVec
	// now 只供测试替换时钟。
	now                     func() time.Time
	viewSubjects            *viewSubjectTracker
	readyPublishRetry       *prometheus.CounterVec
	expiredRowsDropped      *prometheus.CounterVec
	oldestRowsDeleted       *prometheus.CounterVec
	oldestDeleteLockSeconds prometheus.Histogram
	oldestDeleteLastRound   prometheus.Gauge
	restoreDuration         prometheus.Gauge
	restoreReady            prometheus.Gauge
	restoreFailures         prometheus.Counter
	viewFileOverLimit       prometheus.Gauge
	// followLag 是各 View 跟随变更日志的落后情况，采集时现算。
	followLag                        *viewFollowLagCollector
	followingSnapshot                atomic.Bool
	viewWatermarkMu                  sync.Mutex
	viewWatermarks                   map[string]int64
	outboxObservedSnapshot           atomic.Bool
	outboxDynamicAge                 atomic.Bool
	outboxOldestEventAt              atomic.Int64
	outboxPendingSnapshot            atomic.Int64
	outboxOldestAgeSnapshot          atomic.Int64
	outboxPublishErrorsSnapshot      atomic.Int64
	outboxDuplicateSnapshot          atomic.Int64
	outboxPublisherObservedSnapshot  atomic.Bool
	outboxPublisherReadySnapshot     atomic.Bool
	outboxPublisherUnavailableSince  atomic.Int64
	outboxLastPublishSuccessSnapshot atomic.Int64
	outboxLastReconnectSnapshot      atomic.Int64
	outboxReconnectSuccessSnapshot   atomic.Int64
	outboxReconnectFailureSnapshot   atomic.Int64
	outboxReconnectStatusSnapshot    atomic.Int64
	restoreDurationSnapshot          atomic.Int64
	restoreReadySnapshot             atomic.Bool
	restoreFailuresSnapshot          atomic.Int64
}

// ViewMetricsSnapshot is the aggregate runtime state exported by the view and
// outbox instrumentation. It intentionally has no subject, symbol, or message
// identity fields.
type ViewMetricsSnapshot struct {
	// Following 表示 View 已开始跟随变更日志。
	Following                     bool
	OutboxPendingEntries          int64
	OutboxObserved                bool
	OutboxOldestAge               time.Duration
	OutboxPublishErrorsTotal      int64
	OutboxDuplicatePublishTotal   int64
	OutboxPublisherObserved       bool
	OutboxPublisherReady          bool
	OutboxPublisherUnavailableAge time.Duration
	OutboxLastPublishSuccess      time.Time
	OutboxLastReconnectAt         time.Time
	OutboxReconnectSuccesses      int64
	OutboxReconnectFailures       int64
	OutboxReconnectStatus         string
	RestoreDuration               time.Duration
	RestoreReady                  bool
	RestoreFailures               int64
}

func NewViewMetrics(registerer prometheus.Registerer) (*ViewMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("storage view metrics registerer is nil")
	}
	metrics := &ViewMetrics{
		outboxPendingEntries: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "pending_entries",
			Help: "Number of pending Storage outbox entries.",
		}),
		outboxOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "oldest_age_seconds",
			Help: "Age of the oldest pending Storage outbox entry.",
		}),
		outboxPublishErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "publish_errors_total",
			Help: "Storage outbox publish errors.",
		}),
		outboxDuplicatePublish: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "duplicate_publish_total",
			Help: "Storage outbox publishes acknowledged as duplicates.",
		}),
		outboxPublisherReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "publisher_ready",
			Help: "Whether the Storage outbox EventBus publisher is connected.",
		}),
		outboxLastPublishSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "last_publish_success_timestamp_seconds",
			Help: "Unix timestamp of the latest successful Storage outbox publish.",
		}),
		outboxLastReconnect: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "last_reconnect_timestamp_seconds",
			Help: "Unix timestamp of the latest Storage outbox publisher reconnect attempt.",
		}),
		outboxReconnectAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "reconnect_attempts_total",
			Help: "Storage outbox publisher reconnect attempts by result.",
		}, []string{"result"}),
		viewOutputWatermark: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "output_watermark_timestamp_seconds",
			Help: "Latest business timestamp successfully committed to an active Storage View.",
		}, []string{"space_id", "view_id", "dataset_id", "freq"}),
		viewOutputArrival: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "output_arrival_timestamp_seconds",
			Help: "View 输出水位最近一次前进时的墙上时间（这批数据提交完成的时刻）。",
		}, []string{"space_id", "view_id", "dataset_id", "freq"}),
		now:          time.Now,
		viewSubjects: newViewSubjectTracker(),
		readyPublishRetry: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "ready_publish_retry_total",
			Help: "View source/result ready event publishes that need retry.",
		}, []string{"view", "event"}),
		expiredRowsDropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "expired_rows_dropped_total",
			Help: "Rows not written to a View because data_time is older than the maximum View age of its frequency.",
		}, []string{"space", "freq"}),
		oldestRowsDeleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "oldest_rows_deleted_total",
			Help: "Oldest rows deleted from time-series Views because their series exceeded the configured bars.",
		}, []string{"space", "view"}),
		oldestDeleteLockSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "oldest_delete_lock_seconds",
			Help:    "Time one oldest-row delete batch held the View index write lock.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1, 2},
		}),
		oldestDeleteLastRound: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "oldest_delete_last_round_timestamp_seconds",
			Help: "Wall time when the last oldest-row delete round finished, whether or not it deleted anything.",
		}),
		restoreDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "restore_duration_seconds",
			Help: "Duration of the latest active View restore pass.",
		}),
		restoreReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "restore_ready",
			Help: "Whether the latest active View restore pass completed successfully.",
		}),
		restoreFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "restore_failures_total",
			Help: "Number of failed active View restore passes.",
		}),
		viewFileOverLimit: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "file_over_limit",
			Help: "Number of active Views whose index file is at or above max_view_file_bytes; alert only, no data is touched.",
		}),
		followLag:      newViewFollowLagCollector(),
		viewWatermarks: make(map[string]int64),
	}
	metrics.outboxPublisherUnavailableAge = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "moox", Subsystem: "storage_outbox", Name: "publisher_unavailable_age_seconds",
		Help: "Continuous time the Storage outbox EventBus publisher has been unavailable.",
	}, func() float64 { return metrics.currentOutboxPublisherUnavailableAge(time.Now().UTC()).Seconds() })
	var err error
	if metrics.outboxPendingEntries, err = registerOrReuse(registerer, metrics.outboxPendingEntries); err != nil {
		return nil, err
	}
	if metrics.outboxOldestAge, err = registerOrReuse(registerer, metrics.outboxOldestAge); err != nil {
		return nil, err
	}
	if metrics.outboxPublishErrorsTotal, err = registerOrReuse(registerer, metrics.outboxPublishErrorsTotal); err != nil {
		return nil, err
	}
	if metrics.outboxDuplicatePublish, err = registerOrReuse(registerer, metrics.outboxDuplicatePublish); err != nil {
		return nil, err
	}
	if metrics.outboxPublisherReady, err = registerOrReuse(registerer, metrics.outboxPublisherReady); err != nil {
		return nil, err
	}
	if metrics.outboxPublisherUnavailableAge, err = registerOrReuse(registerer, metrics.outboxPublisherUnavailableAge); err != nil {
		return nil, err
	}
	if metrics.viewFileOverLimit, err = registerOrReuse(registerer, metrics.viewFileOverLimit); err != nil {
		return nil, err
	}
	if metrics.followLag, err = registerOrReuse(registerer, metrics.followLag); err != nil {
		return nil, err
	}
	if metrics.outboxLastPublishSuccess, err = registerOrReuse(registerer, metrics.outboxLastPublishSuccess); err != nil {
		return nil, err
	}
	if metrics.outboxLastReconnect, err = registerOrReuse(registerer, metrics.outboxLastReconnect); err != nil {
		return nil, err
	}
	if metrics.outboxReconnectAttempts, err = registerOrReuse(registerer, metrics.outboxReconnectAttempts); err != nil {
		return nil, err
	}
	if metrics.viewOutputWatermark, err = registerOrReuse(registerer, metrics.viewOutputWatermark); err != nil {
		return nil, err
	}
	if metrics.viewOutputArrival, err = registerOrReuse(registerer, metrics.viewOutputArrival); err != nil {
		return nil, err
	}
	if metrics.viewSubjects, err = registerOrReuse(registerer, metrics.viewSubjects); err != nil {
		return nil, err
	}
	if metrics.readyPublishRetry, err = registerOrReuse(registerer, metrics.readyPublishRetry); err != nil {
		return nil, err
	}
	if metrics.expiredRowsDropped, err = registerOrReuse(registerer, metrics.expiredRowsDropped); err != nil {
		return nil, err
	}
	if metrics.oldestRowsDeleted, err = registerOrReuse(registerer, metrics.oldestRowsDeleted); err != nil {
		return nil, err
	}
	if metrics.oldestDeleteLockSeconds, err = registerOrReuse(registerer, metrics.oldestDeleteLockSeconds); err != nil {
		return nil, err
	}
	if metrics.oldestDeleteLastRound, err = registerOrReuse(registerer, metrics.oldestDeleteLastRound); err != nil {
		return nil, err
	}
	if metrics.restoreDuration, err = registerOrReuse(registerer, metrics.restoreDuration); err != nil {
		return nil, err
	}
	if metrics.restoreReady, err = registerOrReuse(registerer, metrics.restoreReady); err != nil {
		return nil, err
	}
	if metrics.restoreFailures, err = registerOrReuse(registerer, metrics.restoreFailures); err != nil {
		return nil, err
	}
	return metrics, nil
}

// SetViewFileOverLimitCount 导出索引文件超过告警线的 View 数量，只告警，不动数据。
func (m *ViewMetrics) SetViewFileOverLimitCount(count int64) {
	if m == nil || m.viewFileOverLimit == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	m.viewFileOverLimit.Set(float64(count))
}

// ObserveViewDatasetOutput records a successful active View index commit.
// 跟踪的序列达到上限时，只在第一次返回 ErrViewSeriesLimit，之后的新序列静默丢弃（见 viewSubjectTracker.limitReported）。
func (m *ViewMetrics) ObserveViewDatasetOutput(observation ViewDatasetObservation) error {
	return m.viewSubjects.observe(observation)
}

// ObserveViewOutputWatermark advances the committed watermark for one active
// View. View IDs remain the metric identity so the monitor can create an
// independent freshness check, while dataset_id lets it associate the View
// with the Collector dataset that it serves.
func (m *ViewMetrics) ObserveViewOutputWatermark(spaceID, viewID, datasetID, frequency string, watermark time.Time) {
	if m == nil || strings.TrimSpace(spaceID) == "" || strings.TrimSpace(viewID) == "" || strings.TrimSpace(datasetID) == "" || watermark.IsZero() {
		return
	}
	frequency = strings.TrimSpace(frequency)
	if frequency == "" {
		return
	}
	spaceID = strings.TrimSpace(spaceID)
	viewID = strings.TrimSpace(viewID)
	datasetID = strings.TrimSpace(datasetID)
	watermarkUnix := watermark.UTC().Unix()
	key := strings.Join([]string{spaceID, viewID, datasetID, frequency}, "\x00")
	m.viewWatermarkMu.Lock()
	defer m.viewWatermarkMu.Unlock()
	if previous, ok := m.viewWatermarks[key]; ok && watermarkUnix <= previous {
		return
	}
	m.viewWatermarks[key] = watermarkUnix
	m.viewOutputWatermark.WithLabelValues(spaceID, viewID, datasetID, frequency).Set(float64(watermarkUnix))
	m.viewOutputArrival.WithLabelValues(spaceID, viewID, datasetID, frequency).Set(float64(m.now().UTC().Unix()))
	report.NotifyOutputAdvanced()
}

// ViewKey identifies a View by space and view ID.
type ViewKey struct {
	SpaceID string
	ViewID  string
}

// RetainViews drops the per-View output series of Views that are no longer
// active (deleted or deactivated). Gauge series are exported by the process
// until they are removed, so without this a deleted View keeps reporting its
// frozen output watermark as if it were alive and Monitor alerts that it is
// late forever. Call it only with the complete active-View set.
func (m *ViewMetrics) RetainViews(active map[ViewKey]struct{}) {
	if m == nil {
		return
	}
	m.viewWatermarkMu.Lock()
	for key := range m.viewWatermarks {
		parts := strings.Split(key, "\x00")
		if len(parts) != 4 {
			continue
		}
		if _, keep := active[ViewKey{SpaceID: parts[0], ViewID: parts[1]}]; keep {
			continue
		}
		m.viewOutputWatermark.DeleteLabelValues(parts...)
		m.viewOutputArrival.DeleteLabelValues(parts...)
		delete(m.viewWatermarks, key)
	}
	m.viewWatermarkMu.Unlock()
	m.viewSubjects.retain(active)
}

func (m *ViewMetrics) ObserveReadyPublishRetry(view, event string) {
	if m == nil {
		return
	}
	m.readyPublishRetry.WithLabelValues(view, event).Inc()
}

// ObserveViewExpiredRowsDropped 累计因超过最长保留期而未写入 View 的行数。
func (m *ViewMetrics) ObserveViewExpiredRowsDropped(space, freq string, rows int) {
	if m == nil || rows <= 0 {
		return
	}
	m.expiredRowsDropped.WithLabelValues(space, freq).Add(float64(rows))
}

// ObserveViewOldestRowsDeleted 记录一批删除最旧数据：删除的行数与这一批持有写锁的时长。
func (m *ViewMetrics) ObserveViewOldestRowsDeleted(space, view string, rows uint64, lockHeld time.Duration) {
	if m == nil {
		return
	}
	m.oldestDeleteLockSeconds.Observe(lockHeld.Seconds())
	if rows > 0 {
		m.oldestRowsDeleted.WithLabelValues(space, view).Add(float64(rows))
	}
}

// ObserveViewOldestDeleteRound 记录一轮删除最旧数据结束的时间（没有可删的也记），用来确认定时任务在跑。
func (m *ViewMetrics) ObserveViewOldestDeleteRound(finished time.Time) {
	if m == nil {
		return
	}
	m.oldestDeleteLastRound.Set(float64(finished.UnixNano()) / 1e9)
}

// ObserveRestore records the latest startup restore result without adding
// View- or dataset-level labels. A failed pass leaves readiness false while
// the liveness endpoint remains available for diagnostics.
func (m *ViewMetrics) ObserveRestore(success bool, elapsed time.Duration) {
	if m == nil {
		return
	}
	if elapsed < 0 {
		elapsed = 0
	}
	m.restoreDurationSnapshot.Store(elapsed.Nanoseconds())
	m.restoreDuration.Set(elapsed.Seconds())
	if success {
		m.restoreReadySnapshot.Store(true)
		m.restoreReady.Set(1)
		return
	}
	m.restoreReadySnapshot.Store(false)
	m.restoreReady.Set(0)
	m.restoreFailuresSnapshot.Add(1)
	m.restoreFailures.Inc()
}

// SetFollowing 记下 View 是否已开始跟随变更日志（就绪判据之一）。
func (m *ViewMetrics) SetFollowing(following bool) {
	if m != nil {
		m.followingSnapshot.Store(following)
	}
}

func registerOrReuse[T prometheus.Collector](registerer prometheus.Registerer, collector T) (T, error) {
	if err := registerer.Register(collector); err == nil {
		return collector, nil
	} else {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(T); ok {
				return existing, nil
			}
		}
		return collector, err
	}
}

func mustDefaultViewMetrics() *ViewMetrics {
	metrics, err := NewViewMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		panic(err)
	}
	return metrics
}

var DefaultViewMetrics = mustDefaultViewMetrics()

// SetOutboxSnapshotAt records the source timestamp of the oldest pending
// entry. Health checks can then observe an increasing age even if the relay
// stops polling after the entry was first observed.
func (m *ViewMetrics) SetOutboxSnapshotAt(pending int, oldestEventAt time.Time) {
	if m == nil {
		return
	}
	if pending < 0 {
		pending = 0
	}
	if pending == 0 || oldestEventAt.IsZero() {
		oldestEventAt = time.Time{}
	}
	m.outboxObservedSnapshot.Store(true)
	m.outboxDynamicAge.Store(true)
	if oldestEventAt.IsZero() {
		m.outboxOldestEventAt.Store(0)
	} else {
		m.outboxOldestEventAt.Store(oldestEventAt.UTC().UnixNano())
	}
	age := m.currentOutboxOldestAge(time.Now().UTC())
	m.outboxPendingEntries.Set(float64(pending))
	m.outboxOldestAge.Set(age.Seconds())
	m.outboxPendingSnapshot.Store(int64(pending))
	m.outboxOldestAgeSnapshot.Store(age.Nanoseconds())
}

func (m *ViewMetrics) currentOutboxOldestAge(now time.Time) time.Duration {
	if m == nil {
		return 0
	}
	if !m.outboxDynamicAge.Load() {
		return time.Duration(m.outboxOldestAgeSnapshot.Load())
	}
	nanos := m.outboxOldestEventAt.Load()
	if nanos <= 0 {
		return 0
	}
	oldest := time.Unix(0, nanos)
	if !now.After(oldest) {
		return 0
	}
	return now.Sub(oldest)
}

func (m *ViewMetrics) IncOutboxPublishError() {
	if m != nil {
		m.outboxPublishErrorsTotal.Inc()
		m.outboxPublishErrorsSnapshot.Add(1)
	}
}

func (m *ViewMetrics) IncOutboxDuplicatePublish() {
	if m != nil {
		m.outboxDuplicatePublish.Inc()
		m.outboxDuplicateSnapshot.Add(1)
	}
}

// SetOutboxPublisherReady records the current transport state. Repeated
// unavailable observations preserve the first failure time so health checks
// can distinguish a short reconnect from a sustained outage.
func (m *ViewMetrics) SetOutboxPublisherReady(ready bool) {
	if m == nil {
		return
	}
	m.outboxPublisherObservedSnapshot.Store(true)
	m.outboxPublisherReadySnapshot.Store(ready)
	if ready {
		m.outboxPublisherUnavailableSince.Store(0)
		m.outboxPublisherReady.Set(1)
		return
	}
	m.outboxPublisherUnavailableSince.CompareAndSwap(0, time.Now().UTC().UnixNano())
	m.outboxPublisherReady.Set(0)
}

func (m *ViewMetrics) currentOutboxPublisherUnavailableAge(now time.Time) time.Duration {
	if m == nil || m.outboxPublisherReadySnapshot.Load() {
		return 0
	}
	nanos := m.outboxPublisherUnavailableSince.Load()
	if nanos <= 0 {
		return 0
	}
	unavailableSince := time.Unix(0, nanos)
	if !now.After(unavailableSince) {
		return 0
	}
	return now.Sub(unavailableSince)
}

func (m *ViewMetrics) ObserveOutboxPublishSuccess() {
	if m == nil {
		return
	}
	now := time.Now().UTC()
	m.outboxLastPublishSuccessSnapshot.Store(now.UnixNano())
	m.outboxLastPublishSuccess.Set(float64(now.Unix()))
	m.SetOutboxPublisherReady(true)
}

func (m *ViewMetrics) ObserveOutboxReconnect(success bool) {
	if m == nil {
		return
	}
	now := time.Now().UTC()
	m.outboxLastReconnectSnapshot.Store(now.UnixNano())
	m.outboxLastReconnect.Set(float64(now.Unix()))
	if success {
		m.outboxReconnectAttempts.WithLabelValues("success").Inc()
		m.outboxReconnectSuccessSnapshot.Add(1)
		m.outboxReconnectStatusSnapshot.Store(1)
		m.SetOutboxPublisherReady(true)
		return
	}
	m.outboxReconnectAttempts.WithLabelValues("failed").Inc()
	m.outboxReconnectFailureSnapshot.Add(1)
	m.outboxReconnectStatusSnapshot.Store(-1)
	m.SetOutboxPublisherReady(false)
}

func atomicTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}

func outboxReconnectStatus(value int64) string {
	switch value {
	case 1:
		return "success"
	case -1:
		return "failed"
	default:
		return "not_attempted"
	}
}

func (m *ViewMetrics) Snapshot() ViewMetricsSnapshot {
	if m == nil {
		return ViewMetricsSnapshot{}
	}
	now := time.Now().UTC()
	oldestOutboxAge := m.currentOutboxOldestAge(now)
	m.outboxOldestAge.Set(oldestOutboxAge.Seconds())
	m.outboxOldestAgeSnapshot.Store(oldestOutboxAge.Nanoseconds())
	return ViewMetricsSnapshot{
		Following:                     m.followingSnapshot.Load(),
		OutboxPendingEntries:          m.outboxPendingSnapshot.Load(),
		OutboxObserved:                m.outboxObservedSnapshot.Load(),
		OutboxOldestAge:               oldestOutboxAge,
		OutboxPublishErrorsTotal:      m.outboxPublishErrorsSnapshot.Load(),
		OutboxDuplicatePublishTotal:   m.outboxDuplicateSnapshot.Load(),
		OutboxPublisherObserved:       m.outboxPublisherObservedSnapshot.Load(),
		OutboxPublisherReady:          m.outboxPublisherReadySnapshot.Load(),
		OutboxPublisherUnavailableAge: m.currentOutboxPublisherUnavailableAge(now),
		OutboxLastPublishSuccess:      atomicTime(m.outboxLastPublishSuccessSnapshot.Load()),
		OutboxLastReconnectAt:         atomicTime(m.outboxLastReconnectSnapshot.Load()),
		OutboxReconnectSuccesses:      m.outboxReconnectSuccessSnapshot.Load(),
		OutboxReconnectFailures:       m.outboxReconnectFailureSnapshot.Load(),
		OutboxReconnectStatus:         outboxReconnectStatus(m.outboxReconnectStatusSnapshot.Load()),
		RestoreDuration:               time.Duration(m.restoreDurationSnapshot.Load()),
		RestoreReady:                  m.restoreReadySnapshot.Load(),
		RestoreFailures:               m.restoreFailuresSnapshot.Load(),
	}
}
