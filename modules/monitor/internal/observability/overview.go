package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

const (
	MaxDatasetFrequencyStatuses = 1000
	maxOverviewServices         = 1000
	maxOverviewMetricNames      = 2000
)

type ServiceStatus struct {
	NodeID, ServiceName, InstanceID, Status, Reason string
	LastSeenAt                                      time.Time
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
	Metrics *monmetrics.QueryService
	Hosts   *hostmetrics.Store
	Checks  *store.CheckRepository
	Results *store.ResultRepository
	Now     func() time.Time
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
	out.SCF = summarizeSCF(out.Services)
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

func (b Builder) buildServices(ctx context.Context, spaceID string) ([]ServiceStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return []ServiceStatus{}, nil
	}
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
	out := make([]ServiceStatus, 0, len(rows))
	for _, row := range rows {
		status, reason := "healthy", "reporter fresh"
		if row.LastSeenAt.IsZero() {
			status, reason = "unknown", "尚未上报"
		} else if row.IsStale {
			status, reason = "stale", "producer stale"
		}
		out = append(out, ServiceStatus{
			NodeID: row.NodeID, ServiceName: row.ServiceName, InstanceID: row.InstanceID,
			Status: status, Reason: reason, LastSeenAt: row.LastSeenAt.UTC(),
		})
	}
	return out, nil
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
	producer, instance, spaceID, datasetID, freq, labels string
}

type datasetValues struct {
	interval, lastRun, lastSuccess, input, output float64
	reporterStale                                 bool
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
			producer: series.ServiceName, instance: series.InstanceID, spaceID: labels["space_id"],
			datasetID: labels["dataset_id"], freq: labels["freq"], labels: series.LabelsJSON,
		}
		values[key] = datasetValues{reporterStale: series.IsStale}
	}
	if err := ensureDatasetLimit(len(values)); err != nil {
		return nil, err
	}
	for key, current := range values {
		prefix := strings.TrimSuffix(metricPrefixForDataset(key, enabledSeries), "enabled")
		for suffix, target := range map[string]*float64{
			"expected_interval_seconds":          &current.interval,
			"last_run_timestamp_seconds":         &current.lastRun,
			"last_success_timestamp_seconds":     &current.lastSuccess,
			"input_watermark_timestamp_seconds":  &current.input,
			"output_watermark_timestamp_seconds": &current.output,
		} {
			series, err := b.Metrics.Catalog().FindSeries(ctx, "", key.producer, prefix+suffix, key.labels, 500)
			if err != nil {
				return nil, err
			}
			for _, item := range series {
				if item.InstanceID != key.instance {
					continue
				}
				latest, err := b.Metrics.Latest(ctx, item.SeriesID)
				if err != nil {
					return nil, err
				}
				*target = latest.Value
				break
			}
		}
		values[key] = current
	}
	out := make([]DatasetFrequencyStatus, 0, len(values))
	for key, value := range values {
		out = append(out, datasetStatus(now, key, value))
	}
	return out, nil
}

func metricPrefixForDataset(key datasetKey, series []monmetrics.MetricSeries) string {
	for _, item := range series {
		if item.ServiceName == key.producer && item.InstanceID == key.instance && item.LabelsJSON == key.labels {
			return strings.TrimSuffix(item.MetricName, "_dataset_enabled") + "_dataset_enabled"
		}
	}
	return ""
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

func datasetStatus(now time.Time, key datasetKey, value datasetValues) DatasetFrequencyStatus {
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
	switch {
	case value.reporterStale:
		item.Status, item.Reason = "stale", "producer stale"
	case item.LastRunAt.IsZero():
		item.Status, item.Reason = "unknown", "尚未上报"
	case item.LastSuccessAt.IsZero():
		item.Status, item.Reason = "degraded", "尚无成功运行"
	case value.interval > 0 && item.LagSeconds > int64(max(2*value.interval, 120)):
		item.Status, item.Reason = "stale", "watermark stale"
	case item.OutputWatermarkAt.IsZero():
		item.Status, item.Reason = "healthy", "正常但空结果"
	default:
		item.Status, item.Reason = "healthy", "normal"
	}
	return item
}

func (b Builder) buildBusinessChecks(ctx context.Context, spaceID string) ([]BusinessStatus, error) {
	if b.Checks == nil || b.Results == nil {
		return []BusinessStatus{}, nil
	}
	enabled := true
	checks, err := b.Checks.List(ctx, store.ListChecksOptions{SpaceID: spaceID, Enabled: &enabled, Page: store.Page{PageSize: 500}})
	if err != nil {
		return nil, err
	}
	out := make([]BusinessStatus, 0)
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

func summarizeSCF(services []ServiceStatus) SCFSummary {
	var out SCFSummary
	for _, service := range services {
		name := strings.ToLower(service.ServiceName)
		if !strings.Contains(name, "scf") {
			continue
		}
		switch service.Status {
		case "healthy":
			out.OnlineCount++
		case "stale", "down":
			out.TimeoutCount++
		default:
			out.UnknownCount++
		}
		if !service.LastSeenAt.IsZero() && (out.OldestHeartbeatAt.IsZero() || service.LastSeenAt.Before(out.OldestHeartbeatAt)) {
			out.OldestHeartbeatAt = service.LastSeenAt
		}
	}
	return out
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
