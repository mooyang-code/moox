package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/report"
)

const (
	MaxDatasetFrequencyStatuses = 1000
	maxOverviewServices         = 1000
	maxOverviewMetricNames      = 2000
)

type ServiceStatus struct {
	NodeID, ServiceName, InstanceID, Status, Reason string
	LastSeenAt                                      time.Time
	ReporterStatus                                  string
	Enabled                                         bool
	ProbeStatus, ProbeReason, ProbeRawError         string
	ProbeCheckedAt                                  time.Time
	Instances                                       []ReporterInstance
	ProbeURL                                        string
	ProbeHistory                                    []domain.CheckResult
	// Lifecycle 是最近一次探测的健康响应里带的生命周期状态；模块没有上报时为 nil。
	Lifecycle *LifecycleStatus
}

type HostStatus struct {
	HostID, AgentID, Hostname, Status, Reason       string
	LastSeenAt                                      time.Time
	CPUPercent, MemoryPercent, FilesystemMaxPercent float64
	// 资源总量：CPU 逻辑核数、内存总量与已用量；磁盘是使用率最高的文件系统（与 FilesystemMaxPercent 同一个）。
	CPUCores                                                       uint32
	MemoryTotalBytes, MemoryUsedBytes                              uint64
	FilesystemTotalBytes, FilesystemUsedBytes                      uint64
	FilesystemMountpoint                                           string
	MemoryAvailable, DiskAvailable, MetricsAvailable, CPUAvailable bool
}

type DatasetFrequencyStatus struct {
	Producer, SpaceID, DatasetID, Freq, Status, Reason string
	// DisplayName 是生产者随期望清单上报的展示名称（moox_<模块>_dataset_info 的 name 标签，例如策略实例名），
	// 没有上报时为空，由调用方退回 Storage 里的数据集名称或数据集 ID。
	DisplayName string
	// PrimaryDatasetID links an independent Storage View fact back to the
	// dataset whose Collector produces its input. It is empty for other
	// producers and keeps DatasetID available as the View identity.
	PrimaryDatasetID         string
	LastRunAt, LastSuccessAt time.Time
	// LastPartialAt 是最近一次“按设计只产出部分结果”的运行（数据积累中），见 DatasetReasonWarming。
	LastPartialAt                       time.Time
	InputWatermarkAt, OutputWatermarkAt time.Time
	LastReportedAt                      time.Time
	// 最新一根 K 线（由输出水位识别，水位是它在存储里的 data_time）：LatestBarClose 是按市场交易日历算出的收线时间，
	// OutputArrivedAt 是生产者写完它的时刻。延迟 = 到达 − 收线，在到达那一刻定值，不随当前时间增长；
	// 生产者没有上报到达时间时 DelayKnown 为 false。
	LatestBarClose  time.Time
	OutputArrivedAt time.Time
	DelaySeconds    int64
	DelayKnown      bool
	// ExpectedBarAt、ExpectedBarClose 是预期 K 线：按市场交易日历此刻应已收线的最新一根。ExpectedBarAt 是它的 data_time
	// （开盘时间），与 OutputWatermarkAt 相同即已到；无法按交易日历判断时为零。
	ExpectedBarAt, ExpectedBarClose time.Time
	// WaitingBars 是此刻应已收线却还没到的根数；WaitingSince 是其中最早一根的收线时间（应到时间），
	// OverdueSeconds 是它已晚多久（当前时间 − 应到时间）。
	WaitingBars    int
	WaitingSince   time.Time
	OverdueSeconds int64
	// LateConfirmed 表示应到的那根已由生产者的上报确认迟到：上报那一刻它已晚超过允许迟到仍没有（见 evaluateBars）。
	LateConfirmed bool
	// LatenessLimitSeconds 是允许迟到的秒数：生产者的上报确认应到的那根超过它仍未到时判为过期并告警；
	// 已到的根延迟超过它只标为迟到，不影响当前状态。
	LatenessLimitSeconds int64
	// ClockIssue 说明无法按交易日历判断的原因，例如“日历未覆盖”“周期对齐未确认”；为空表示已按日历判断。
	ClockIssue string
	// Timezone 是市场时区（IANA 名称）：加密货币 UTC，A 股 Asia/Shanghai；没有交易日历时为 UTC。
	Timezone string
}

type BusinessStatus struct {
	SpaceID, Kind, Module, Status, Reason string
	CheckID, RawError, Name               string
	LastCheckedAt                         time.Time
}

type Overview struct {
	GeneratedAt    time.Time
	Services       []ServiceStatus
	Unregistered   []ServiceStatus
	TopologyKnown  bool
	Topology       *domain.TopologySnapshot
	Hosts          []HostStatus
	Datasets       []DatasetFrequencyStatus
	BusinessChecks []BusinessStatus
	GatewaySignals []GatewaySignal
}

type Builder struct {
	Metrics                    *monmetrics.QueryService
	Hosts                      *hostmetrics.Store
	Checks                     *store.CheckRepository
	Topology                   *store.TopologyRepository
	Gateways                   *store.GatewayRepository
	Results                    *store.ResultRepository
	Policy                     report.RealtimeTimeSeriesPolicy
	BalanceDifferenceThreshold float64
	MarketFetchThresholds      MarketFetchThresholds
	// Inventory 是采集任务清单，用来确定每个空间的市场交易日历（市场、日历、时区、交易时段）。
	Inventory InventorySource
	Now       func() time.Time
}

// InventorySource 提供最近一份有效的采集任务清单；monmetrics.TaskResultInventoryCache 实现它。
type InventorySource interface {
	GetAllowStale(ctx context.Context, now time.Time) (monmetrics.TaskResultInventorySnapshot, bool, error)
}

type MarketFetchThresholds struct {
	CoordinationStaleAfter      time.Duration
	PendingGrace                time.Duration
	LeaseGrace                  time.Duration
	FeedFailureRateWindow       time.Duration
	FeedFailureRateThreshold    float64
	InstrumentSnapshotMaxAge    time.Duration
	InstrumentMinimumCount      int
	InstrumentRequiredExchanges []string
}

func (b Builder) Build(ctx context.Context, spaceID string) (Overview, error) {
	if err := ctx.Err(); err != nil {
		return Overview{}, err
	}
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	out := Overview{GeneratedAt: now}
	var err error
	if out.Services, out.Unregistered, out.Topology, err = b.buildServices(ctx, now); err != nil {
		return Overview{}, err
	}
	out.TopologyKnown = out.Topology != nil
	if out.Hosts, err = b.buildHosts(ctx, now); err != nil {
		return Overview{}, err
	}
	if out.Datasets, err = b.buildDatasets(ctx, spaceID, now); err != nil {
		return Overview{}, err
	}
	if out.BusinessChecks, err = b.buildBusinessChecks(ctx, spaceID); err != nil {
		return Overview{}, err
	}
	coordination, err := b.buildMarketFetchCoordination(ctx, spaceID, now)
	if err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = append(out.BusinessChecks, coordination...)
	streams, err := b.buildCollectorStreamHealth(ctx, spaceID, now)
	if err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = append(out.BusinessChecks, streams...)
	watermarks, err := b.buildFactorWatermarkHealth(ctx, spaceID, now)
	if err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = append(out.BusinessChecks, watermarks...)
	rejections, err := b.buildDeadlineRejectionHealth(ctx, spaceID, now)
	if err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = append(out.BusinessChecks, rejections...)
	signals, err := b.buildMarketFetchSignalHealth(ctx, spaceID, now)
	if err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = append(out.BusinessChecks, signals...)
	delivery, err := b.buildStorageOutboxHealth(ctx, spaceID, now)
	if err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = append(out.BusinessChecks, delivery...)
	presence, err := b.buildWorkerPresence(ctx, spaceID, now)
	if err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = append(out.BusinessChecks, presence...)
	seriesLimit, err := b.buildSeriesLimitHealth(ctx, spaceID, now)
	if err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = append(out.BusinessChecks, seriesLimit...)
	if out.GatewaySignals, err = b.gatewaySignals(ctx, now); err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = uniqueBusinessChecks(out.BusinessChecks)
	sortOverview(&out)
	return out, nil
}

const storageOutboxBacklogGrace = 2 * time.Minute

// buildSeriesLimitHealth 检查指标目录的新序列增长：新序列被限额丢弃，或近 1 小时 / 24 小时的新增数接近上限，都视为异常。
// 这通常是某个上报方的标签失控（例如把标的、请求 ID 放进了标签），不处理会撑大目录并让真正的指标被挤掉。
func (b Builder) buildSeriesLimitHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if spaceID != "" && spaceID != monmetrics.InternalMetricSpaceID {
		return nil, nil
	}
	if b.Metrics == nil {
		return nil, nil
	}
	limit, err := b.Metrics.SeriesLimitStatus(ctx, now)
	if err != nil {
		return nil, err
	}
	if !limit.Enabled {
		return nil, nil
	}
	status, reason := "healthy", fmt.Sprintf("指标序列新增正常（近 1 小时 %s，近 24 小时 %s）", seriesQuota(limit.HourCurrent, limit.HourMax), seriesQuota(limit.DayCurrent, limit.DayMax))
	if !limit.Healthy() {
		status = "down"
		reason = fmt.Sprintf("指标序列新增过快（近 1 小时 %s，近 24 小时 %s）", seriesQuota(limit.HourCurrent, limit.HourMax), seriesQuota(limit.DayCurrent, limit.DayMax))
		if limit.HasDropping {
			top := limit.Drops[0]
			reason += fmt.Sprintf("，已丢弃 %s 的新序列 %d 条（最近一条 %s），请检查它的指标标签是否失控", top.Service, top.Total, top.LastMetric)
		}
	}
	return []BusinessStatus{{
		SpaceID: monmetrics.InternalMetricSpaceID, Kind: "metrics_ingest", Module: "series_limit",
		Status: status, Reason: reason, LastCheckedAt: now,
	}}, nil
}

