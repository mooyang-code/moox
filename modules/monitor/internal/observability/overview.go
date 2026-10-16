package observability

import (
	"context"
	"encoding/json"
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
}

type HostStatus struct {
	HostID, AgentID, Hostname, Status, Reason                      string
	LastSeenAt                                                     time.Time
	CPUPercent, MemoryPercent, FilesystemMaxPercent                float64
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
	LagSeconds                          int64
	// LagLimitSeconds 是输出水位允许落后的秒数，超过即判为过期；0 表示没有水位要求。
	LagLimitSeconds int64
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
	Now                        func() time.Time
}

type MarketFetchThresholds struct {
	CoordinationStaleAfter      time.Duration
	PendingGrace                time.Duration
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
	if out.GatewaySignals, err = b.gatewaySignals(ctx, now); err != nil {
		return Overview{}, err
	}
	out.BusinessChecks = uniqueBusinessChecks(out.BusinessChecks)
	sortOverview(&out)
	return out, nil
}

const storageOutboxBacklogGrace = 2 * time.Minute

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
			if snapshot.GetCpu() != nil && snapshot.GetCpu().GetUsageAvailable() {
				item.CPUAvailable = true
				item.CPUPercent = snapshot.GetCpu().GetUsagePercent()
			}
			if snapshot.GetMemory() != nil {
				item.MemoryAvailable = true
				item.MemoryPercent = snapshot.GetMemory().GetUsagePercent()
			}
			item.DiskAvailable = len(snapshot.GetFilesystems()) > 0
			for _, filesystem := range snapshot.GetFilesystems() {
				item.FilesystemMaxPercent = max(item.FilesystemMaxPercent, filesystem.GetUsagePercent())
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
	reporterStale                                                         bool
	reportedAt                                                            time.Time
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
			key := datasetKey{service: series.ServiceName, producer: "storage_view", instance: series.InstanceID, spaceID: viewSpaceID, datasetID: viewID, primaryDatasetID: labels["dataset_id"], freq: freq, labels: series.LabelsJSON}
			values[key] = datasetValues{interval: interval.Seconds(), lastRun: float64(observed), lastSuccess: float64(observed), output: latest.Value, reporterStale: series.IsStale, reportedAt: latest.ObservedAt.UTC()}
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
		} else {
			current.value.interval = minPositive(current.value.interval, value.interval)
			current.value.inventory = max(current.value.inventory, value.inventory)
			current.value.lastRun = max(current.value.lastRun, value.lastRun)
			current.value.lastSuccess = max(current.value.lastSuccess, value.lastSuccess)
			current.value.lastPartial = max(current.value.lastPartial, value.lastPartial)
			current.value.input = max(current.value.input, value.input)
			current.value.output = max(current.value.output, value.output)
			current.value.reportedAt = maxTime(current.value.reportedAt, value.reportedAt)
			current.value.reporterStale = current.value.reporterStale && value.reporterStale
		}
		aggregated[identity] = current
	}
	displayNames := b.datasetDisplayNames(ctx, names, spaceID)
	out := make([]DatasetFrequencyStatus, 0, len(aggregated))
	for identity, current := range aggregated {
		item := datasetStatus(now, current.key, current.value, b.Policy)
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

func datasetStatus(now time.Time, key datasetKey, value datasetValues, policy report.RealtimeTimeSeriesPolicy) DatasetFrequencyStatus {
	item := DatasetFrequencyStatus{
		Producer: key.producer, SpaceID: key.spaceID, DatasetID: key.datasetID, Freq: key.freq,
		PrimaryDatasetID: key.primaryDatasetID,
		LastRunAt:        unixTime(value.lastRun), LastSuccessAt: unixTime(value.lastSuccess), LastPartialAt: unixTime(value.lastPartial),
		InputWatermarkAt: unixTime(value.input), OutputWatermarkAt: unixTime(value.output),
		LastReportedAt: value.reportedAt,
	}
	reference := item.OutputWatermarkAt
	if reference.IsZero() {
		reference = item.LastSuccessAt
	}
	if !reference.IsZero() {
		item.LagSeconds = max(int64(0), int64(now.Sub(reference)/time.Second))
	}
	runLag, successLag, watermarkLag := datasetTolerances(key, value.interval, policy)
	item.LagLimitSeconds = int64(watermarkLag / time.Second)
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
	case watermarkLag > 0 && item.LagSeconds > int64(watermarkLag/time.Second):
		item.Status, item.Reason = "stale", datasetWatermarkStaleReason(item, now)
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
func datasetWatermarkStaleReason(item DatasetFrequencyStatus, now time.Time) string {
	lag := formatDatasetLag(item.LagSeconds)
	latest := "未产生"
	if !item.OutputWatermarkAt.IsZero() {
		latest = item.OutputWatermarkAt.UTC().Format("2006-01-02 15:04:05 UTC")
	}
	reason := fmt.Sprintf("输出水位已落后 %s，最新输出时间 %s", lag, latest)
	if !item.InputWatermarkAt.IsZero() {
		reason += fmt.Sprintf("，输入水位 %s", item.InputWatermarkAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	if !now.IsZero() {
		reason += fmt.Sprintf("（检查时间 %s）", now.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return reason
}

func formatDatasetLag(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", max(int64(0), seconds))
	}
	minutes := seconds / 60
	remainingSeconds := seconds % 60
	if minutes < 60 {
		if remainingSeconds == 0 {
			return fmt.Sprintf("%d 分钟", minutes)
		}
		return fmt.Sprintf("%d 分 %d 秒", minutes, remainingSeconds)
	}
	hours := minutes / 60
	remainingMinutes := minutes % 60
	if remainingMinutes == 0 {
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", hours, remainingMinutes)
}

func ownsExpectedDatasetInventory(producer string) bool {
	return producer == "collector" || producer == "factor"
}

func datasetTolerances(key datasetKey, interval float64, policy report.RealtimeTimeSeriesPolicy) (time.Duration, time.Duration, time.Duration) {
	runBase := time.Duration(interval * float64(time.Second))
	frequency := parseOverviewFrequency(key.freq)
	defaults := policy.Defaults
	var runLag, successLag time.Duration
	if defaults.RunMissedIntervals > 0 && runBase > 0 {
		runLag = time.Duration(defaults.RunMissedIntervals)*runBase + 30*time.Second
	}
	if defaults.SuccessMissedIntervals > 0 && runBase > 0 {
		successLag = time.Duration(defaults.SuccessMissedIntervals)*runBase + 30*time.Second
	}
	// 水位容忍按“周期与期望间隔中较大者”放大：A 股等交易日历数据集的期望间隔跨周末、长假，可能远大于名义周期。
	watermarkLag := max(time.Duration(defaults.WatermarkPeriods)*max(frequency, runBase), defaults.MinimumWatermarkLag)
	for _, override := range policy.Overrides {
		if override.SpaceID == key.spaceID && override.DatasetID == key.datasetID && sameDatasetFrequency(override.Freq, key.freq) && override.WatermarkLag > 0 {
			watermarkLag = override.WatermarkLag
			break
		}
	}
	return runLag, successLag, watermarkLag
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

func (b Builder) marketFetchThresholds() MarketFetchThresholds {
	thresholds := b.MarketFetchThresholds
	if thresholds.CoordinationStaleAfter <= 0 {
		thresholds.CoordinationStaleAfter = timerCoordinationStaleAfter
	}
	if thresholds.PendingGrace <= 0 {
		thresholds.PendingGrace = timerCoordinationPendingGrace
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
		if pendingGrace {
			reason := "采集计划协调进行中"
			if state.failureReasons["submit_timeout"] {
				reason = "采集计划配置提交超时，正在自动重试"
			}
			out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "healthy", Reason: reason, LastCheckedAt: now})
			continue
		}
		if state.hasHealth && state.healthy <= 0 {
			reason := "采集计划协调失败：请检查 Collector 日志、CloudNode 和函数网络"
			switch {
			case state.failureReasons["submit_timeout"]:
				reason = "采集计划配置提交持续超时：请检查 CloudNode、网关连接和批处理队列"
			case state.failureReasons["cloudnode"]:
				reason = "采集计划配置服务调用失败：请检查 CloudNode、网关连接和批处理队列"
			case state.failureReasons["environment"]:
				reason = "函数运行环境生成失败：请检查环境变量大小和 DNS 配置"
			case state.failureReasons["rules"]:
				reason = "采集规则读取失败：请检查 Collector 规则配置和存储连接"
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

func unixTime(value float64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(value), 0).UTC()
}
