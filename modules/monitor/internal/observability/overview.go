package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/doctor"
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
}

type HostStatus struct {
	AgentID, Hostname, Status, Reason               string
	LastSeenAt                                      time.Time
	CPUPercent, MemoryPercent, FilesystemMaxPercent float64
}

type DatasetFrequencyStatus struct {
	Producer, SpaceID, DatasetID, Freq, Status, Reason string
	LastRunAt, LastSuccessAt                           time.Time
	InputWatermarkAt, OutputWatermarkAt                time.Time
	LastReportedAt                                     time.Time
	LagSeconds                                         int64
}

type BusinessStatus struct {
	SpaceID, Kind, Module, Status, Reason string
	LastCheckedAt                         time.Time
}

type Overview struct {
	GeneratedAt    time.Time
	Services       []ServiceStatus
	Hosts          []HostStatus
	Datasets       []DatasetFrequencyStatus
	BusinessChecks []BusinessStatus
}

type Builder struct {
	Metrics                    *monmetrics.QueryService
	Hosts                      *hostmetrics.Store
	Checks                     *store.CheckRepository
	Results                    *store.ResultRepository
	Policy                     report.RealtimeTimeSeriesPolicy
	BalanceDifferenceThreshold float64
	MarketFetchThresholds      MarketFetchThresholds
	Now                        func() time.Time
}