// seriesQuota 把“当前 / 上限”写成文字，上限为 0 表示该窗口不限。
func seriesQuota(current, max int64) string {
	if max <= 0 {
		return fmt.Sprintf("%d 条（不限）", current)
	}
	return fmt.Sprintf("%d/%d", current, max)
}

// buildStorageOutboxHealth detects an interrupted Primary-to-View delivery
// path before a Dataset output watermark has had time to become stale.
func (b Builder) buildStorageOutboxHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if spaceID != "" && spaceID != monmetrics.InternalMetricSpaceID {
		return nil, nil
	}
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	readMax := func(metricName string) (float64, bool, error) {
		series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", metricName, "", 100, now)
		if err != nil {
			return 0, false, err
		}
		var maximum float64
		found := false
		for _, item := range series {
			if item.IsStale {
				continue
			}
			latest, err := b.Metrics.Latest(ctx, item.SeriesID)
			if err != nil {
				return 0, false, err
			}
			if !found || latest.Value > maximum {
				maximum = latest.Value
			}
			found = true
		}
		return maximum, found, nil
	}
	pending, hasPending, err := readMax("moox_storage_outbox_pending_entries")
	if err != nil {
		return nil, err
	}
	ageSeconds, hasAge, err := readMax("moox_storage_outbox_oldest_age_seconds")
	if err != nil {
		return nil, err
	}
	if !hasPending && !hasAge {
		return nil, nil
	}
	status, reason := "healthy", "数据变更投递正常"
	if pending > 0 && ageSeconds > storageOutboxBacklogGrace.Seconds() {
		status = "down"
		minutes := max(1, int(ageSeconds)/60)
		reason = fmt.Sprintf("数据变更投递积压 %.0f 条，最早一条已等待约 %d 分钟", pending, minutes)
	} else if pending > 0 {
		reason = fmt.Sprintf("正在投递 %.0f 条数据变更", pending)
	}
	return []BusinessStatus{{
		SpaceID: monmetrics.InternalMetricSpaceID, Kind: "data_delivery", Module: "storage_outbox",
		Status: status, Reason: reason, LastCheckedAt: now,
	}}, nil
}

func (b Builder) buildHosts(ctx context.Context, now time.Time) ([]HostStatus, error) {
	if b.Hosts == nil {
		return []HostStatus{}, nil
	}
	rows, err := b.Hosts.ListAgentsAt(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make([]HostStatus, 0, len(rows))
	for _, row := range rows {
		status, reason := "healthy", "agent reachable"
		if !row.Reachable {
			status, reason = "down", "agent unreachable"
		}
		item := HostStatus{HostID: row.HostID, AgentID: row.AgentID, Hostname: row.Hostname, Status: status, Reason: reason}
		item.LastSeenAt, _ = time.Parse(time.RFC3339Nano, row.LastSeenAt)
		if snapshot := row.Snapshot; snapshot != nil {
			item.MetricsAvailable = true
			item.CPUCores = snapshot.GetCpu().GetLogicalCores()
			if snapshot.GetCpu() != nil && snapshot.GetCpu().GetUsageAvailable() {
				item.CPUAvailable = true
				item.CPUPercent = snapshot.GetCpu().GetUsagePercent()
			}
			if snapshot.GetMemory() != nil {
				item.MemoryAvailable = true
				item.MemoryPercent = snapshot.GetMemory().GetUsagePercent()
				item.MemoryTotalBytes, item.MemoryUsedBytes = snapshot.GetMemory().GetTotalBytes(), snapshot.GetMemory().GetUsedBytes()
			}
			item.DiskAvailable = len(snapshot.GetFilesystems()) > 0
			for index, filesystem := range snapshot.GetFilesystems() {
				if index == 0 || filesystem.GetUsagePercent() > item.FilesystemMaxPercent {
					item.FilesystemMaxPercent = filesystem.GetUsagePercent()
					item.FilesystemTotalBytes, item.FilesystemUsedBytes = filesystem.GetTotalBytes(), filesystem.GetUsedBytes()
					item.FilesystemMountpoint = filesystem.GetMountpoint()
				}
			}
		}
		out = append(out, item)
	}
	return out, nil
}

type datasetKey struct {
	service, producer, instance, spaceID, datasetID, primaryDatasetID, freq, labels string
}

type datasetValues struct {
	interval, inventory, lastRun, lastSuccess, lastPartial, input, output float64
	// arrival 是输出水位最近一次前进时生产者写完的墙上时间。
	arrival       float64
	reporterStale bool
	reportedAt    time.Time
	// confirmedAt 是能确认最新一根“到此刻还没更新”的时刻，即生产者上报快照的时刻；多个实例合并后见 confirmedUntil。
	confirmedAt time.Time
}

func (b Builder) buildDatasets(ctx context.Context, spaceID string, now time.Time) ([]DatasetFrequencyStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return []DatasetFrequencyStatus{}, nil
	}
	names, total, err := b.Metrics.Catalog().ListNames(ctx, "", 0, 500)
	if err != nil {
		return nil, err
	}
	if total > maxOverviewMetricNames {
		return nil, fmt.Errorf("observability metric names exceed limit %d", maxOverviewMetricNames)
	}
	for offset := len(names); int64(offset) < total; offset = len(names) {
		rows, _, err := b.Metrics.Catalog().ListNames(ctx, "", offset, 500)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		names = append(names, rows...)
	}
	var enabledSeries []monmetrics.MetricSeries
	for _, name := range names {
		if !strings.HasPrefix(name.MetricName, "moox_") || !strings.HasSuffix(name.MetricName, "_dataset_enabled") {
			continue
		}
		rows, total, err := b.Metrics.Catalog().ListSeries(ctx, name.ServiceName, name.MetricName, "", 0, MaxDatasetFrequencyStatuses+1)
		if err != nil {
			return nil, err
		}
		if total > MaxDatasetFrequencyStatuses {
			return nil, datasetLimitError()
		}
		if err := ensureDatasetLimit(len(enabledSeries) + len(rows)); err != nil {
			return nil, err
		}
		enabledSeries = append(enabledSeries, rows...)
	}
	currentBoots, err := b.Metrics.Catalog().CurrentBootIDs(ctx, now)
	if err != nil {
		return nil, err
	}
	values := make(map[datasetKey]datasetValues, len(enabledSeries))
	for _, series := range enabledSeries {
		labels, err := datasetLabels(series.LabelsJSON)
		if err != nil {
			return nil, fmt.Errorf("dataset labels for %s: %w", series.SeriesID, err)
		}
		if labels["space_id"] == "" || labels["dataset_id"] == "" || labels["freq"] == "" {
			return nil, fmt.Errorf("dataset labels for %s are incomplete", series.SeriesID)
		}
		if spaceID != "" && labels["space_id"] != spaceID {
			continue
		}
		enabled, err := b.Metrics.Latest(ctx, series.SeriesID)
		if err != nil {
			return nil, err
		}
		if enabled.Value <= 0 {
			continue
		}
		if bootID := currentBoots[monmetrics.ReporterInstanceKey(series.ServiceName, series.InstanceID)]; !metricSampleFromBoot(enabled.MessageID, bootID) {
			continue
		}
		key := datasetKey{
			service: series.ServiceName, producer: datasetModuleFromMetric(series.MetricName),
			instance: series.InstanceID, spaceID: labels["space_id"],
			datasetID: labels["dataset_id"], freq: labels["freq"], labels: series.LabelsJSON,
		}
		values[key] = datasetValues{reporterStale: series.IsStale, reportedAt: enabled.ObservedAt.UTC()}
	}
	if err := b.populateDatasetValues(ctx, values, enabledSeries); err != nil {
		return nil, err
	}
	expectedScopes := make(map[string]float64, len(values))
	for key, current := range values {
		scope := datasetScopeKey(key.spaceID, key.datasetID, key.freq)
		if current.interval > 0 && (expectedScopes[scope] == 0 || current.interval < expectedScopes[scope]) {
			expectedScopes[scope] = current.interval
		}
	}
	storageValues := make(map[datasetKey]datasetValues)
	for _, name := range names {
		if datasetModuleFromMetric(name.MetricName) != "storage" ||
			!strings.HasSuffix(name.MetricName, "_dataset_last_run_timestamp_seconds") {
			continue
		}
		rows, total, err := b.Metrics.Catalog().ListSeries(ctx, name.ServiceName, name.MetricName, "", 0, MaxDatasetFrequencyStatuses+1)
		if err != nil {
			return nil, err
		}
		if total > MaxDatasetFrequencyStatuses {
			return nil, datasetLimitError()
		}
		for _, series := range rows {
			labels, err := datasetLabels(series.LabelsJSON)
			if err != nil {
				return nil, fmt.Errorf("dataset labels for %s: %w", series.SeriesID, err)
			}
			if labels["space_id"] == "" || labels["dataset_id"] == "" || labels["freq"] == "" {
				return nil, fmt.Errorf("dataset labels for %s are incomplete", series.SeriesID)
			}
			if spaceID != "" && labels["space_id"] != spaceID {
				continue
			}
			interval, expected := expectedScopes[datasetScopeKey(labels["space_id"], labels["dataset_id"], labels["freq"])]
			if !expected {
				continue
			}
			key := datasetKey{
				service: series.ServiceName, producer: "storage", instance: series.InstanceID,
				spaceID: labels["space_id"], datasetID: labels["dataset_id"],
				freq: labels["freq"], labels: series.LabelsJSON,
			}
			if _, exists := storageValues[key]; !exists {
				storageValues[key] = datasetValues{
					interval:      interval,
					reporterStale: series.IsStale,
				}
			}
		}
	}
	if err := b.populateDatasetValues(ctx, storageValues, enabledSeries); err != nil {
		return nil, err
	}
	if err := ensureDatasetLimit(len(values) + len(storageValues)); err != nil {
		return nil, err
	}
	for key, current := range storageValues {
		values[key] = current
	}
	// Storage View exposes its own committed watermark because a View can lag
	// behind the authoritative Primary dataset while the latter remains healthy.
	// Treat each View/frequency tuple as an independent freshness producer so it
	// gets a separate business check instead of being folded into storage output.
	const viewWatermarkMetric = "moox_storage_view_output_watermark_timestamp_seconds"
	for _, name := range names {
		if name.MetricName != viewWatermarkMetric {
			continue
		}
		rows, total, err := b.Metrics.Catalog().ListSeries(ctx, name.ServiceName, viewWatermarkMetric, "", 0, MaxDatasetFrequencyStatuses+1)
		if err != nil {
			return nil, err
		}
		if total > MaxDatasetFrequencyStatuses {
			return nil, datasetLimitError()
		}
		for _, series := range rows {
			labels, err := datasetLabels(series.LabelsJSON)
			if err != nil {
				return nil, fmt.Errorf("view watermark labels for %s: %w", series.SeriesID, err)
			}
			viewSpaceID, viewID, freq := labels["space_id"], labels["view_id"], labels["freq"]
			if viewSpaceID == "" || viewID == "" || freq == "" {
				continue
			}
			if spaceID != "" && viewSpaceID != spaceID {
				continue
			}
			interval := parseOverviewFrequency(freq)
			if interval <= 0 {
				continue
			}
			latest, err := b.Metrics.Latest(ctx, series.SeriesID)
			if err != nil {
				return nil, err
			}
			observed := latest.ObservedAt.UTC().Unix()
			arrival, err := b.viewArrival(ctx, series)
			if err != nil {
				return nil, err
			}
			key := datasetKey{service: series.ServiceName, producer: "storage_view", instance: series.InstanceID, spaceID: viewSpaceID, datasetID: viewID, primaryDatasetID: labels["dataset_id"], freq: freq, labels: series.LabelsJSON}
			values[key] = datasetValues{interval: interval.Seconds(), lastRun: float64(observed), lastSuccess: float64(observed), output: latest.Value, arrival: arrival, reporterStale: series.IsStale, reportedAt: latest.ObservedAt.UTC()}
		}
	}
	type aggregatedDataset struct {
		key   datasetKey
		value datasetValues
	}
	aggregated := make(map[string]aggregatedDataset, len(values))
	for key, value := range values {
		identity := strings.Join([]string{key.producer, key.spaceID, key.datasetID, key.freq}, "\x00")
		current, exists := aggregated[identity]
		if !exists {
			current = aggregatedDataset{
				key: datasetKey{
					producer: key.producer, spaceID: key.spaceID,
					datasetID: key.datasetID, primaryDatasetID: key.primaryDatasetID, freq: key.freq,
				},
				value: value,
			}
			current.value.confirmedAt = value.reportedAt
		} else {
			current.value.confirmedAt = confirmedUntil(current.value, value)
			current.value.interval = minPositive(current.value.interval, value.interval)
			current.value.inventory = max(current.value.inventory, value.inventory)
			current.value.lastRun = max(current.value.lastRun, value.lastRun)
			current.value.lastSuccess = max(current.value.lastSuccess, value.lastSuccess)
			current.value.lastPartial = max(current.value.lastPartial, value.lastPartial)
			current.value.input = max(current.value.input, value.input)
			// 到达时间跟着最新的输出走；多个实例写到同一根时取最早到的那次。
			switch {
			case value.output > current.value.output:
				current.value.arrival = value.arrival
			case value.output == current.value.output && value.arrival > 0 && (current.value.arrival == 0 || value.arrival < current.value.arrival):
				current.value.arrival = value.arrival
			}
			current.value.output = max(current.value.output, value.output)
			current.value.reportedAt = maxTime(current.value.reportedAt, value.reportedAt)
			current.value.reporterStale = current.value.reporterStale && value.reporterStale
		}
		aggregated[identity] = current
	}
	displayNames := b.datasetDisplayNames(ctx, names, spaceID)
	clocks := b.spaceClocks(ctx, now)
	out := make([]DatasetFrequencyStatus, 0, len(aggregated))
	for identity, current := range aggregated {
		clock, ok := clocks[current.key.spaceID]
		if !ok {
			clock = spaceClock{err: monmetrics.ErrNoTradingCalendar}
		}
		item := datasetStatus(now, current.key, current.value, b.Policy, clock)
		item.DisplayName = displayNames[identity]
		out = append(out, item)
	}
	return out, nil
}

