package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

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
}

type HostStatus struct {
	AgentID, Hostname, Status, Reason               string
	LastSeenAt                                      time.Time
	CPUPercent, MemoryPercent, FilesystemMaxPercent float64
}

type SCFSummary struct {
	OnlineCount, TimeoutCount, UnknownCount int
	OldestHeartbeatAt                       time.Time
}

type DatasetFrequencyStatus struct {
	Producer, SpaceID, DatasetID, Freq, Status, Reason string
	LastRunAt, LastSuccessAt                           time.Time
	InputWatermarkAt, OutputWatermarkAt                time.Time
	LagSeconds                                         int64
}

type BusinessStatus struct {
	Kind, Module, Status, Reason string
	LastCheckedAt                time.Time
}

type Overview struct {
	GeneratedAt    time.Time
	Services       []ServiceStatus
	Hosts          []HostStatus
	SCF            SCFSummary
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
	Now                        func() time.Time
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
	if out.SCF, err = b.buildSCFSummary(ctx, now); err != nil {
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
	sortOverview(&out)
	return out, nil
}

func (b Builder) buildSCFSummary(ctx context.Context, now time.Time) (SCFSummary, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return SCFSummary{}, nil
	}
	const (
		nodesMetric     = "moox_cloudnode_scf_nodes"
		oldestAgeMetric = "moox_cloudnode_scf_oldest_heartbeat_age_seconds"
		maxSeries       = 500
	)
	series, total, err := b.Metrics.Catalog().ListSeries(ctx, "", nodesMetric, "", 0, maxSeries+1)
	if err != nil {
		return SCFSummary{}, err
	}
	if total > maxSeries {
		return SCFSummary{}, fmt.Errorf("SCF metric series exceed limit %d", maxSeries)
	}
	if len(series) == 0 {
		return SCFSummary{}, nil
	}
	latestInstance := series[0].InstanceID
	latestSeen := series[0].LastSeenAt
	for _, item := range series[1:] {
		if item.LastSeenAt.After(latestSeen) {
			latestInstance, latestSeen = item.InstanceID, item.LastSeenAt
		}
	}

	var out SCFSummary
	var stale bool
	for _, item := range series {
		if item.InstanceID != latestInstance {
			continue
		}
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return SCFSummary{}, fmt.Errorf("SCF status labels: %w", err)
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return SCFSummary{}, err
		}
		count, err := nonNegativeMetricCount(latest.Value)
		if err != nil {
			return SCFSummary{}, fmt.Errorf("SCF status %q: %w", labels["status"], err)
		}
		stale = stale || item.IsStale
		switch labels["status"] {
		case "online":
			out.OnlineCount += count
		case "timeout":
			out.TimeoutCount += count
		case "unknown":
			out.UnknownCount += count
		default:
			return SCFSummary{}, fmt.Errorf("unknown SCF heartbeat status %q", labels["status"])
		}
	}
	if stale {
		out.UnknownCount += out.OnlineCount + out.TimeoutCount
		out.OnlineCount, out.TimeoutCount = 0, 0
		return out, nil
	}

	ages, ageTotal, err := b.Metrics.Catalog().ListSeries(ctx, "", oldestAgeMetric, "", 0, maxSeries+1)
	if err != nil {
		return SCFSummary{}, err
	}
	if ageTotal > maxSeries {
		return SCFSummary{}, fmt.Errorf("SCF oldest heartbeat series exceed limit %d", maxSeries)
	}
	for _, item := range ages {
		if item.InstanceID != latestInstance {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return SCFSummary{}, err
		}
		if latest.Value < 0 || math.IsNaN(latest.Value) || math.IsInf(latest.Value, 0) {
			return SCFSummary{}, fmt.Errorf("SCF oldest heartbeat age is invalid")
		}
		if out.OnlineCount+out.TimeoutCount+out.UnknownCount > 0 {
			age := latest.Value
			if observedLag := now.Sub(latest.ObservedAt).Seconds(); observedLag > 0 {
				age += observedLag
			}
			out.OldestHeartbeatAt = now.Add(-time.Duration(age * float64(time.Second)))
		}
		break
	}
	return out, nil
}

func nonNegativeMetricCount(value float64) (int, error) {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value > maxOverviewServices {
		return 0, fmt.Errorf("count is invalid")
	}
	return int(value), nil
}

func (b Builder) buildServices(ctx context.Context, spaceID string) ([]ServiceStatus, error) {
	services := make(map[string]ServiceStatus)
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
			services[key] = ServiceStatus{
				NodeID: nodeID, ServiceName: serviceName, Status: "unknown",
				ReporterStatus: "missing", Reason: "reporter missing",
			}
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
		values[key] = datasetValues{reporterStale: series.IsStale}
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
	case watermarkLag > 0 && item.LagSeconds > int64(watermarkLag):
		item.Status, item.Reason = "stale", "watermark stale"
	case item.OutputWatermarkAt.IsZero():
		item.Status, item.Reason = "healthy", "正常但空结果"
	default:
		item.Status, item.Reason = "healthy", "normal"
	}
	return item
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
		if override.SpaceID == key.spaceID && override.DatasetID == key.datasetID && override.Freq == key.freq && override.WatermarkLag > 0 {
			watermarkLag = override.WatermarkLag
			break
		}
	}
	return runLag, successLag, watermarkLag
}

func parseOverviewFrequency(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.ParseUint(strings.TrimSuffix(raw, "d"), 10, 32)
		if err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
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
			item := BusinessStatus{Kind: kind, Module: check.Source, Status: "unknown", Reason: "尚未上报"}
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
		lastSuccessAt := unixTime(successValue.value)
		lastCheckedAt := lastSuccessAt
		if runValue, ok := lastRun[key]; ok {
			lastCheckedAt = unixTime(runValue.value)
		}
		status, reason := "healthy", "balance sync fresh"
		switch {
		case successValue.stale:
			status, reason = "stale", "producer stale"
		case failures[key].value >= 3:
			status, reason = "down", "balance sync failed 3 consecutive runs"
		case differences[key].value > threshold:
			status, reason = "down", fmt.Sprintf("balance difference %.4f exceeds %.4f", differences[key].value, threshold)
		case lastSuccessAt.IsZero():
			status, reason = "unknown", "尚未上报"
		case now.Sub(lastSuccessAt) > 15*time.Minute:
			status, reason = "down", "balance sync stale"
		}
		out = append(out, BusinessStatus{
			Kind: "balance", Module: successValue.serviceName, Status: status,
			Reason: reason, LastCheckedAt: lastCheckedAt,
		})
	}
	return out, nil
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