type MarketFetchThresholds struct {
	CoordinationStaleAfter      time.Duration
	PendingGrace                time.Duration
	LowCapacityHeadroom         int
	FeedFailureRateWindow       time.Duration
	FeedFailureRateThreshold    float64
	InstrumentSnapshotMaxAge    time.Duration
	InstrumentMinimumCount      int
	InstrumentRequiredExchanges []string
	EgressStaleAfter            time.Duration
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
	if out.Services, err = b.buildServices(ctx, spaceID); err != nil {
		return Overview{}, err
	}
	if out.Hosts, err = b.buildHosts(ctx); err != nil {
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

func (b Builder) buildServices(ctx context.Context, spaceID string) ([]ServiceStatus, error) {
	services := make(map[string]ServiceStatus)
	reporterServices, err := expectedReporterServices()
	if err != nil {
		return nil, err
	}
	if b.Metrics != nil && b.Metrics.Catalog() != nil {
		rows, total, err := b.Metrics.Catalog().ListServices(ctx, spaceID, 0, 500)
		if err != nil {
			return nil, err
		}
		if total > maxOverviewServices {
			return nil, fmt.Errorf("observability services exceed limit %d", maxOverviewServices)
		}
		if total > int64(len(rows)) {
			more, _, err := b.Metrics.Catalog().ListServices(ctx, spaceID, len(rows), int(total)-len(rows))
			if err != nil {
				return nil, err
			}
			rows = append(rows, more...)
		}
		for _, row := range rows {
			key := serviceInstanceKey(row.NodeID, row.ServiceName, row.InstanceID)
			if current, exists := services[key]; exists && !row.LastSeenAt.After(current.LastSeenAt) {
				continue
			}
			status, reason := "healthy", "reporter fresh"
			if row.LastSeenAt.IsZero() {
				status, reason = "unknown", "尚未上报"
			} else if row.IsStale {
				status, reason = "stale", "producer stale"
			}
			services[key] = ServiceStatus{
				NodeID: row.NodeID, ServiceName: row.ServiceName, InstanceID: row.InstanceID,
				Status: status, ReporterStatus: status, Reason: reason, LastSeenAt: row.LastSeenAt.UTC(),
			}
		}
	}

	checks, err := b.listEnabledSysDeployChecks(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	for _, check := range checks {
		labels, err := serviceCheckLabels(check.Labels)
		if err != nil {
			return nil, fmt.Errorf("sysdeploy check %q labels: %w", check.CheckID, err)
		}
		nodeID, serviceName := labels["node_id"], labels["service_name"]
		matched := make([]string, 0, 1)
		for key, item := range services {
			if item.NodeID == nodeID && item.ServiceName == serviceName {
				matched = append(matched, key)
			}
		}
		if len(matched) == 0 {
			key := serviceInstanceKey(nodeID, serviceName, "")
			service := ServiceStatus{NodeID: nodeID, ServiceName: serviceName, Status: "unknown", Reason: "health not checked"}
			if reporterServices[serviceName] {
				service.ReporterStatus = "missing"
				service.Reason = "reporter missing"
			}
			services[key] = service
			matched = append(matched, key)
		}
		var latest *domain.CheckResult
		if b.Results != nil {
			results, err := b.Results.Recent(ctx, check.SpaceID, check.CheckID, 1)
			if err != nil {
				return nil, err
			}
			if len(results) > 0 {
				latest = &results[0]
			}
		}
		for _, key := range matched {
			services[key] = mergeServiceHealth(services[key], latest)
		}
	}

	out := make([]ServiceStatus, 0, len(services))
	for _, item := range services {
		out = append(out, item)
	}
	return out, nil
}

func expectedReporterServices() (map[string]bool, error) {
	manifest, err := doctor.LoadEmbeddedManifest()
	if err != nil {
		return nil, fmt.Errorf("load observability component manifest: %w", err)
	}
	out := make(map[string]bool, len(manifest.Components))
	for _, component := range manifest.Components {
		if component.Transport == doctor.TransportReporter {
			out[component.ServiceName] = true
		}
	}
	return out, nil
}

func serviceInstanceKey(nodeID, serviceName, instanceID string) string {
	return strings.Join([]string{nodeID, serviceName, instanceID}, "\x00")
}

func (b Builder) listEnabledSysDeployChecks(ctx context.Context, spaceID string) ([]domain.Check, error) {
	if b.Checks == nil {
		return []domain.Check{}, nil
	}
	enabled := true
	out := make([]domain.Check, 0, 500)
	for page := 1; len(out) < maxOverviewServices; page++ {
		rows, err := b.Checks.List(ctx, store.ListChecksOptions{
			SpaceID: spaceID, Source: domain.CheckSourceSysDeploy, Enabled: &enabled,
			Page: store.Page{Page: page, PageSize: 500},
		})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < 500 {
			break
		}
	}
	if len(out) >= maxOverviewServices {
		total, err := b.Checks.Count(ctx, store.ListChecksOptions{
			SpaceID: spaceID, Source: domain.CheckSourceSysDeploy, Enabled: &enabled,
		})
		if err != nil {
			return nil, err
		}
		if total > maxOverviewServices {
			return nil, fmt.Errorf("sysdeploy checks exceed limit %d", maxOverviewServices)
		}
	}
	return out, nil
}

func serviceCheckLabels(raw string) (map[string]string, error) {
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return nil, err
	}
	for _, key := range []string{"node_id", "service_name"} {
		labels[key] = strings.TrimSpace(labels[key])
		if labels[key] == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	return labels, nil
}

func mergeServiceHealth(service ServiceStatus, result *domain.CheckResult) ServiceStatus {
	healthStatus, healthReason := "unknown", "health not checked"
	if result != nil {
		service.LastSeenAt = maxTime(service.LastSeenAt, result.CheckedAt.UTC())
		switch {
		case !result.Success:
			healthStatus, healthReason = "down", strings.TrimSpace(result.ErrorMessage)
			if healthReason == "" {
				healthReason = "health check failed"
			}
		case result.Status == domain.CheckStatusDegraded:
			healthStatus, healthReason = "degraded", "health check degraded"
		default:
			healthStatus, healthReason = "healthy", "health check ok"
		}
	}
	if service.ReporterStatus == "" {
		service.Status = healthStatus
		service.Reason = healthReason
		return service
	}
	if statusRank(healthStatus) < statusRank(service.Status) {
		service.Status = healthStatus
	}
	service.Reason = strings.Join([]string{service.Reason, healthReason}, "; ")
	return service
}

func (b Builder) buildHosts(ctx context.Context) ([]HostStatus, error) {
	if b.Hosts == nil {
		return []HostStatus{}, nil
	}
	rows, err := b.Hosts.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]HostStatus, 0, len(rows))
	for _, row := range rows {
		status, reason := "healthy", "agent reachable"
		if !row.Reachable {
			status, reason = "down", "agent unreachable"
		}
		item := HostStatus{AgentID: row.AgentID, Hostname: row.Hostname, Status: status, Reason: reason}
		item.LastSeenAt, _ = time.Parse(time.RFC3339Nano, row.LastSeenAt)
		if snapshot := row.Snapshot; snapshot != nil {
			if snapshot.GetCpu() != nil && snapshot.GetCpu().GetUsageAvailable() {
				item.CPUPercent = snapshot.GetCpu().GetUsagePercent()
			}
			if snapshot.GetMemory() != nil {
				item.MemoryPercent = snapshot.GetMemory().GetUsagePercent()
			}
			for _, filesystem := range snapshot.GetFilesystems() {
				item.FilesystemMaxPercent = max(item.FilesystemMaxPercent, filesystem.GetUsagePercent())
			}
		}
		out = append(out, item)
	}
	return out, nil
}

type datasetKey struct {
	service, producer, instance, spaceID, datasetID, freq, labels string
}

type datasetValues struct {
	interval, inventory, lastRun, lastSuccess, input, output float64
	reporterStale                                            bool
	reportedAt                                               time.Time
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
			key := datasetKey{service: series.ServiceName, producer: "storage_view", instance: series.InstanceID, spaceID: viewSpaceID, datasetID: viewID, freq: freq, labels: series.LabelsJSON}
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
					datasetID: key.datasetID, freq: key.freq,
				},
				value: value,
			}
		} else {
			current.value.interval = minPositive(current.value.interval, value.interval)
			current.value.inventory = max(current.value.inventory, value.inventory)
			current.value.lastRun = max(current.value.lastRun, value.lastRun)
			current.value.lastSuccess = max(current.value.lastSuccess, value.lastSuccess)
			current.value.input = max(current.value.input, value.input)
			current.value.output = max(current.value.output, value.output)
			current.value.reportedAt = maxTime(current.value.reportedAt, value.reportedAt)
			current.value.reporterStale = current.value.reporterStale && value.reporterStale
		}
		aggregated[identity] = current
	}
	out := make([]DatasetFrequencyStatus, 0, len(aggregated))
	for _, current := range aggregated {
		out = append(out, datasetStatus(now, current.key, current.value, b.Policy))
	}
	return out, nil
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
		LastRunAt: unixTime(value.lastRun), LastSuccessAt: unixTime(value.lastSuccess),
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
	watermarkLag := max(time.Duration(defaults.WatermarkPeriods)*frequency, defaults.MinimumWatermarkLag)
	for _, override := range policy.Overrides {
		if override.SpaceID == key.spaceID && override.DatasetID == key.datasetID && sameDatasetFrequency(override.Freq, key.freq) && override.WatermarkLag > 0 {
			watermarkLag = override.WatermarkLag
			break
		}
	}
	return runLag, successLag, watermarkLag
}