// maxDatasetNameSeries 是读取数据集名称序列的总条数上限。名称序列随改名留下历史身份，过期后由指标保留期清理；
// 超出上限的部分不再读取，对应数据集退回默认名称，不影响状态判断。
const maxDatasetNameSeries = 4 * MaxDatasetFrequencyStatuses

// datasetDisplayNames 读取生产者上报的数据集展示名称，键与数据集聚合身份一致（生产者、空间、数据集、频率）。
// 只认仍在上报的序列；同一数据集有多条（刚改名，旧序列还没过期）时取最近上报的那条。名称只用于给检查起名：
// 读取出错时停止读取、返回已读到的部分，其余数据集退回默认名称，不让概览和业务检查因此失败。
func (b Builder) datasetDisplayNames(ctx context.Context, names []monmetrics.MetricName, spaceID string) map[string]string {
	type candidate struct {
		name   string
		seenAt time.Time
	}
	best := make(map[string]candidate)
	read := 0
metrics:
	for _, metric := range names {
		if !strings.HasPrefix(metric.MetricName, "moox_") || !strings.HasSuffix(metric.MetricName, "_dataset_info") {
			continue
		}
		producer := datasetModuleFromMetric(metric.MetricName)
		if producer == "" {
			continue
		}
		for offset := 0; read < maxDatasetNameSeries; {
			rows, total, err := b.Metrics.Catalog().ListSeries(ctx, metric.ServiceName, metric.MetricName, "", offset, 500)
			if err != nil {
				break metrics
			}
			read += len(rows)
			for _, series := range rows {
				if series.IsStale {
					continue
				}
				labels, err := datasetLabels(series.LabelsJSON)
				if err != nil {
					continue
				}
				name := strings.TrimSpace(labels["name"])
				if name == "" || labels["space_id"] == "" || labels["dataset_id"] == "" || labels["freq"] == "" {
					continue
				}
				if spaceID != "" && labels["space_id"] != spaceID {
					continue
				}
				identity := strings.Join([]string{producer, labels["space_id"], labels["dataset_id"], labels["freq"]}, "\x00")
				current, exists := best[identity]
				if !exists || series.LastSeenAt.After(current.seenAt) || (series.LastSeenAt.Equal(current.seenAt) && name < current.name) {
					best[identity] = candidate{name: name, seenAt: series.LastSeenAt}
				}
			}
			offset += len(rows)
			if len(rows) == 0 || int64(offset) >= total {
				break
			}
		}
	}
	out := make(map[string]string, len(best))
	for identity, item := range best {
		out[identity] = item.name
	}
	return out
}

// viewArrival 读取与 View 水位同一身份的到达时间（View 水位最近一次前进时提交完成的时刻）；没有上报时为 0。
func (b Builder) viewArrival(ctx context.Context, watermark monmetrics.MetricSeries) (float64, error) {
	const viewArrivalMetric = "moox_storage_view_output_arrival_timestamp_seconds"
	series, err := b.Metrics.Catalog().FindSeries(ctx, "", watermark.ServiceName, viewArrivalMetric, watermark.LabelsJSON, 10)
	if err != nil {
		return 0, err
	}
	for _, item := range series {
		if item.InstanceID != watermark.InstanceID {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return 0, err
		}
		return latest.Value, nil
	}
	return 0, nil
}

