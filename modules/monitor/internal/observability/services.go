package observability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

type ReporterInstance struct {
	InstanceID, BootID, Version, Status string
	LastSeenAt                          time.Time
}

// buildServices joins all registered placements with reporters by exact host
// and component identity. Probe existence never determines registration.
func (b Builder) buildServices(ctx context.Context, now time.Time) ([]ServiceStatus, []ServiceStatus, *domain.TopologySnapshot, error) {
	if b.Topology == nil {
		return nil, nil, nil, nil
	}
	snapshot, err := b.Topology.Snapshot(ctx)
	if err != nil || snapshot == nil {
		return nil, nil, nil, err
	}
	rows, err := b.reporters(ctx, now)
	if err != nil {
		return nil, nil, snapshot, err
	}
	byPlacement := make(map[string][]monmetrics.MetricService)
	for _, row := range rows {
		key := placementKey(row.NodeID, row.ServiceName)
		byPlacement[key] = append(byPlacement[key], row)
	}
	hosts := make(map[string]domain.TopologyHost, len(snapshot.Hosts))
	for _, host := range snapshot.Hosts {
		hosts[host.HostID] = host
	}
	registered := make(map[string]bool, len(snapshot.Placements))
	services := make([]ServiceStatus, 0, len(snapshot.Placements))
	for _, placement := range snapshot.Placements {
		key := placementKey(placement.HostID, placement.ComponentID)
		registered[key] = true
		component, _ := snapshot.Catalog.Component(placement.ComponentID)
		item := ServiceStatus{
			NodeID: placement.HostID, ServiceName: placement.ComponentID,
			Enabled: placement.Status == servicecatalog.Enabled && hosts[placement.HostID].Status == servicecatalog.Enabled,
			Status:  "unknown", Reason: "health not checked", ProbeStatus: "unknown",
		}
		reporterRows := byPlacement[key]
		if component.Doctor.Transport == "reporter" || len(reporterRows) > 0 {
			item = withReporters(item, reporterRows)
		}

		var latest *domain.CheckResult
		if component.Health.Kind != "none" {
			checkID := "placement:" + placement.HostID + ":" + placement.ComponentID
			if b.Checks != nil {
				check, err := b.Checks.Get(ctx, "", checkID)
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, nil, snapshot, err
				}
				if check != nil {
					item.ProbeURL = check.URL
					if check.Kind == domain.CheckKindTCP {
						item.ProbeURL = "tcp://" + net.JoinHostPort(check.TCPHost, strconv.Itoa(check.TCPPort))
					}
				}
			}
			if b.Results != nil {
				results, err := b.Results.Recent(ctx, "", checkID, 10)
				if err != nil {
					return nil, nil, snapshot, err
				}
				item.ProbeHistory = results
				if len(results) > 0 {
					latest = &results[0]
				}
			}
		}
		switch {
		case !item.Enabled:
			item.Status, item.Reason, item.ProbeStatus = "disabled", "部署已停用", "disabled"
		case component.Health.Kind == "none":
			item.ProbeStatus, item.ProbeReason, item.Status, item.Reason = "unchecked", "不探测", "unchecked", "不探测"
		default:
			item = mergeServiceHealth(item, latest)
		}
		services = append(services, item)
	}
	// Catalog principals run outside host placements (for example SCF). Their
	// metrics remain available to business checks without inventing deployments.
	external := make(map[string]bool, len(snapshot.Catalog.Principals))
	for _, principal := range snapshot.Catalog.Principals {
		external[principal.ID] = true
	}
	var unregistered []ServiceStatus
	for _, row := range rows {
		if registered[placementKey(row.NodeID, row.ServiceName)] || external[row.ServiceName] || row.IsStale || row.LastSeenAt.IsZero() {
			continue
		}
		item := withReporters(ServiceStatus{NodeID: row.NodeID, ServiceName: row.ServiceName}, []monmetrics.MetricService{row})
		item.Status, item.Reason = "unregistered", "进程在上报，但没有部署记录"
		unregistered = append(unregistered, item)
	}
	sort.Slice(unregistered, func(i, j int) bool {
		a, z := unregistered[i], unregistered[j]
		return strings.Join([]string{a.NodeID, a.ServiceName, a.InstanceID}, "\x00") < strings.Join([]string{z.NodeID, z.ServiceName, z.InstanceID}, "\x00")
	})
	return services, unregistered, snapshot, nil
}

func (b Builder) reporters(ctx context.Context, now time.Time) ([]monmetrics.MetricService, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	rows, total, err := b.Metrics.Catalog().ListServicesAt(ctx, "", 0, 500, now)
	if err != nil {
		return nil, err
	}
	if total > maxOverviewServices {
		return nil, fmt.Errorf("observability services exceed limit %d", maxOverviewServices)
	}
	if total > int64(len(rows)) {
		more, _, err := b.Metrics.Catalog().ListServicesAt(ctx, "", len(rows), int(total)-len(rows), now)
		if err != nil {
			return nil, err
		}
		rows = append(rows, more...)
	}
	return rows, nil
}

func placementKey(hostID, componentID string) string { return hostID + "\x00" + componentID }

func withReporters(item ServiceStatus, rows []monmetrics.MetricService) ServiceStatus {
	item.ReporterStatus, item.Status, item.Reason = "missing", "unknown", "reporter missing"
	for _, row := range rows {
		status := "healthy"
		if row.IsStale {
			status = "stale"
		}
		item.Instances = append(item.Instances, ReporterInstance{InstanceID: row.InstanceID, BootID: row.BootID, Version: row.Version, Status: status, LastSeenAt: row.LastSeenAt.UTC()})
		if row.LastSeenAt.After(item.LastSeenAt) {
			item.InstanceID, item.LastSeenAt, item.ReporterStatus = row.InstanceID, row.LastSeenAt.UTC(), status
		}
	}
	if !item.LastSeenAt.IsZero() {
		item.Status, item.Reason = item.ReporterStatus, "producer stale"
		if item.ReporterStatus == "healthy" {
			item.Reason = "reporter fresh"
		}
	}
	return item
}

func mergeServiceHealth(service ServiceStatus, result *domain.CheckResult) ServiceStatus {
	healthStatus, healthReason := "unknown", "health not checked"
	if result != nil {
		service.ProbeCheckedAt, service.ProbeRawError = result.CheckedAt.UTC(), result.RawError
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
	service.ProbeStatus, service.ProbeReason = healthStatus, healthReason
	if service.ReporterStatus == "" {
		service.Status, service.Reason = healthStatus, healthReason
		return service
	}
	if statusRank(healthStatus) < statusRank(service.Status) {
		service.Status = healthStatus
	}
	service.Reason = strings.Join([]string{service.Reason, healthReason}, "; ")
	return service
}