func sameDatasetFrequency(left, right string) bool {
	left, leftErr := report.NormalizeDatasetFrequency(strings.TrimSpace(left))
	right, rightErr := report.NormalizeDatasetFrequency(strings.TrimSpace(right))
	return leftErr == nil && rightErr == nil && left == right
}

func parseOverviewFrequency(raw string) time.Duration {
	parsed, err := report.ParseDatasetFrequency(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return parsed
}

func (b Builder) buildBusinessChecks(ctx context.Context, spaceID string) ([]BusinessStatus, error) {
	out := make([]BusinessStatus, 0)
	if b.Checks != nil && b.Results != nil {
		enabled := true
		checks, err := b.Checks.List(ctx, store.ListChecksOptions{SpaceID: spaceID, Enabled: &enabled, Page: store.Page{PageSize: 500}})
		if err != nil {
			return nil, err
		}
		for _, check := range checks {
			kind := businessKind(check)
			if kind == "" {
				continue
			}
			item := BusinessStatus{SpaceID: check.SpaceID, Kind: kind, Module: check.Source, Status: "unknown", Reason: "尚未上报"}
			results, err := b.Results.Recent(ctx, check.SpaceID, check.CheckID, 1)
			if err != nil {
				return nil, err
			}
			if len(results) > 0 {
				item.LastCheckedAt = results[0].CheckedAt.UTC()
				item.Status = strings.ToLower(string(results[0].Status))
				item.Reason = results[0].ErrorMessage
				if item.Reason == "" {
					item.Reason = map[bool]string{true: "normal", false: "check failed"}[results[0].Success]
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
			SpaceID: "crypto_market", Kind: "balance", Module: successValue.serviceName, Status: status,
			Reason: reason, LastCheckedAt: lastCheckedAt,
		})
	}
	return out, nil
}

type timerCoordinationState struct {
	required, active, lastSuccess                                                 float64
	pending, pendingSince                                                         float64
	capacityTotal, capacityRequired, capacityActive, capacityHeadroom             float64
	hasRequired, hasLastSuccess                                                   bool
	hasPending, hasPendingSince                                                   bool
	hasCapacityTotal, hasCapacityRequired, hasCapacityActive, hasCapacityHeadroom bool
	healthy                                                                       float64
	hasHealth                                                                     bool
	staleSeries                                                                   bool
	badNodes                                                                      []string
	failureReasons                                                                map[string]bool
	configuredGroupsExpected, configuredGroupsActual                              float64
	hasConfiguredGroupsExpected, hasConfiguredGroupsActual                        bool
	configuredGroupIDs                                                            map[int]float64
	configuredGroupIDObserved                                                     map[int]time.Time
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

type marketEgressHealthState struct {
	route                                            string
	expected, result, nonEmpty, distinct             float64
	lastObserved                                     time.Time
	hasExpected, hasResult, hasNonEmpty, hasDistinct bool
	kindObserved                                     map[string]time.Time
}

const maxMarketSignalSeries = 5000

// buildMarketFetchSignalHealth consumes the bounded facts emitted by short-lived
// Collector invocations and by the CLI release gate. It intentionally does not
// infer health from missing rows unless a stock signal family has already been
// observed; an uninstalled market must not page forever.
func (b Builder) buildMarketFetchSignalHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	if spaceID != "" && spaceID != "stock_cn" && spaceID != monmetrics.InternalMetricSpaceID {
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
	egress, err := b.buildStockCNEgressHealth(ctx, now)
	if err != nil {
		return nil, err
	}
	return append(append(feed, instrument...), egress...), nil
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
		if strings.TrimSpace(labels["market_id"]) != "stock_cn" || strings.TrimSpace(labels["feed_kind"]) != "kline" {
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
		out = append(out, BusinessStatus{SpaceID: "stock_cn", Kind: "market_fetch", Module: "provider_feed:" + provider, Status: status, Reason: reason, LastCheckedAt: now})
	}
	if len(out) == 0 {
		out = append(out, BusinessStatus{SpaceID: "stock_cn", Kind: "market_fetch", Module: "provider_feed", Status: "down", Reason: fmt.Sprintf("stock_cn Provider Feed 最近 %s 没有新指标", window), LastCheckedAt: now})
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
		if labels["market_id"] != "stock_cn" || item.IsStale {
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
			if labels["market_id"] != "stock_cn" || item.IsStale {
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
		out = append(out, BusinessStatus{SpaceID: "stock_cn", Kind: "market_fetch", Module: "instrument_snapshot:" + state.provider, Status: status, Reason: reason, LastCheckedAt: now})
	}
	return out, nil
}

func nonEmptyString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func (b Builder) buildStockCNEgressHealth(ctx context.Context, now time.Time) ([]BusinessStatus, error) {
	staleAfter := b.marketFetchThresholds().EgressStaleAfter
	if staleAfter <= 0 {
		staleAfter = 15 * time.Minute
	}
	series, err := b.marketMetricSeries(ctx, "moox_collector_market_egress_functions")
	if err != nil {
		return nil, err
	}
	states := map[string]*marketEgressHealthState{}
	for _, item := range series {
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return nil, fmt.Errorf("egress gate labels for %s: %w", item.SeriesID, err)
		}
		if labels["market_id"] != "stock_cn" || item.IsStale {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		if latest == nil {
			continue
		}
		route := nonEmptyString(labels["route_id"], "unknown")
		state := states[route]
		if state == nil {
			state = &marketEgressHealthState{route: route, kindObserved: make(map[string]time.Time)}
			states[route] = state
		}
		observedAt := latest.ObservedAt.UTC()
		switch labels["kind"] {
		case "expected":
			if state.kindObserved["expected"].Before(observedAt) {
				state.expected, state.hasExpected = latest.Value, true
				state.kindObserved["expected"] = observedAt
			}
		case "result":
			if state.kindObserved["result"].Before(observedAt) {
				state.result, state.hasResult = latest.Value, true
				state.kindObserved["result"] = observedAt
			}
		case "non_empty_ip":
			if state.kindObserved["non_empty_ip"].Before(observedAt) {
				state.nonEmpty, state.hasNonEmpty = latest.Value, true
				state.kindObserved["non_empty_ip"] = observedAt
			}
		case "distinct_ip":
			if state.kindObserved["distinct_ip"].Before(observedAt) {
				state.distinct, state.hasDistinct = latest.Value, true
				state.kindObserved["distinct_ip"] = observedAt
			}
		}
		state.lastObserved = maxTime(state.lastObserved, observedAt)
	}
	out := make([]BusinessStatus, 0, len(states))
	for _, state := range states {
		status, reason := "healthy", fmt.Sprintf("stock_cn 出口 IP 门禁正常，函数数 %.0f", state.expected)
		switch {
		case !isFreshMetric(now, state.lastObserved, staleAfter):
			status, reason = "down", fmt.Sprintf("stock_cn 出口 IP 门禁超过 %s 未更新", staleAfter)
		case !state.hasExpected || !isFreshMetric(now, state.kindObserved["expected"], staleAfter) || state.expected <= 0:
			status, reason = "down", "stock_cn 出口 IP 门禁没有有效 expected_function_count"
		case !state.hasResult || !isFreshMetric(now, state.kindObserved["result"], staleAfter) || state.result != state.expected:
			status, reason = "down", fmt.Sprintf("stock_cn 出口探针结果数 %.0f 不等于期望 %.0f", state.result, state.expected)
		case !state.hasNonEmpty || !isFreshMetric(now, state.kindObserved["non_empty_ip"], staleAfter) || state.nonEmpty != state.expected:
			status, reason = "down", fmt.Sprintf("stock_cn 非空出口 IP 数 %.0f 不等于期望 %.0f", state.nonEmpty, state.expected)
		case !state.hasDistinct || !isFreshMetric(now, state.kindObserved["distinct_ip"], staleAfter) || state.distinct != state.expected:
			status, reason = "down", fmt.Sprintf("stock_cn 去重出口 IP 数 %.0f 不等于期望 %.0f", state.distinct, state.expected)
		}
		out = append(out, BusinessStatus{SpaceID: "stock_cn", Kind: "market_fetch", Module: "egress_gate:" + state.route, Status: status, Reason: reason, LastCheckedAt: now})
	}
	return out, nil
}

// A full Tencent runtime-config batch may touch dozens of functions. The
// coordination timestamp measures completion of that asynchronous batch, not
// the ten-second Collector timer callback. Keep this threshold above the
// observed multi-node provider update window; Storage freshness remains the
// fast alert for an actual missing K-line.
const timerCoordinationStaleAfter = 15 * time.Minute

const timerCoordinationPendingGrace = 5 * time.Minute

// Keep a small reserve so adding a new symbol or frequency is visible before
// the next reconciliation hits a hard Timer capacity error. The metric still
// exposes the exact headroom for operators who need a different threshold.
const timerCoordinationLowCapacityHeadroom = 2

func (b Builder) marketFetchThresholds() MarketFetchThresholds {
	thresholds := b.MarketFetchThresholds
	if thresholds.CoordinationStaleAfter <= 0 {
		thresholds.CoordinationStaleAfter = timerCoordinationStaleAfter
	}
	if thresholds.PendingGrace <= 0 {
		thresholds.PendingGrace = timerCoordinationPendingGrace
	}
	if thresholds.LowCapacityHeadroom <= 0 {
		thresholds.LowCapacityHeadroom = timerCoordinationLowCapacityHeadroom
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
	if thresholds.EgressStaleAfter <= 0 {
		thresholds.EgressStaleAfter = 15 * time.Minute
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
			if service.ServiceName == "moox_collector" && !service.IsStale {
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
	if err := load("moox_collector_market_fetch_assignment_required", func(state *timerCoordinationState, _ map[string]string, value float64) {
		state.required += maxFloat(value, 0)
		state.hasRequired = true
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_assignment_active", func(state *timerCoordinationState, _ map[string]string, value float64) {
		state.active += maxFloat(value, 0)
	}); err != nil {
		return nil, err
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
	if err := load("moox_collector_market_fetch_timer_capacity_total", func(state *timerCoordinationState, _ map[string]string, value float64) {
		if !state.hasCapacityTotal || value > state.capacityTotal {
			state.capacityTotal = maxFloat(value, 0)
		}
		state.hasCapacityTotal = true
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_timer_capacity_required", func(state *timerCoordinationState, _ map[string]string, value float64) {
		if !state.hasCapacityRequired || value > state.capacityRequired {
			state.capacityRequired = maxFloat(value, 0)
		}
		state.hasCapacityRequired = true
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_timer_capacity_active", func(state *timerCoordinationState, _ map[string]string, value float64) {
		if !state.hasCapacityActive || value > state.capacityActive {
			state.capacityActive = maxFloat(value, 0)
		}
		state.hasCapacityActive = true
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_timer_capacity_headroom", func(state *timerCoordinationState, _ map[string]string, value float64) {
		// During a rolling restart more than one Collector may report the same
		// space. Use the smallest headroom so one constrained reporter cannot be
		// hidden by a newer, more optimistic series.
		if !state.hasCapacityHeadroom || value < state.capacityHeadroom {
			state.capacityHeadroom = value
		}
		state.hasCapacityHeadroom = true
	}); err != nil {
		return nil, err
	}
	if err := load("moox_collector_market_fetch_timer_available", func(state *timerCoordinationState, labels map[string]string, value float64) {
		// Disabled SCF assignments are spare capacity, not an expected
		// trigger. Their readback may legitimately be unavailable while the
		// Collector is reconciling the fleet; only enabled assignments should
		// participate in the coordination failure check.
		if strings.EqualFold(strings.TrimSpace(labels["enabled"]), "false") {
			return
		}
		// CloudNode reports -1 while a provider readback is unknown.  That is a
		// transient observation, not proof that the trigger is unavailable; the
		// next reconciliation will either confirm it or publish a real 0.
		if value != 0 {
			return
		}
		if nodeID := strings.TrimSpace(labels["node_id"]); nodeID != "" {
			state.badNodes = append(state.badNodes, nodeID)
		}
	}); err != nil {
		return nil, err
	}
	configuredGroups, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", "moox_collector_market_configured_groups", "", 500, now)
	if err != nil {
		return nil, err
	}
	for _, item := range configuredGroups {
		if item.IsStale {
			continue
		}
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return nil, fmt.Errorf("configured group labels for %s: %w", item.SeriesID, err)
		}
		currentSpace := strings.TrimSpace(labels["market_id"])
		if currentSpace != "stock_cn" || (spaceID != "" && currentSpace != spaceID) {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		state := states[currentSpace]
		if state == nil {
			state = &timerCoordinationState{}
			states[currentSpace] = state
		}
		switch strings.TrimSpace(labels["kind"]) {
		case "expected":
			if !state.hasConfiguredGroupsExpected || latest.Value > state.configuredGroupsExpected {
				state.configuredGroupsExpected = maxFloat(latest.Value, 0)
			}
			state.hasConfiguredGroupsExpected = true
		case "actual":
			if !state.hasConfiguredGroupsActual || latest.Value < state.configuredGroupsActual {
				state.configuredGroupsActual = maxFloat(latest.Value, 0)
			}
			state.hasConfiguredGroupsActual = true
		}
	}
	groupIdentitySeries, err := b.marketMetricSeries(ctx, "moox_collector_market_configured_group_id")
	if err != nil {
		return nil, err
	}
	for _, item := range groupIdentitySeries {
		if item.IsStale {
			continue
		}
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return nil, fmt.Errorf("configured group identity labels for %s: %w", item.SeriesID, err)
		}
		currentSpace := strings.TrimSpace(labels["market_id"])
		if currentSpace != "stock_cn" || (spaceID != "" && currentSpace != spaceID) {
			continue
		}
		groupID, parseErr := strconv.Atoi(strings.TrimSpace(labels["group_id"]))
		if parseErr != nil || groupID < 0 {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		state := states[currentSpace]
		if state == nil {
			state = &timerCoordinationState{}
			states[currentSpace] = state
		}
		if state.configuredGroupIDs == nil {
			state.configuredGroupIDs = make(map[int]float64)
			state.configuredGroupIDObserved = make(map[int]time.Time)
		}
		if state.configuredGroupIDObserved[groupID].Before(latest.ObservedAt.UTC()) {
			state.configuredGroupIDs[groupID] = maxFloat(latest.Value, 0)
			state.configuredGroupIDObserved[groupID] = latest.ObservedAt.UTC()
		}
	}
	out := make([]BusinessStatus, 0, len(states))
	for currentSpace, state := range states {
		pendingGrace := state.hasRequired && state.hasPending && state.pending > 0 && state.hasPendingSince && state.pendingSince > 0 &&
			now.Sub(unixTime(state.pendingSince)) >= 0 && now.Sub(unixTime(state.pendingSince)) <= thresholds.PendingGrace &&
			state.active >= state.required && len(state.badNodes) == 0
		if pendingGrace {
			reason := "Timer 配置协调进行中"
			if state.failureReasons["submit_timeout"] {
				reason = "Timer 配置提交超时，正在自动重试"
			}
			out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "healthy", Reason: reason, LastCheckedAt: now})
			continue
		}
		if state.hasCapacityTotal && state.hasCapacityRequired && state.capacityRequired > state.capacityTotal {
			shortfall := state.capacityRequired - state.capacityTotal
			reason := fmt.Sprintf("Timer SCF 容量不足：需要 %.0f 个节点，当前仅有 %.0f 个，缺口 %.0f 个", state.capacityRequired, state.capacityTotal, shortfall)
			out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "down", Reason: reason, LastCheckedAt: now})
			continue
		}
		if state.hasConfiguredGroupsExpected && state.hasConfiguredGroupsActual &&
			state.configuredGroupsExpected != state.configuredGroupsActual {
			reason := fmt.Sprintf("stock_cn Group 数量不一致：期望 %.0f，实际 %.0f", state.configuredGroupsExpected, state.configuredGroupsActual)
			out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "down", Reason: reason, LastCheckedAt: now})
			continue
		}
		if state.hasHealth && state.healthy <= 0 {
			reason := "Collector Timer 协调失败：请检查 Timer 分片容量、触发器状态和 Collector 日志"
			switch {
			case state.failureReasons["submit_timeout"]:
				reason = "Timer 配置提交持续超时：请检查 CloudNode、网关连接和批处理队列"
			case state.failureReasons["cloudnode"]:
				reason = "Timer 配置服务调用失败：请检查 CloudNode、网关连接和批处理队列"
			case state.failureReasons["environment"]:
				reason = "Timer 运行环境生成失败：请检查环境变量大小和 DNS 配置"
			case state.failureReasons["rules"]:
				reason = "Timer 采集规则读取失败：请检查 Collector 规则配置和存储连接"
			case state.failureReasons["task_instances"]:
				reason = "Timer 任务实例保存失败：请检查 Collector 数据库"
			case state.failureReasons["capacity"] && state.hasRequired && state.required > state.active:
				reason = fmt.Sprintf("Timer 分片节点不足：需要 %.0f 个，当前仅分配 %.0f 个", state.required, state.active)
			}
			out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "down", Reason: reason, LastCheckedAt: now})
			continue
		}
		if !state.hasRequired || state.required <= 0 {
			if state.staleSeries && !collectorReporterFresh {
				out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: "down", Reason: "Collector Timer 协调指标已停止上报", LastCheckedAt: now})
			}
			continue
		}
		status, reason := "healthy", "Timer 分配和触发器正常"
		switch {
		case state.hasCapacityHeadroom && state.capacityHeadroom >= 0 && state.capacityHeadroom <= float64(thresholds.LowCapacityHeadroom):
			status, reason = "degraded", fmt.Sprintf("Timer SCF 容量余量仅 %.0f 个节点，当前需要 %.0f/%.0f 个", state.capacityHeadroom, state.capacityRequired, state.capacityTotal)
		case state.active < state.required:
			status, reason = "down", fmt.Sprintf("Timer 节点分配不足：已分配 %.0f 个，需要 %.0f 个", state.active, state.required)
		case !state.hasLastSuccess || state.lastSuccess <= 0:
			status, reason = "down", "尚未完成 Timer 配置协调"
		case now.Sub(unixTime(state.lastSuccess)) > thresholds.CoordinationStaleAfter:
			status, reason = "down", fmt.Sprintf("最近一次 Timer 配置协调已超过允许时间，最后成功时间 %s", unixTime(state.lastSuccess).Format(time.RFC3339))
		case len(state.badNodes) > 0:
			sort.Strings(state.badNodes)
			status, reason = "down", "Timer 触发器不可用：节点 "+strings.Join(state.badNodes, ", ")
		case currentSpace == "stock_cn" && state.hasConfiguredGroupsExpected:
			status, reason = validateConfiguredGroupIDs(now, state, thresholds.CoordinationStaleAfter)
		}
		out = append(out, BusinessStatus{SpaceID: currentSpace, Kind: "market_fetch", Module: "scf_timer", Status: status, Reason: reason, LastCheckedAt: now})
	}
	return out, nil
}