func minPositive(left, right float64) float64 {
	switch {
	case left <= 0:
		return right
	case right <= 0:
		return left
	case left < right:
		return left
	default:
		return right
	}
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

// confirmedUntil 把又一个实例并入已合并的数据集时，给出能确认最新一根“到此刻还没更新”的时刻。数据集的最新一根取各实例的最大值，
// 某一根要所有实例都上报过、都还没有它才算确认未到，所以取仍在上报的实例里最早的那次上报；已停止上报的实例不拖住确认
// （全部停止时由 producer stale 判断）。merged.reporterStale 表示已合并的实例是否全部停止上报。
func confirmedUntil(merged, next datasetValues) time.Time {
	switch {
	case next.reportedAt.IsZero() || next.reporterStale && !merged.reporterStale:
		return merged.confirmedAt
	case merged.confirmedAt.IsZero() || merged.reporterStale && !next.reporterStale:
		return next.reportedAt
	case next.reportedAt.Before(merged.confirmedAt):
		return next.reportedAt
	default:
		return merged.confirmedAt
	}
}

func (b Builder) populateDatasetValues(
	ctx context.Context,
	values map[datasetKey]datasetValues,
	enabledSeries []monmetrics.MetricSeries,
) error {
	for key, current := range values {
		prefix := strings.TrimSuffix(metricPrefixForDataset(key, enabledSeries), "enabled")
		for suffix, target := range map[string]*float64{
			"expected_interval_seconds":          &current.interval,
			"last_run_timestamp_seconds":         &current.lastRun,
			"last_success_timestamp_seconds":     &current.lastSuccess,
			"last_partial_timestamp_seconds":     &current.lastPartial,
			"input_watermark_timestamp_seconds":  &current.input,
			"output_watermark_timestamp_seconds": &current.output,
			"output_arrival_timestamp_seconds":   &current.arrival,
		} {
			series, err := b.Metrics.Catalog().FindSeries(ctx, "", key.service, prefix+suffix, key.labels, 500)
			if err != nil {
				return err
			}
			for _, item := range series {
				if item.InstanceID != key.instance {
					continue
				}
				latest, err := b.Metrics.Latest(ctx, item.SeriesID)
				if err != nil {
					return err
				}
				*target = latest.Value
				current.reportedAt = maxTime(current.reportedAt, latest.ObservedAt.UTC())
				break
			}
		}
		if !ownsExpectedDatasetInventory(key.producer) {
			values[key] = current
			continue
		}
		inventorySeries, _, err := b.Metrics.Catalog().ListSeries(ctx, key.service, prefix+"inventory_last_success_timestamp_seconds", "", 0, 100)
		if err != nil {
			return err
		}
		for _, item := range inventorySeries {
			if item.InstanceID != key.instance {
				continue
			}
			latest, err := b.Metrics.Latest(ctx, item.SeriesID)
			if err != nil {
				return err
			}
			current.inventory = latest.Value
			current.reportedAt = maxTime(current.reportedAt, latest.ObservedAt.UTC())
			break
		}
		values[key] = current
	}
	return nil
}

func datasetScopeKey(spaceID, datasetID, freq string) string {
	return strings.Join([]string{spaceID, datasetID, freq}, "\x00")
}

func metricPrefixForDataset(key datasetKey, series []monmetrics.MetricSeries) string {
	for _, item := range series {
		if item.ServiceName == key.service && item.InstanceID == key.instance && item.LabelsJSON == key.labels {
			return strings.TrimSuffix(item.MetricName, "_dataset_enabled") + "_dataset_enabled"
		}
	}
	return "moox_" + key.producer + "_dataset_"
}

func datasetModuleFromMetric(metricName string) string {
	if !strings.HasPrefix(metricName, "moox_") {
		return ""
	}
	rest := strings.TrimPrefix(metricName, "moox_")
	module, _, ok := strings.Cut(rest, "_dataset_")
	if !ok {
		return ""
	}
	return module
}

func metricSampleFromBoot(messageID, bootID string) bool {
	messageID = strings.TrimSpace(messageID)
	bootID = strings.TrimSpace(bootID)
	if messageID == "" || bootID == "" {
		return true
	}
	return strings.HasPrefix(messageID, bootID+"-") || messageID == bootID
}

func datasetLabels(raw string) (map[string]string, error) {
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

func ensureDatasetLimit(count int) error {
	if count > MaxDatasetFrequencyStatuses {
		return datasetLimitError()
	}
	return nil
}

func datasetLimitError() error {
	return fmt.Errorf("dataset frequency statuses exceed limit %d", MaxDatasetFrequencyStatuses)
}

// datasetStatus 判定一个数据集的状态。新鲜度看“最新一根 K 线是否按时到达”：按市场交易日历算出此刻应已收线的最新一根（预期 K 线），
// 还没到、并且生产者在收线超过允许迟到之后的上报里仍没有它，才判为过期；已经到了的根即使迟到，也只记下延迟，当前状态仍是正常。
func datasetStatus(now time.Time, key datasetKey, value datasetValues, policy report.RealtimeTimeSeriesPolicy, clock spaceClock) DatasetFrequencyStatus {
	item := DatasetFrequencyStatus{
		Producer: key.producer, SpaceID: key.spaceID, DatasetID: key.datasetID, Freq: key.freq,
		PrimaryDatasetID: key.primaryDatasetID,
		LastRunAt:        unixTime(value.lastRun), LastSuccessAt: unixTime(value.lastSuccess), LastPartialAt: unixTime(value.lastPartial),
		InputWatermarkAt: unixTime(value.input), OutputWatermarkAt: unixTime(value.output),
		LastReportedAt: value.reportedAt,
	}
	runLag, successLag, lateness := datasetTolerances(key, value.interval, policy)
	item.LatenessLimitSeconds = int64(lateness / time.Second)
	bars := evaluateBars(clock, key.freq, item.OutputWatermarkAt, unixTime(value.arrival), value.confirmedAt, now)
	item.LatestBarClose, item.OutputArrivedAt = bars.latestClose, unixTime(value.arrival)
	item.DelaySeconds, item.DelayKnown = int64(bars.delay/time.Second), bars.delayKnown
	item.ExpectedBarAt, item.ExpectedBarClose = bars.expected.Open, bars.expected.Close
	item.WaitingBars, item.WaitingSince, item.OverdueSeconds = bars.waiting, bars.waitingSince, int64(bars.overdue/time.Second)
	item.LateConfirmed = bars.waiting > 0 && lateness > 0 && bars.confirmed && bars.confirmedOverdue > lateness
	item.ClockIssue = clockIssueText(bars.issue)
	item.Timezone = time.UTC.String()
	if clock.err == nil {
		item.Timezone = clock.clock.Location().String()
	}
	inventoryAt := unixTime(value.inventory)
	switch {
	case value.reporterStale:
		item.Status, item.Reason = "stale", "producer stale"
	case ownsExpectedDatasetInventory(key.producer) && (inventoryAt.IsZero() || now.Sub(inventoryAt) > 10*time.Minute):
		item.Status, item.Reason = "unknown", "inventory_stale"
	case item.LastRunAt.IsZero():
		item.Status, item.Reason = "unknown", "尚未上报"
	case runLag > 0 && now.Sub(item.LastRunAt) > runLag:
		item.Status, item.Reason = "stale", "run stale"
	case warmingNow(now, item, successLag):
		// 生产者仍在按设计只产出部分结果（空洞保护、历史不足、预热）：页面显示降级，但不是故障，不告警。
		item.Status, item.Reason = "degraded", DatasetReasonWarming
	case item.LastSuccessAt.IsZero():
		item.Status, item.Reason = "degraded", "尚无成功运行"
	case successLag > 0 && now.Sub(item.LastSuccessAt) > successLag:
		item.Status, item.Reason = "degraded", "success stale"
	case errors.Is(bars.issue, monmetrics.ErrCalendarNotCovered):
		// 日历覆盖不到就无法判断应收哪一根：不推测，报出来让人更新交易日历。
		item.Status, item.Reason = "unknown", item.ClockIssue
	case item.LateConfirmed:
		item.Status = "stale"
		item.Reason = waitingReason(item)
	case item.OutputWatermarkAt.IsZero():
		item.Status, item.Reason = "healthy", "正常但空结果"
	default:
		item.Status, item.Reason = "healthy", "normal"
	}
	return item
}

// DatasetReasonWarming 是“数据积累中”的原因码：生产者报告最近一次运行只按设计产出了部分结果（窗口空洞保护、
// 历史不足、预热中），而且此后没有更新的成功。状态仍是 degraded，页面照常展示；告警检查把它当作通过。
const DatasetReasonWarming = "warming"

// warmingNow 判断数据集是否处在“积累中”：最近一次产出是部分结果（比最近一次成功更新），并且部分结果仍按期出现——
// 超过成功容忍时间没有新的部分结果（生产者停了或开始真正失败）就不再算积累，回到原来的成功过期判断。
func warmingNow(now time.Time, item DatasetFrequencyStatus, successLag time.Duration) bool {
	if item.LastPartialAt.IsZero() || !item.LastPartialAt.After(item.LastSuccessAt) {
		return false
	}
	return successLag <= 0 || now.Sub(item.LastPartialAt) <= successLag
}

// datasetWatermarkStaleReason keeps the alert useful without requiring an
// operator to translate the internal "watermark" term. Timestamps are
// explicitly UTC so the value can be compared with Collector/Storage logs.
func ownsExpectedDatasetInventory(producer string) bool {
	return producer == "collector" || producer == "factor"
}

func datasetTolerances(key datasetKey, interval float64, policy report.RealtimeTimeSeriesPolicy) (time.Duration, time.Duration, time.Duration) {
	runBase := time.Duration(interval * float64(time.Second))
	defaults := policy.Defaults
	var runLag, successLag time.Duration
	if defaults.RunMissedIntervals > 0 && runBase > 0 {
		runLag = time.Duration(defaults.RunMissedIntervals)*runBase + 30*time.Second
	}
	if defaults.SuccessMissedIntervals > 0 && runBase > 0 {
		successLag = time.Duration(defaults.SuccessMissedIntervals)*runBase + 30*time.Second
	}
	// 允许迟到按周期分级（分钟级、小时级、日线及以上），由健康策略统一给出；应收的最新一根已按交易日历确定，周末、长假不需要放大。
	return runLag, successLag, policy.Lateness(key.spaceID, key.datasetID, key.freq)
}

func sameDatasetFrequency(left, right string) bool {
	left, leftErr := frequencypkg.Normalize(left)
	right, rightErr := frequencypkg.Normalize(right)
	return leftErr == nil && rightErr == nil && left == right
}

func parseOverviewFrequency(raw string) time.Duration {
	parsed, err := frequencypkg.Parse(raw)
	if err != nil {
		return 0
	}
	return parsed.NominalDuration()
}

func (b Builder) buildBusinessChecks(ctx context.Context, spaceID string) ([]BusinessStatus, error) {
	out := make([]BusinessStatus, 0)
	if b.Checks != nil && b.Results != nil {
		checks, err := b.Checks.ListEnabledKinds(ctx, spaceID, []string{"market_canary", "subject_tag", "balance", "module", "kline_freshness", "console-page"}, 1501)
		if err != nil {
			return nil, err
		}
		if len(checks) > 1500 {
			return nil, fmt.Errorf("business checks exceed 1500 items")
		}
		for _, check := range checks {
			kind := businessKind(check)
			if kind == "" {
				continue
			}
			item := BusinessStatus{CheckID: check.CheckID, SpaceID: check.SpaceID, Name: check.Name, Kind: kind, Module: businessModule(check.CheckID), Status: "unknown", Reason: "尚未上报"}
			results, err := b.Results.Recent(ctx, check.SpaceID, check.CheckID, 1)
			if err != nil {
				return nil, err
			}
			if len(results) > 0 {
				item.LastCheckedAt = results[0].CheckedAt.UTC()
				item.Status = strings.ToLower(string(results[0].Status))
				item.Reason = results[0].ErrorMessage
				item.RawError = results[0].RawError
				if item.Reason == "" {
					item.Reason = map[bool]string{true: "normal", false: "check failed"}[results[0].Success]
				}
				if item.Reason == domain.CheckReasonRetired {
					item.Status = "disabled"
				}
			}
			out = append(out, item)
		}
	}
	balances, err := b.buildBalanceStatuses(ctx)
	if err != nil {
		return nil, err
	}
	out = append(out, balances...)
	return out, nil
}

func (b Builder) buildBalanceStatuses(ctx context.Context) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	lastSuccess, err := b.balanceMetricValues(ctx, "moox_trade_balance_sync_last_success_timestamp_seconds")
	if err != nil {
		return nil, err
	}
	lastRun, err := b.balanceMetricValues(ctx, "moox_trade_balance_sync_last_run_timestamp_seconds")
	if err != nil {
		return nil, err
	}
	failures, err := b.balanceMetricValues(ctx, "moox_trade_balance_sync_consecutive_failures")
	if err != nil {
		return nil, err
	}
	differences, err := b.balanceMetricValues(ctx, "moox_trade_balance_sync_max_difference_ratio")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	threshold := b.BalanceDifferenceThreshold
	if threshold <= 0 {
		threshold = 0.05
	}
	out := make([]BusinessStatus, 0, len(lastSuccess))
	for key, successValue := range lastSuccess {
		runValue := lastRun[key]
		failureValue := failures[key]
		differenceValue := differences[key]
		if successValue.value == 0 && runValue.value == 0 && failureValue.value == 0 && differenceValue.value == 0 {
			// Prometheus exports registered gauges as zero even when Trade has no
			// configured accounts. No sync has been expected until activity begins.
			continue
		}
		lastSuccessAt := unixTime(successValue.value)
		lastCheckedAt := lastSuccessAt
		if _, ok := lastRun[key]; ok {
			lastCheckedAt = unixTime(runValue.value)
		}
		status, reason := "healthy", "balance sync fresh"
		switch {
		case successValue.stale:
			status, reason = "stale", "producer stale"
		case failureValue.value >= 3:
			status, reason = "down", "balance sync failed 3 consecutive runs"
		case differenceValue.value > threshold:
			status, reason = "down", fmt.Sprintf("balance difference %.4f exceeds %.4f", differenceValue.value, threshold)
		case lastSuccessAt.IsZero():
			status, reason = "unknown", "尚未上报"
		case now.Sub(lastSuccessAt) > 15*time.Minute:
			status, reason = "down", "balance sync stale"
		}
		out = append(out, BusinessStatus{
			SpaceID: "crypto", Kind: "balance", Module: successValue.serviceName, Status: status,
			Reason: reason, LastCheckedAt: lastCheckedAt,
		})
	}
	return out, nil
}

type timerCoordinationState struct {
	lastSuccess     float64
	pending         float64
	pendingSince    float64
	hasLastSuccess  bool
	hasPending      bool
	hasPendingSince bool
	healthy         float64
	hasHealth       bool
	staleSeries     bool
	failureReasons  map[string]bool
	// failureSince 是当前失败原因首次出现的时间（多个上报方时取最早的非零值）。
	failureSince    float64
	hasFailureSince bool
}

type marketFeedHealthState struct {
	requests, failures, rateLimited, noCandidate float64
	lastObserved                                 time.Time
}

type marketInstrumentHealthState struct {
	provider, route, result string
	active                  float64
	lastObserved, fetchedAt time.Time
	exchanges               map[string]float64
}

const maxMarketSignalSeries = 5000

// buildMarketFetchSignalHealth consumes the bounded facts emitted by short-lived
// Collector invocations. It intentionally does not
// infer health from missing rows unless a stock signal family has already been
// observed; an uninstalled market must not page forever.
func (b Builder) buildMarketFetchSignalHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	if spaceID != "" && spaceID != "stockcn" && spaceID != monmetrics.InternalMetricSpaceID {
		return nil, nil
	}
	feed, err := b.buildStockCNFeedHealth(ctx, now)
	if err != nil {
		return nil, err
	}
	instrument, err := b.buildStockCNInstrumentHealth(ctx, now)
	if err != nil {
		return nil, err
	}
	return append(feed, instrument...), nil
}

