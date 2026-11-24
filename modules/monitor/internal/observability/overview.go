package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
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
	// Source 是健康的来源：reporter、host_snapshot、delegated（见 SourceReporter 等）。
	Source string
	// ReporterStatus 是上报的新鲜度：healthy、stale（停止上报）、missing（从未上报）。
	ReporterStatus string
	Enabled        bool
	// Readiness 是最近一次上报的就绪判断（ready、not_ready，空表示上报里没有）；NotReadyReason 是未就绪的原因。
	Readiness, NotReadyReason string
	// BootChanges 是最近 10 分钟里这个组件的启动标识变了几次（“反复重启”检查用）。
	BootChanges int
	Instances   []ReporterInstance
	// Lifecycle 是最近一次上报带的模块级生命周期；上报不新鲜或模块没有生命周期时为 nil。
	Lifecycle *LifecycleStatus
}

type HostStatus struct {
	HostID, Hostname, Status, Reason                string
	LastSeenAt                                      time.Time
	CPUPercent, MemoryPercent, FilesystemMaxPercent float64
	// 资源总量：CPU 逻辑核数、内存总量与已用量；磁盘是使用率最高的文件系统（与 FilesystemMaxPercent 同一个）。
	CPUCores                                                       uint32
	MemoryTotalBytes, MemoryUsedBytes                              uint64
	FilesystemTotalBytes, FilesystemUsedBytes                      uint64
	FilesystemMountpoint                                           string
	MemoryAvailable, DiskAvailable, MetricsAvailable, CPUAvailable bool
	// Processes 是最新一次采样里占用最高的进程，CPU、内存检查超限时随告警带出。
	Processes []alerttext.Process
}

type DatasetFrequencyStatus struct {
	Producer, SpaceID, DatasetID, Freq, Status, Reason string
	// ServiceName 是上报这项数据集指标的服务（组件 ID）。
	ServiceName string
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
	// Timezone 是市场时区（IANA 名称）：连续市场是 UTC，交易时段市场是交易所时区；没有交易日历时为 UTC。
	Timezone string
}

// BusinessStatus 是一项业务检查本轮的结果：Status 为 healthy 时通过，其余（down、degraded、stale、unknown）都算失败。
type BusinessStatus struct {
	SpaceID, Kind, Module, Status, Reason string
	CheckID, RawError, Name               string
	// Severity 是生产者给出的级别（关注项的 critical、warning）；为空时按检查 ID 推出。
	Severity string
	// Frozen 表示本轮读不到依赖、无法判断：检查保持原状态（既不推进也不删除）。
	Frozen        bool
	LastCheckedAt time.Time
}

type Overview struct {
	GeneratedAt   time.Time
	Services      []ServiceStatus
	Unregistered  []ServiceStatus
	TopologyKnown bool
	Topology      *domain.TopologySnapshot
	Hosts         []HostStatus
	Datasets      []DatasetFrequencyStatus
}

type Builder struct {
	Metrics  *monmetrics.QueryService
	Hosts    *hostmetrics.Store
	Topology *store.TopologyRepository
	Policy   report.RealtimeTimeSeriesPolicy
	// Inventory 是采集任务清单，用来确定每个空间的市场交易日历（市场、日历、时区、交易时段）。
	Inventory InventorySource
	// Datasets 列出应当有归档任务的数据集，用于“未归档的数据集”检查；为空时不做这项检查。
	Datasets ArchivableDatasets
	Now      func() time.Time
}

// InventorySource 提供最近一份有效的采集任务清单；monmetrics.TaskResultInventoryCache 实现它。
type InventorySource interface {
	GetAllowStale(ctx context.Context, now time.Time) (monmetrics.TaskResultInventorySnapshot, bool, error)
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
	if out.Hosts, err = b.buildHosts(ctx, now); err != nil {
		return Overview{}, err
	}
	if out.Services, out.Unregistered, out.Topology, err = b.buildServices(ctx, now, out.Hosts); err != nil {
		return Overview{}, err
	}
	out.TopologyKnown = out.Topology != nil
	if out.Datasets, err = b.buildDatasets(ctx, spaceID, now); err != nil {
		return Overview{}, err
	}
	sortOverview(&out)
	return out, nil
}

// BusinessChecks 按指标算出本轮的业务检查，只在检查评估里调用（页面读检查表里上一轮评估的结果）：各服务上报的关注项，
// 以及 Monitor 自己的两项——指标序列限额、未归档的数据集。每项都带检查 ID、名称（关注项另带级别）。
func (b Builder) BusinessChecks(ctx context.Context, now time.Time) ([]BusinessStatus, error) {
	attention, err := b.buildAttentionStatuses(ctx, now)
	if err != nil {
		return nil, err
	}
	seriesLimit, err := b.buildSeriesLimitHealth(ctx, now)
	if err != nil {
		return nil, err
	}
	coverage, err := b.buildArchiveCoverage(ctx, now)
	if err != nil {
		return nil, err
	}
	return append(append(attention, seriesLimit...), coverage...), nil
}