func validateConfiguredGroupIDs(now time.Time, state *timerCoordinationState, staleAfter time.Duration) (string, string) {
	expected := int(state.configuredGroupsExpected)
	if expected <= 0 {
		return "healthy", "Timer 分配和触发器正常"
	}
	if len(state.configuredGroupIDs) != expected {
		return "down", fmt.Sprintf("stock_cn Group ID 数量不一致：期望 %d，实际 %d", expected, len(state.configuredGroupIDs))
	}
	for groupID := 0; groupID < expected; groupID++ {
		count, ok := state.configuredGroupIDs[groupID]
		observed := state.configuredGroupIDObserved[groupID]
		if !ok {
			return "down", fmt.Sprintf("stock_cn 缺少 Group ID %d", groupID)
		}
		if count != 1 {
			return "down", fmt.Sprintf("stock_cn Group ID %d 出现 %.0f 次，期望 1 次", groupID, count)
		}
		if !isFreshMetric(now, observed, staleAfter) {
			return "down", fmt.Sprintf("stock_cn Group ID %d 的配置指标已过期", groupID)
		}
	}
	for groupID := range state.configuredGroupIDs {
		if groupID < 0 || groupID >= expected {
			return "down", fmt.Sprintf("stock_cn 出现越界 Group ID %d，期望范围 0..%d", groupID, expected-1)
		}
	}
	return "healthy", "Timer 分配和触发器正常"
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
	text := strings.ToLower(strings.Join([]string{check.CheckID, check.Name, check.GroupName, check.Source}, " "))
	switch {
	case strings.Contains(text, "canary"):
		return "canary"
	case strings.Contains(text, "balance"):
		return "balance"
	default:
		return ""
	}
}

func sortOverview(out *Overview) {
	sort.Slice(out.Services, func(i, j int) bool {
		if statusRank(out.Services[i].Status) != statusRank(out.Services[j].Status) {
			return statusRank(out.Services[i].Status) < statusRank(out.Services[j].Status)
		}
		return out.Services[i].ServiceName < out.Services[j].ServiceName
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