func (b Builder) marketMetricSeries(ctx context.Context, metricName string) ([]monmetrics.MetricSeries, error) {
	const pageSize = 500
	all := make([]monmetrics.MetricSeries, 0, pageSize)
	for offset := 0; ; offset += pageSize {
		rows, total, err := b.Metrics.Catalog().ListSeries(ctx, "", metricName, "", offset, pageSize)
		if err != nil {
			return nil, err
		}
		if total > maxMarketSignalSeries {
			return nil, fmt.Errorf("market metric series exceed limit %d for %s", maxMarketSignalSeries, metricName)
		}
		all = append(all, rows...)
		if len(all) >= int(total) || len(rows) == 0 {
			return all, nil
		}
	}
}

func isFreshMetric(now, observed time.Time, window time.Duration) bool {
	if observed.IsZero() || observed.After(now) {
		return false
	}
	return now.Sub(observed) <= window
}

func (b Builder) buildStockCNFeedHealth(ctx context.Context, now time.Time) ([]BusinessStatus, error) {
	window := b.marketFetchThresholds().FeedFailureRateWindow
	if window <= 0 {
		window = 5 * time.Minute
	}
	series, err := b.marketMetricSeries(ctx, "moox_collector_market_feed_results_total")
	if err != nil {
		return nil, err
	}
	states := map[string]*marketFeedHealthState{}
	sawStock := false
	for _, item := range series {
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return nil, fmt.Errorf("market feed labels for %s: %w", item.SeriesID, err)
		}
		if strings.TrimSpace(labels["market_id"]) != "stockcn" || strings.TrimSpace(labels["feed_kind"]) != "kline" {
			continue
		}
		sawStock = true
		provider := strings.TrimSpace(labels["provider_id"])
		if provider == "" {
			provider = "unknown"
		}
		state := states[provider]
		if state == nil {
			state = &marketFeedHealthState{}
			states[provider] = state
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		if latest == nil || item.IsStale || !isFreshMetric(now, latest.ObservedAt.UTC(), window) {
			continue
		}
		value := maxFloat(latest.Value, 0)
		state.requests += value
		state.lastObserved = maxTime(state.lastObserved, latest.ObservedAt.UTC())
		switch strings.TrimSpace(labels["result"]) {
		case "http_429", "rate_limited":
			state.failures += value
			state.rateLimited += value
		case "http_5xx", "timeout", "invalid", "storage_error", "no_candidate":
			state.failures += value
			if strings.TrimSpace(labels["result"]) == "no_candidate" {
				state.noCandidate += value
			}
		}
	}
	if !sawStock {
		return nil, nil
	}
	threshold := b.marketFetchThresholds().FeedFailureRateThreshold
	if threshold <= 0 {
		threshold = 0.2
	}
	providers := make([]string, 0, len(states))
	for provider := range states {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	out := make([]BusinessStatus, 0, len(providers))
	for _, provider := range providers {
		state := states[provider]
		status, reason := "healthy", fmt.Sprintf("%s Provider Feed 正常", provider)
		if state.requests <= 0 {
			status, reason = "down", fmt.Sprintf("%s Provider Feed 在最近 %s 无有效结果", provider, window)
		} else if state.noCandidate > 0 {
			status, reason = "down", fmt.Sprintf("%s Provider Feed 候选池为空", provider)
		} else if state.rateLimited > 0 {
			status, reason = "down", fmt.Sprintf("%s Provider Feed 最近 %s 持续触发 429/限频", provider, window)
		} else if rate := state.failures / state.requests; rate > threshold {
			status, reason = "down", fmt.Sprintf("%s Provider Feed 最近 %s 失败率 %.1f%%，超过 %.1f%%", provider, window, rate*100, threshold*100)
		}
		out = append(out, BusinessStatus{SpaceID: "stockcn", Kind: "market_fetch", Module: "provider_feed:" + provider, Status: status, Reason: reason, LastCheckedAt: now})
	}
	if len(out) == 0 {
		out = append(out, BusinessStatus{SpaceID: "stockcn", Kind: "market_fetch", Module: "provider_feed", Status: "down", Reason: fmt.Sprintf("stockcn Provider Feed 最近 %s 没有新指标", window), LastCheckedAt: now})
	}
	return out, nil
}

func (b Builder) buildStockCNInstrumentHealth(ctx context.Context, now time.Time) ([]BusinessStatus, error) {
	thresholds := b.marketFetchThresholds()
	maxAge := thresholds.InstrumentSnapshotMaxAge
	if maxAge <= 0 {
		maxAge = 36 * time.Hour
	}
	series, err := b.marketMetricSeries(ctx, "moox_collector_market_instrument_last_snapshot_timestamp_seconds")
	if err != nil {
		return nil, err
	}
	states := map[string]*marketInstrumentHealthState{}
	for _, item := range series {
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return nil, fmt.Errorf("instrument snapshot labels for %s: %w", item.SeriesID, err)
		}
		if labels["market_id"] != "stockcn" || item.IsStale {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		if latest == nil {
			continue
		}
		provider := nonEmptyString(labels["provider_id"], "unknown")
		key := strings.Join([]string{labels["route_id"], provider}, "\x00")
		state := states[key]
		if state == nil || latest.ObservedAt.After(state.lastObserved) {
			active := float64(0)
			exchanges := map[string]float64{}
			if state != nil {
				active = state.active
				exchanges = state.exchanges
			}
			states[key] = &marketInstrumentHealthState{provider: provider, route: labels["route_id"], result: labels["result"], active: active, exchanges: exchanges, fetchedAt: unixTime(latest.Value), lastObserved: latest.ObservedAt.UTC()}
		}
	}
	loadInstrumentFacts := func(metricName string, apply func(*marketInstrumentHealthState, map[string]string, float64)) error {
		facts, err := b.marketMetricSeries(ctx, metricName)
		if err != nil {
			return err
		}
		for _, item := range facts {
			labels, err := datasetLabels(item.LabelsJSON)
			if err != nil {
				return fmt.Errorf("instrument metric labels for %s: %w", item.SeriesID, err)
			}
			if labels["market_id"] != "stockcn" || item.IsStale {
				continue
			}
			latest, err := b.Metrics.Latest(ctx, item.SeriesID)
			if err != nil {
				return err
			}
			if latest == nil {
				continue
			}
			provider := nonEmptyString(labels["provider_id"], "unknown")
			key := strings.Join([]string{labels["route_id"], provider}, "\x00")
			state := states[key]
			if state == nil {
				state = &marketInstrumentHealthState{provider: provider, route: labels["route_id"], exchanges: map[string]float64{}}
				states[key] = state
			}
			if !latest.ObservedAt.Before(state.lastObserved) || state.lastObserved.IsZero() {
				apply(state, labels, latest.Value)
				state.lastObserved = latest.ObservedAt.UTC()
			}
		}
		return nil
	}
	if err := loadInstrumentFacts("moox_collector_market_instrument_active", func(state *marketInstrumentHealthState, _ map[string]string, value float64) {
		state.active = maxFloat(value, 0)
	}); err != nil {
		return nil, err
	}
	if err := loadInstrumentFacts("moox_collector_market_instrument_exchange", func(state *marketInstrumentHealthState, labels map[string]string, value float64) {
		if state.exchanges == nil {
			state.exchanges = map[string]float64{}
		}
		state.exchanges[labels["exchange"]] = maxFloat(value, 0)
	}); err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, nil
	}
	out := make([]BusinessStatus, 0, len(states))
	for _, state := range states {
		status, reason := "healthy", fmt.Sprintf("%s Instrument 快照正常", state.provider)
		if state.result != "success" {
			status, reason = "down", fmt.Sprintf("%s Instrument 快照结果为 %s", state.provider, nonEmptyString(state.result, "unknown"))
		} else if state.fetchedAt.IsZero() || now.Sub(state.fetchedAt) > maxAge {
			status, reason = "down", fmt.Sprintf("%s Instrument 快照已超过 %s 未更新", state.provider, maxAge)
		} else if state.active < float64(max(1, thresholds.InstrumentMinimumCount)) {
			status, reason = "down", fmt.Sprintf("%s Instrument active 数 %.0f 低于下限 %d", state.provider, state.active, thresholds.InstrumentMinimumCount)
		} else {
			for _, exchange := range thresholds.InstrumentRequiredExchanges {
				if state.exchanges[strings.TrimSpace(exchange)] <= 0 {
					status, reason = "down", fmt.Sprintf("%s Instrument 快照缺少交易所 %s", state.provider, strings.TrimSpace(exchange))
					break
				}
			}
		}
		out = append(out, BusinessStatus{SpaceID: "stockcn", Kind: "market_fetch", Module: "instrument_snapshot:" + state.provider, Status: status, Reason: reason, LastCheckedAt: now})
	}
	return out, nil
}