// buildSeriesLimitHealth 检查指标目录的新序列增长：新序列被限额丢弃，或近 1 小时 / 24 小时的新增数接近上限，都视为异常。
// 这通常是某个上报方的标签失控（例如把标的、请求 ID 放进了标签），不处理会撑大目录并让真正的指标被挤掉。
func (b Builder) buildSeriesLimitHealth(ctx context.Context, now time.Time) ([]BusinessStatus, error) {
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
		SpaceID: events.SystemSpaceID, Kind: "metrics_ingest", Module: "series_limit", CheckID: "metrics_ingest:series_limit", Name: "指标序列数量保护",
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
func (b Builder) HostFacts(ctx context.Context) (Overview, error) {
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	out := Overview{GeneratedAt: now}
	var err error
	if b.Topology != nil {
		if out.Topology, err = b.Topology.Snapshot(ctx); err != nil {
			return Overview{}, err
		}
	}
	out.TopologyKnown = out.Topology != nil
	if out.Hosts, err = b.buildHosts(ctx, now); err != nil {
		return Overview{}, err
	}
	if out.Services, _, _, err = b.buildServices(ctx, now, out.Hosts); err != nil {
		return Overview{}, err
	}
	return out, nil
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
		item := HostStatus{HostID: row.HostID, Hostname: row.Hostname, Status: status, Reason: reason}
		item.LastSeenAt, _ = time.Parse(time.RFC3339Nano, row.LastSeenAt)
		if snapshot := row.Snapshot; snapshot != nil {
			item.MetricsAvailable = true
			item.Processes = hostmetrics.TopProcesses(snapshot)
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
	// 生产者当前明确不期望产出的数据集（enabled 为 0）；与它同一数据集的 View 水位不再生成新鲜度检查。
	// liveScopes 是当前有生产者明确期望产出的数据集：另一个生产者（旧生产者或同生产者的其他实例）留下的 0 不能盖过它。
	disabledScopes := make(map[string]struct{})
	liveScopes := make(map[string]struct{})
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
		fromCurrentBoot := metricSampleFromBoot(enabled.MessageID, currentBoots[monmetrics.ReporterInstanceKey(series.ServiceName, series.InstanceID)])
		if enabled.Value <= 0 {
			// 生产者明确说这个数据集现在不期望产出（任务暂停、停用）：记下来，它下游的 View 检查也不做。
			if fromCurrentBoot {
				disabledScopes[datasetScopeKey(labels["space_id"], labels["dataset_id"], labels["freq"])] = struct{}{}
			}
			continue
		}
		if !fromCurrentBoot {
			continue
		}
		liveScopes[datasetScopeKey(labels["space_id"], labels["dataset_id"], labels["freq"])] = struct{}{}
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
			// View 指向的数据集已在 Storage 里被删除（例如删除策略实例连同私有因子结果）：它留下的指标序列不再代表任何在运行的东西，
			// 不进入概览，也就不会一直显示“数据生产端长时间未更新”。
			if b.Metrics.DatasetGone(ctx, viewSpaceID, labels["dataset_id"]) {
				continue
			}
			// 生产者已把这个数据集标为不期望产出（因子任务暂停等）：View 水位停在原地是预期的，不检查。
			scope := datasetScopeKey(viewSpaceID, labels["dataset_id"], freq)
			if _, off := disabledScopes[scope]; off {
				if _, live := liveScopes[scope]; !live {
					continue
				}
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
			values[key] = datasetValues{interval: interval.Seconds(), lastRun: float64(observed), lastSuccess: float64(observed), output: latest.Value, arrival: arrival, reporterStale: viewReporterStale(series, interval, now), reportedAt: latest.ObservedAt.UTC()}
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
					service: key.service, producer: key.producer, spaceID: key.spaceID,
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

const (
	// lowFrequencyViewInterval 是视图被当作低频的最短周期；lowFrequencyViewGrace 是低频视图在一个周期之外多容忍的静默时间。
	lowFrequencyViewInterval = 5 * time.Minute
	lowFrequencyViewGrace    = 5 * time.Minute
)

// viewReporterStale 判断视图的输出水位序列是否算“生产端停止上报”。视图只在输出提交（每个周期一次）时刷新水位指标，
// Storage 重启后指标也要等到下一次提交才重新出现；周期为 1 小时的视图正常情况下就会静默接近一个小时，
// 不能按通用的 2 分钟无数据判定。低频视图（周期不短于 lowFrequencyViewInterval）容忍一个完整周期再加 lowFrequencyViewGrace，
// 真正停止的视图仍会被这里或后面的运行过期判断发现；分钟级视图沿用通用判定。
func viewReporterStale(series monmetrics.MetricSeries, interval time.Duration, now time.Time) bool {
	if !series.IsStale {
		return false
	}
	if interval < lowFrequencyViewInterval {
		return true
	}
	return now.Sub(series.LastSeenAt) > interval+lowFrequencyViewGrace
}

// datasetStatus 判定一个数据集的状态。新鲜度看“最新一根 K 线是否按时到达”：按市场交易日历算出此刻应已收线的最新一根（预期 K 线），
// 还没到、并且生产者在收线超过允许迟到之后的上报里仍没有它，才判为过期；已经到了的根即使迟到，也只记下延迟，当前状态仍是正常。
func datasetStatus(now time.Time, key datasetKey, value datasetValues, policy report.RealtimeTimeSeriesPolicy, clock spaceClock) DatasetFrequencyStatus {
	item := DatasetFrequencyStatus{
		Producer: key.producer, SpaceID: key.spaceID, DatasetID: key.datasetID, Freq: key.freq,
		PrimaryDatasetID: key.primaryDatasetID,
		LastRunAt:        unixTime(value.lastRun), LastSuccessAt: unixTime(value.lastSuccess), LastPartialAt: unixTime(value.lastPartial),
		InputWatermarkAt: unixTime(value.input), OutputWatermarkAt: unixTime(value.output),
		LastReportedAt: value.reportedAt, ServiceName: key.service,
	}
	runLag, successLag, lateness := datasetTolerances(key, value.interval, policy)
	item.LatenessLimitSeconds = int64(lateness / time.Second)
	bars := evaluateBars(clock, key.freq, item.OutputWatermarkAt, unixTime(value.arrival), value.confirmedAt, now)
	if clock.err == nil && clock.clock.Exchange() {
		// 从最近一次成功或按设计的部分成功（预热中）往后数：生产者每期都失败时最近运行会逐期前移，只看它会把一整天的失败
		// 当成“刚运行过”；预热期不推进产出水位、最近成功也不前移，不算 partial 会让午休和隔夜误报。
		bars = waitingSinceRun(clock, key.freq, item.OutputWatermarkAt, laterTime(item.LastSuccessAt, item.LastPartialAt), bars)
	}
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
	// 交易所市场在午休、收盘后、非交易日不运行：上报的间隔是连续时间，按最近一次运行判停滞会在每次休市、每次开盘误报。
	// 此刻没有应收而未到的 K 线时跳过运行、成功两项；有等待中的 K 线时，从最早一根的应到时间（开盘后即本时段第一根收线）
	// 与最近一次运行、成功中较晚的那个起算，开盘后真停了照常判出来。
	exchange := clock.err == nil && clock.clock.Exchange()
	marketIdle := exchange && bars.waiting == 0
	runFrom, successFrom := item.LastRunAt, item.LastSuccessAt
	if exchange && bars.waiting > 0 {
		runFrom, successFrom = laterTime(runFrom, bars.waitingSince), laterTime(successFrom, bars.waitingSince)
	}
	switch {
	case value.reporterStale:
		item.Status, item.Reason = "stale", "producer stale"
	case ownsExpectedDatasetInventory(key.producer) && (inventoryAt.IsZero() || now.Sub(inventoryAt) > 10*time.Minute):
		item.Status, item.Reason = "unknown", "inventory_stale"
	case item.LastRunAt.IsZero():
		item.Status, item.Reason = "unknown", "尚未上报"
	case !marketIdle && runLag > 0 && now.Sub(runFrom) > runLag:
		item.Status, item.Reason = "stale", "run stale"
	case warmingNow(now, item, successLag):
		// 生产者仍在按设计只产出部分结果（历史不足、预热）：页面显示降级，但不是故障，不告警。
		item.Status, item.Reason = "degraded", DatasetReasonWarming
	case item.LastSuccessAt.IsZero():
		item.Status, item.Reason = "degraded", "尚无成功运行"
	case !marketIdle && successLag > 0 && now.Sub(successFrom) > successLag:
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

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

// DatasetReasonWarming 是“数据积累中”的原因码：生产者报告最近一次运行只按设计产出了部分结果（历史不足、
// 预热中），而且此后没有更新的成功。状态仍是 degraded，页面照常展示；告警检查把它当作通过。
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

func unixTime(value float64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(value), 0).UTC()
}