func nonEmptyString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

// A full Tencent runtime-config batch may touch dozens of functions. The
// coordination timestamp measures completion of that asynchronous batch, not
// the ten-second Collector timer callback. Keep this threshold above the
// observed multi-node provider update window; Storage freshness remains the
// fast alert for an actual missing K-line.
const timerCoordinationStaleAfter = 15 * time.Minute

const timerCoordinationPendingGrace = 5 * time.Minute

// timerCoordinationLeaseGrace 是「发布租约被占用」的宽限期。重启后旧作业的操作占用要等它结束或过期（每次续期到当前时间加
// 6 分钟），实测通常 6 分钟左右；10 分钟之后仍被占用，说明作业卡住或有其他发布方，值得人工介入。
const timerCoordinationLeaseGrace = 10 * time.Minute

// publishLeaseHeldReason 是 Collector 上报的失败原因：Admin 明确答复发布租约或在途操作占用仍被持有。
const publishLeaseHeldReason = "publish_lease_held"

func (b Builder) marketFetchThresholds() MarketFetchThresholds {
	thresholds := b.MarketFetchThresholds
	if thresholds.CoordinationStaleAfter <= 0 {
		thresholds.CoordinationStaleAfter = timerCoordinationStaleAfter
	}
	if thresholds.PendingGrace <= 0 {
		thresholds.PendingGrace = timerCoordinationPendingGrace
	}
	if thresholds.LeaseGrace <= 0 {
		thresholds.LeaseGrace = timerCoordinationLeaseGrace
	}
	if thresholds.FeedFailureRateWindow <= 0 {
		thresholds.FeedFailureRateWindow = 5 * time.Minute
	}
	if thresholds.FeedFailureRateThreshold <= 0 {
		thresholds.FeedFailureRateThreshold = 0.2
	}
	if thresholds.InstrumentSnapshotMaxAge <= 0 {
		thresholds.InstrumentSnapshotMaxAge = 36 * time.Hour
	}
	if thresholds.InstrumentMinimumCount <= 0 {
		thresholds.InstrumentMinimumCount = 4000
	}
	if len(thresholds.InstrumentRequiredExchanges) == 0 {
		thresholds.InstrumentRequiredExchanges = []string{"XSHG", "XSHE", "XBSE"}
	}
	return thresholds
}

// buildMarketFetchCoordination consumes the small Collector coordination
// metric set. It deliberately does not call Tencent or CloudNode: Collector
// already reads the trigger readback and publishes it through the shared
// reporter, while Monitor remains a pure consumer.
func (b Builder) buildMarketFetchCoordination(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	states := map[string]*timerCoordinationState{}
	thresholds := b.marketFetchThresholds()
	collectorReporterFresh := false
	if services, _, err := b.Metrics.Catalog().ListServices(ctx, "", 0, 500); err != nil {
		return nil, err
	} else {
		for _, service := range services {
			if service.ServiceName == "collector" && !service.IsStale {
				collectorReporterFresh = true
				break
			}
		}
	}
	load := func(metricName string, apply func(*timerCoordinationState, map[string]string, float64)) error {
		series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", metricName, "", 500, now)
		if err != nil {
			return err
		}
		for _, item := range series {
			labels, err := datasetLabels(item.LabelsJSON)
			if err != nil {
				return fmt.Errorf("timer coordination labels for %s: %w", item.SeriesID, err)
			}
			currentSpace := strings.TrimSpace(labels["space_id"])
			if currentSpace == "" || (spaceID != "" && currentSpace != spaceID) {
				continue
			}
			state := states[currentSpace]
			if state == nil {
				state = &timerCoordinationState{}
				states[currentSpace] = state
			}
			if item.IsStale {
				// Collector removes Gauge children when a rule/node disappears.
				// The metrics catalog retains old series for history, so stale
				// children must not keep an obsolete coordination alert alive.
				state.staleSeries = true
				continue
			}
			latest, err := b.Metrics.Latest(ctx, item.SeriesID)
			if err != nil {
				return err
			}
			apply(state, labels, latest.Value)
		}
		return nil
	}
	if err := load("moox_collector_market_fetch_assignment_last_success_timestamp_seconds", func(state *timerCoordinationState, _ map[string]string, value float64) {
		if value > state.lastSuccess {
			state.lastSuccess = value
		}
		state.hasLastSuccess = true
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_coordination_healthy", func(state *timerCoordinationState, _ map[string]string, value float64) {
		// A space may be reported by more than one Collector during a rolling
		// restart.  Aggregate conservatively: one failed reporter must not be
		// hidden by whichever series happens to sort last.
		if !state.hasHealth || value <= 0 {
			state.healthy = value
		}
		state.hasHealth = true
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_coordination_failure", func(state *timerCoordinationState, labels map[string]string, value float64) {
		if value <= 0 {
			return
		}
		if state.failureReasons == nil {
			state.failureReasons = make(map[string]bool)
		}
		if reason := strings.TrimSpace(labels["reason"]); reason != "" {
			state.failureReasons[reason] = true
		}
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_coordination_failure_since_timestamp_seconds", func(state *timerCoordinationState, _ map[string]string, value float64) {
		// 多个 Collector 上报同一空间时取最早的非零起始时间：最保守，不会因为另一个实例刚开始失败而延长宽限。
		if value > 0 && (!state.hasFailureSince || value < state.failureSince) {
			state.failureSince, state.hasFailureSince = value, true
		}
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_coordination_pending", func(state *timerCoordinationState, _ map[string]string, value float64) {
		if value > state.pending {
			state.pending = value
		}
		state.hasPending = true
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_coordination_pending_since_timestamp_seconds", func(state *timerCoordinationState, _ map[string]string, value float64) {
		if value > state.pendingSince {
			state.pendingSince = value
		}
		state.hasPendingSince = true
	}); err != nil {
		return nil, err
	}
	out := make([]BusinessStatus, 0, len(states))
	for currentSpace, state := range states {
		pendingGrace := state.hasPending && state.pending > 0 && state.hasPendingSince && state.pendingSince > 0 &&
			now.Sub(unixTime(state.pendingSince)) >= 0 && now.Sub(unixTime(state.pendingSince)) <= thresholds.PendingGrace
		// 发布租约被占用有自己的宽限期：只有在「被占用」是唯一失败原因、且起始时间已知并在宽限期内时才给宽限。
		// 多个 Collector 实例上报时，另一个实例的真实故障不能被租约宽限掩盖；缺少起始时间无法证明在宽限期内。
		leaseHeld := state.hasHealth && state.healthy <= 0 && state.failureReasons[publishLeaseHeldReason]
		leaseWithinGrace := leaseHeld && len(state.failureReasons) == 1 && state.hasFailureSince && state.failureSince > 0 &&
			now.Sub(unixTime(state.failureSince)) <= thresholds.LeaseGrace
		// 普通的 pending 宽限不能盖过已超期（或无法证明在宽限期内）的租约失败。
		if pendingGrace && !(leaseHeld && !leaseWithinGrace) {
			out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "healthy", Reason: "采集计划协调进行中", LastCheckedAt: now})
			continue
		}
		if state.hasHealth && state.healthy <= 0 {
			// 发布租约被占用是租约在正常工作（重启前的旧作业还在云端运行）：宽限期内按协调进行中处理。
			// 缺少失败起始时间时无法证明还在宽限期内，按已超期处理。
			if leaseWithinGrace {
				out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "healthy", Reason: "发布租约被占用：等待在途云端作业结束，通常数分钟内自动恢复", LastCheckedAt: now})
				continue
			}
			reason := coordinationFailureText(state.failureReasons)
			if leaseHeld && len(state.failureReasons) == 1 {
				// 租约冲突是唯一原因才用这条文案；与其他原因同时出现时，以真实故障的文案为准。
				reason = "发布租约长时间被占用：请检查 CloudNode 批次作业是否卡住，以及是否有其他发布方（CLI 手工发布、补采）在运行"
			}
			out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "down", Reason: reason, LastCheckedAt: now})
			continue
		}
		if !state.hasLastSuccess || state.lastSuccess <= 0 {
			if state.staleSeries && !collectorReporterFresh {
				out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "down", Reason: "Collector 采集计划指标已停止上报", LastCheckedAt: now})
			}
			continue
		}
		status, reason := "healthy", "采集计划对账正常"
		if now.Sub(unixTime(state.lastSuccess)) > thresholds.CoordinationStaleAfter {
			status, reason = "down", fmt.Sprintf("最近一次采集计划对账已超过允许时间，最后成功时间 %s", unixTime(state.lastSuccess).Format(time.RFC3339))
		}
		out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: status, Reason: reason, LastCheckedAt: now})
	}
	return out, nil
}

func maxFloat(value, floor float64) float64 {
	if value < floor {
		return floor
	}
	return value
}

type balanceMetricValue struct {
	serviceName string
	value       float64
	stale       bool
}

func (b Builder) balanceMetricValues(ctx context.Context, metricName string) (map[string]balanceMetricValue, error) {
	series, total, err := b.Metrics.Catalog().ListSeries(ctx, "", metricName, "", 0, 101)
	if err != nil {
		return nil, err
	}
	if total > 100 {
		return nil, fmt.Errorf("trade balance series exceed limit 100 for %s", metricName)
	}
	out := make(map[string]balanceMetricValue, len(series))
	for _, item := range series {
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		out[item.ServiceName+"\x00"+item.InstanceID] = balanceMetricValue{
			serviceName: item.ServiceName, value: latest.Value, stale: item.IsStale,
		}
	}
	return out, nil
}

func businessKind(check domain.Check) string {
	kind, _, ok := strings.Cut(check.CheckID, ":")
	if !ok {
		return ""
	}
	switch kind {
	case "market_canary", "subject_tag", "balance", "module", "kline_freshness", "console-page":
		return kind
	default:
		return ""
	}
}

func businessModule(checkID string) string {
	kind, id, _ := strings.Cut(checkID, ":")
	switch kind {
	case "console-page":
		return "console-proxy"
	case "market_canary", "subject_tag", "kline_freshness":
		return "collector"
	case "balance":
		return "trade"
	case "module":
		for _, check := range report.BuiltInModuleHealthChecks() {
			if check.ID == id {
				return check.Module
			}
		}
	}
	return ""
}

// Live metric facts supersede the cached external check for the same identity.
func uniqueBusinessChecks(items []BusinessStatus) []BusinessStatus {
	indexes := make(map[string]int, len(items))
	out := make([]BusinessStatus, 0, len(items))
	for _, item := range items {
		if item.CheckID == "" {
			item.CheckID = item.Kind + ":" + item.Module
		}
		key := item.SpaceID + "\x00" + item.CheckID
		if i, exists := indexes[key]; exists {
			out[i] = item
			continue
		}
		indexes[key] = len(out)
		out = append(out, item)
	}
	return out
}

func sortOverview(out *Overview) {
	sort.Slice(out.Services, func(i, j int) bool {
		if statusRank(out.Services[i].Status) != statusRank(out.Services[j].Status) {
			return statusRank(out.Services[i].Status) < statusRank(out.Services[j].Status)
		}
		return strings.Join([]string{out.Services[i].ServiceName, out.Services[i].NodeID, out.Services[i].InstanceID}, "\x00") < strings.Join([]string{out.Services[j].ServiceName, out.Services[j].NodeID, out.Services[j].InstanceID}, "\x00")
	})
	sort.Slice(out.Hosts, func(i, j int) bool {
		if statusRank(out.Hosts[i].Status) != statusRank(out.Hosts[j].Status) {
			return statusRank(out.Hosts[i].Status) < statusRank(out.Hosts[j].Status)
		}
		return out.Hosts[i].Hostname < out.Hosts[j].Hostname
	})
	sort.Slice(out.Datasets, func(i, j int) bool {
		if statusRank(out.Datasets[i].Status) != statusRank(out.Datasets[j].Status) {
			return statusRank(out.Datasets[i].Status) < statusRank(out.Datasets[j].Status)
		}
		left := out.Datasets[i].Producer + "\x00" + out.Datasets[i].DatasetID + "\x00" + out.Datasets[i].Freq
		right := out.Datasets[j].Producer + "\x00" + out.Datasets[j].DatasetID + "\x00" + out.Datasets[j].Freq
		return left < right
	})
	sort.Slice(out.BusinessChecks, func(i, j int) bool {
		if statusRank(out.BusinessChecks[i].Status) != statusRank(out.BusinessChecks[j].Status) {
			return statusRank(out.BusinessChecks[i].Status) < statusRank(out.BusinessChecks[j].Status)
		}
		return out.BusinessChecks[i].Kind < out.BusinessChecks[j].Kind
	})
}

func statusRank(status string) int {
	switch strings.ToLower(status) {
	case "down", "error", "firing":
		return 0
	case "stale", "degraded", "warn":
		return 1
	case "unknown", "unspecified":
		return 2
	default:
		return 3
	}
}

// coordinationFailureTexts 是 Collector 上报的协调失败原因对应的告警文案，按优先级排列：
// 多个实例上报不同原因时取排在前面的。原因名与 Collector 的 assignmentFailureReasons 一一对应。
var coordinationFailureTexts = []struct{ reason, text string }{
	{"cloudnode", "采集计划配置服务调用失败：请检查 CloudNode、网关连接和批处理队列"},
	{"environment", "函数运行环境生成失败：请检查环境变量大小和 DNS 配置"},
	{"rules", "采集规则读取失败：请检查 Collector 规则配置和存储连接"},
	{"store", "Collector 本地数据库读写失败：请检查 Collector 的 SQLite 数据库、磁盘空间和权限"},
	{"storage", "标的快照登记失败：请检查 Storage 连接，以及 Collector 与 Storage 计算的快照哈希是否一致"},
	{"package", "采集函数代码包解析失败：请检查代码包是否已上传，以及 CloudNode 是否可达"},
	{"dataset", "数据集属性（允许迟到）设置失败：请检查 Storage 连接和数据集配置"},
	{"activity", "采集计划活动日志未写入，计划保持未生效：请检查 Admin 的活动日志服务"},
	{"preflight", "创建新采集计划前的检查未通过：请检查数据源前置条件，例如 A 股交易日历的覆盖期"},
	{"capacity", "采集计划无法放进函数：请检查每函数标的数（symbols_per_function）、环境变量大小和 Access 配置"},
}

// coordinationFailureText 返回当前失败原因集合对应的告警文案；没有专属文案时给出通用提示。
func coordinationFailureText(reasons map[string]bool) string {
	for _, item := range coordinationFailureTexts {
		if reasons[item.reason] {
			return item.text
		}
	}
	return "采集计划协调失败：请检查 Collector 日志、CloudNode 和函数网络"
}

func unixTime(value float64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(value), 0).UTC()
}

// collectorStreamHeartbeatTimeout 是 collector-stream 心跳中断的告警阈值。
const collectorStreamHeartbeatTimeout = 3 * time.Minute

// collectorStreamFailedStreak 是连续多少个周期有失败标的（不含疑似下架）才告警。
const collectorStreamFailedStreak = 3

// buildCollectorStreamHealth 消费 Collector 汇总的推送采集指标：collector-stream 心跳中断超过 3 分钟、
// 连续 3 个周期有失败标的（已判定疑似下架的不计入，避免每次下架都误报）。没有心跳序列的空间（没部署推送）不出检查项。
func (b Builder) buildCollectorStreamHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	type streamState struct {
		heartbeat float64
		streaks   []string
	}
	states := map[string]*streamState{}
	load := func(metricName string, apply func(*streamState, map[string]string, float64)) error {
		series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", metricName, "", 500, now)
		if err != nil {
			return err
		}
		for _, item := range series {
			if item.IsStale {
				continue
			}
			labels, err := datasetLabels(item.LabelsJSON)
			if err != nil {
				return fmt.Errorf("collector stream labels for %s: %w", item.SeriesID, err)
			}
			current := strings.TrimSpace(labels["space_id"])
			if current == "" || (spaceID != "" && current != spaceID) {
				continue
			}
			latest, err := b.Metrics.Latest(ctx, item.SeriesID)
			if err != nil {
				return err
			}
			state := states[current]
			if state == nil {
				state = &streamState{}
				states[current] = state
			}
			apply(state, labels, latest.Value)
		}
		return nil
	}
	if err := load("moox_collector_stream_heartbeat_timestamp_seconds", func(state *streamState, _ map[string]string, value float64) {
		if value > state.heartbeat {
			state.heartbeat = value
		}
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_stream_failed_period_streak", func(state *streamState, labels map[string]string, value float64) {
		if value >= collectorStreamFailedStreak {
			state.streaks = append(state.streaks, fmt.Sprintf("%s/%s %s 连续 %d 期", labels["market"], labels["dataset_id"], labels["frequency"], int(value)))
		}
	}); err != nil {
		return nil, err
	}
	spaces := make([]string, 0, len(states))
	for space := range states {
		spaces = append(spaces, space)
	}
	sort.Strings(spaces)
	var out []BusinessStatus
	for _, space := range spaces {
		state := states[space]
		if state.heartbeat > 0 {
			status, reason := "healthy", "collector-stream 心跳正常"
			if since := now.Sub(unixTime(state.heartbeat)); since > collectorStreamHeartbeatTimeout {
				status, reason = "down", fmt.Sprintf("collector-stream 心跳已中断 %s（最后 %s）：推送采集没有写入方，持续时用 moox-cli collector ingest-mode set --mode pull --executor scf --force 切回 SCF", since.Truncate(time.Second), unixTime(state.heartbeat).Format(time.RFC3339))
			}
			out = append(out, BusinessStatus{SpaceID: space, Kind: "market_fetch", Module: "collector_stream", Status: status, Reason: reason, LastCheckedAt: now})
		}
		if len(state.streaks) > 0 {
			sort.Strings(state.streaks)
			out = append(out, BusinessStatus{SpaceID: space, Kind: "market_fetch", Module: "collector_stream_missing", Status: "down",
				Reason: "推送连续多个周期有标的没到（不含疑似下架，由补采补齐）：" + strings.Join(state.streaks, "；"), LastCheckedAt: now})
		} else if state.heartbeat > 0 {
			out = append(out, BusinessStatus{SpaceID: space, Kind: "market_fetch", Module: "collector_stream_missing", Status: "healthy", Reason: "推送周期没有连续缺失", LastCheckedAt: now})
		}
	}
	return out, nil
}

// factorWatermarkStallAfter 是因子集合连续水位阻塞多久告警：不自动跳过，交给人工（手动补采、确认缺失）。
const factorWatermarkStallAfter = 30 * time.Minute

// buildFactorWatermarkHealth 消费因子管理服务导出的连续水位阻塞起始时间：阻塞超过 30 分钟的集合告警。
func (b Builder) buildFactorWatermarkHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", "moox_factor_set_watermark_blocked_since_timestamp_seconds", "", 500, now)
	if err != nil {
		return nil, err
	}
	stalled := map[string][]string{}
	seen := map[string]bool{}
	for _, item := range series {
		if item.IsStale {
			continue
		}
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return nil, fmt.Errorf("factor watermark labels for %s: %w", item.SeriesID, err)
		}
		current := strings.TrimSpace(labels["space_id"])
		if current == "" || (spaceID != "" && current != spaceID) {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		seen[current] = true
		if latest.Value > 0 && now.Sub(unixTime(latest.Value)) > factorWatermarkStallAfter {
			stalled[current] = append(stalled[current], fmt.Sprintf("%s 自 %s 起", labels["set_id"], unixTime(latest.Value).Format(time.RFC3339)))
		}
	}
	spaces := make([]string, 0, len(seen))
	for space := range seen {
		spaces = append(spaces, space)
	}
	sort.Strings(spaces)
	out := make([]BusinessStatus, 0, len(spaces))
	for _, space := range spaces {
		if list := stalled[space]; len(list) > 0 {
			sort.Strings(list)
			out = append(out, BusinessStatus{SpaceID: space, Kind: "factor_watermark", Module: "factor_engine", Status: "down",
				Reason: "因子连续水位停滞超过 30 分钟（前面有还没补到的周期，不会自动跳过）：" + strings.Join(list, "；") + "。请检查补采，或确认行情源缺失", LastCheckedAt: now})
			continue
		}
		out = append(out, BusinessStatus{SpaceID: space, Kind: "factor_watermark", Module: "factor_engine", Status: "healthy", Reason: "因子连续水位正常", LastCheckedAt: now})
	}
	return out, nil
}

// deadlineRejectionWindow 是“周期建立前截止已过”拒绝的告警窗口：最近这段时间内出现过就告警。
const deadlineRejectionWindow = 10 * time.Minute

// buildDeadlineRejectionHealth 消费 Storage 导出的截止拒绝时刻：写入方取数晚于数据集截止时整期提交被拒（常见于采集方式切换后截止没跟上），
// 数据只能靠补采补齐，最近 10 分钟内出现过就告警。
func (b Builder) buildDeadlineRejectionHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", "moox_storage_period_deadline_rejected_timestamp_seconds", "", 500, now)
	if err != nil {
		return nil, err
	}
	recent := map[string][]string{}
	seen := map[string]bool{}
	for _, item := range series {
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return nil, fmt.Errorf("storage deadline rejection labels for %s: %w", item.SeriesID, err)
		}
		current := strings.TrimSpace(labels["space_id"])
		if current == "" || (spaceID != "" && current != spaceID) {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		seen[current] = true
		if latest.Value > 0 && now.Sub(unixTime(latest.Value)) <= deadlineRejectionWindow {
			name := b.Metrics.DatasetDisplayName(ctx, current, labels["dataset_id"])
			if name == "" {
				name = labels["dataset_id"]
			}
			recent[current] = append(recent[current], fmt.Sprintf("%s（%s，最近 %s）", name, labels["freq"], unixTime(latest.Value).Format(time.RFC3339)))
		}
	}
	spaces := make([]string, 0, len(seen))
	for space := range seen {
		spaces = append(spaces, space)
	}
	sort.Strings(spaces)
	out := make([]BusinessStatus, 0, len(spaces))
	for _, space := range spaces {
		if list := recent[space]; len(list) > 0 {
			sort.Strings(list)
			out = append(out, BusinessStatus{SpaceID: space, Kind: "storage_rejection", Module: "storage", Status: "down",
				Reason: "Storage 拒绝周期提交：周期建立前截止已过（写入方取数晚于数据集截止，常见于采集方式切换后截止没跟上），整期数据要靠补采：" + strings.Join(list, "；"), LastCheckedAt: now})
			continue
		}
		out = append(out, BusinessStatus{SpaceID: space, Kind: "storage_rejection", Module: "storage", Status: "healthy", Reason: "最近 10 分钟没有截止拒绝", LastCheckedAt: now})
	}
	return out, nil
}
