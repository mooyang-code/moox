package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/placement"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

type businessFreshnessItem struct {
	spaceID, checkID, name, reason, diagnostic string
	success                                    bool
}

func unixSeconds(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.Unix())
}

func buildBusinessFreshnessReporterWithInterval(
	builder *monitorobservability.Builder,
	repositories *store.Repositories,
	hook func(context.Context, domain.Check, domain.CheckResult),
	klineInterval time.Duration,
	klineEvaluators ...*monmetrics.KlineFreshnessEvaluator,
) func(context.Context) error {
	if builder == nil || repositories == nil {
		return nil
	}
	if klineInterval <= 0 {
		klineInterval = 30 * time.Second
	}
	var lastKlineEvaluation time.Time
	return func(ctx context.Context) error {
		klineIntervalSeconds := 30
		if seconds := int(klineInterval.Round(time.Second) / time.Second); seconds > 0 {
			klineIntervalSeconds = seconds
		}
		overview, err := builder.Build(ctx, "")
		if err != nil {
			return err
		}
		items := make(map[string]businessFreshnessItem, len(overview.Services)+len(overview.Datasets)+len(overview.BusinessChecks)+1)
		suppressed := make(map[string]struct{})
		klineEvaluationRan := len(klineEvaluators) == 0 || klineEvaluators[0] == nil
		klineEvaluated := make(map[string]struct{})
		if len(klineEvaluators) > 0 && klineEvaluators[0] != nil &&
			(lastKlineEvaluation.IsZero() || overview.GeneratedAt.Sub(lastKlineEvaluation) >= klineInterval) {
			lastKlineEvaluation = overview.GeneratedAt
			klineEvaluationRan = true
			reports, err := klineEvaluators[0].Evaluate(ctx, overview.GeneratedAt)
			if err != nil {
				var inventoryErr *monmetrics.TaskResultInventoryRefreshError
				if !errors.As(err, &inventoryErr) {
					return err
				}
				klineEvaluationRan = false
				state := inventoryErr.State
				diagnostic := fmt.Sprintf("cache_available=%t cache_age_seconds=%.0f last_success_timestamp_seconds=%.0f error=%s", state.Available, state.Age.Seconds(), unixSeconds(state.LastSuccess), inventoryErr.Cause)
				items[monmetrics.InternalMetricSpaceID+"\x00"+monmetrics.TaskResultInventoryCheckID] = businessFreshnessItem{
					spaceID: monmetrics.InternalMetricSpaceID, checkID: monmetrics.TaskResultInventoryCheckID,
					name: "采集任务清单", success: false,
					reason: "无法获取采集任务清单：采集服务接口异常，K线结果检查暂停", diagnostic: diagnostic,
				}
			} else {
				for _, report := range reports {
					klineEvaluated[report.CheckID] = struct{}{}
					name := "采集任务清单"
					if report.CheckID != monmetrics.TaskResultInventoryCheckID {
						name = klineCheckName(datasetSubject(builder.Metrics.DatasetDisplayName(ctx, report.Rule.SpaceID, report.Rule.DatasetID), report.Rule.ViewID, report.Rule.Frequency))
					}
					if report.Skipped {
						if report.Reason == "disabled" {
							items[report.Rule.SpaceID+"\x00"+report.CheckID] = businessFreshnessItem{
								spaceID: report.Rule.SpaceID, checkID: report.CheckID, name: name,
								success: true, reason: noLongerExpected,
							}
						}
						continue
					}
					reason := klineReasonText(report, overview.GeneratedAt)
					if report.CheckID == monmetrics.TaskResultInventoryCheckID {
						reason = "采集任务清单正常"
					}
					items[report.Rule.SpaceID+"\x00"+report.CheckID] = businessFreshnessItem{
						spaceID: report.Rule.SpaceID, checkID: report.CheckID, name: name,
						success: report.Success, reason: reason, diagnostic: report.Diagnostic,
					}
				}
			}
		}
		for _, service := range overview.Services {
			if service.ReporterStatus == "" {
				continue
			}
			expected, err := reporterDeploymentExpected(ctx, repositories.Checks, service)
			if err != nil {
				return err
			}
			if !expected {
				continue
			}
			checkID := strings.Join([]string{
				"reporter", service.NodeID, service.ServiceName, service.InstanceID,
			}, ":")
			item := businessFreshnessItem{
				spaceID: monmetrics.InternalMetricSpaceID,
				checkID: checkID,
				name:    reporterCheckName(service.ServiceName, service.NodeID),
				success: service.ReporterStatus == "healthy",
				reason:  reporterReasonText(service.ReporterStatus),
			}
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		gatewayEvaluated := overview.GatewayHostsErr == nil
		if gatewayEvaluated {
			for _, host := range overview.GatewayHosts {
				item := businessFreshnessItem{
					spaceID: monmetrics.InternalMetricSpaceID, checkID: gatewayCheckID(host.HostID),
					name: "主机网关（" + host.HostID + "）· 心跳与路由同步", success: host.Healthy, reason: host.Reason,
				}
				items[item.spaceID+"\x00"+item.checkID] = item
			}
		}
		factorExpected, factorExpectedKnown := false, false
		storageScopes := make(map[string]struct{})
		for _, dataset := range overview.Datasets {
			if dataset.Producer == "storage" {
				storageScopes[dataset.SpaceID+"\x00"+dataset.DatasetID+"\x00"+strings.ToLower(dataset.Freq)] = struct{}{}
			} else if dataset.Producer == "storage_view" {
				for _, scopeDatasetID := range storageViewDatasetIDs(dataset.SpaceID, dataset.DatasetID, dataset.PrimaryDatasetID) {
					storageScopes[dataset.SpaceID+"\x00"+scopeDatasetID+"\x00"+strings.ToLower(dataset.Freq)] = struct{}{}
				}
			}
		}
		for _, dataset := range overview.Datasets {
			if dataset.Producer == "collector" {
				// Collector's Timer path intentionally has no per-run observer.
				// Once Storage has a fact row, its write watermark is authoritative
				// and the inventory row must not create a duplicate stale alert. If
				// Storage has never reported, retain this expectation so a newly
				// enabled but never-running Dataset is still visible as unhealthy.
				if _, exists := storageScopes[dataset.SpaceID+"\x00"+dataset.DatasetID+"\x00"+strings.ToLower(dataset.Freq)]; exists {
					continue
				}
			}
			if hostMonitoringDataset(dataset) {
				// Host samples land in these datasets; host presence and the host
				// threshold rules already alert when a host stops reporting.
				continue
			}
			checkID := strings.Join([]string{"dataset", dataset.Producer, dataset.DatasetID, dataset.Freq}, ":")
			if dataset.Producer == "factor" {
				if !factorExpectedKnown {
					factorExpected, err = serviceDeploymentExpected(ctx, repositories.Checks, "factor-mgr")
					if err != nil {
						return err
					}
					factorExpectedKnown = true
				}
				if !factorExpected {
					continue
				}
			}
			if dataset.Reason == "producer stale" {
				suppressed[dataset.SpaceID+"\x00"+checkID] = struct{}{}
				continue
			}
			nameDatasetID := dataset.DatasetID
			if dataset.Producer == "storage_view" && dataset.PrimaryDatasetID != "" {
				nameDatasetID = dataset.PrimaryDatasetID
			}
			item := businessFreshnessItem{
				spaceID: dataset.SpaceID,
				checkID: checkID,
				name:    datasetCheckName(dataset.Producer, datasetSubject(builder.Metrics.DatasetDisplayName(ctx, dataset.SpaceID, nameDatasetID), dataset.DatasetID, dataset.Freq)),
				success: dataset.Status == "healthy",
				reason:  datasetReasonText(dataset, overview.GeneratedAt),
			}
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		for _, business := range overview.BusinessChecks {
			if business.Kind != "balance" && business.Kind != "market_fetch" && business.Kind != "data_delivery" {
				continue
			}
			spaceID := business.SpaceID
			if spaceID == "" {
				spaceID = "crypto"
			}
			item := businessFreshnessItem{
				spaceID: spaceID, checkID: business.Kind + ":" + business.Module, name: business.Name,
				success: business.Status == "healthy", reason: business.Reason,
			}
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		moduleItems, err := moduleHealthItems(ctx, builder.Metrics, report.BuiltInModuleHealthChecks(), overview.GeneratedAt)
		if err != nil {
			return err
		}
		for _, item := range moduleItems {
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		enabled := true
		existing := make([]domain.Check, 0, 500)
		for page := 1; len(existing) < 1000; page++ {
			batch, err := repositories.Checks.List(ctx, store.ListChecksOptions{
				Source: domain.CheckSourceObservability, Enabled: &enabled,
				Page: store.Page{Page: page, PageSize: 500},
			})
			if err != nil {
				return err
			}
			existing = append(existing, batch...)
			if len(batch) < 500 {
				break
			}
		}
		if len(existing) >= 1000 {
			total, err := repositories.Checks.Count(ctx, store.ListChecksOptions{
				Source: domain.CheckSourceObservability, Enabled: &enabled,
			})
			if err != nil {
				return err
			}
			if total > 1000 {
				return fmt.Errorf("business freshness checks exceed limit 1000")
			}
		}
		for _, check := range existing {
			// Market canaries are evaluated by the dedicated watchdog below the
			// scheduler. Business freshness must not synthesize a success for an
			// absent overview item, otherwise a real canary failure is immediately
			// resolved by a concurrent no_longer_expected result.
			if strings.HasPrefix(check.CheckID, "market_canary:") {
				continue
			}
			if strings.HasPrefix(check.CheckID, gatewayCheckPrefix) && !gatewayEvaluated {
				// SysDeploy 暂时读不到时保留主机网关检查上一次的状态，不当作已恢复。
				continue
			}
			if strings.HasPrefix(check.CheckID, "kline_freshness:") {
				if !klineEvaluationRan {
					continue
				}
				if _, expected := klineEvaluated[check.CheckID]; !expected {
					key := check.SpaceID + "\x00" + check.CheckID
					items[key] = businessFreshnessItem{
						spaceID: check.SpaceID, checkID: check.CheckID, name: check.Name,
						success: true, reason: noLongerExpected,
					}
				}
				continue
			}
			key := check.SpaceID + "\x00" + check.CheckID
			if _, frozen := suppressed[key]; frozen {
				// Suppression avoids duplicating a producer-stale signal, but the
				// old business check still needs a successful result so its alert
				// state can resolve instead of remaining permanently firing.
				items[key] = businessFreshnessItem{
					spaceID: check.SpaceID, checkID: check.CheckID, name: check.Name,
					success: true, reason: noLongerExpected,
				}
				continue
			}
			if _, ok := items[key]; !ok {
				items[key] = businessFreshnessItem{
					spaceID: check.SpaceID, checkID: check.CheckID, name: check.Name,
					success: true, reason: noLongerExpected,
				}
			}
		}
		if len(items) > 1000 {
			return fmt.Errorf("business freshness checks exceed limit 1000")
		}

		checks := make(map[string]domain.Check, len(items))
		for key, item := range items {
			check, err := repositories.Checks.Get(ctx, item.spaceID, item.checkID)
			switch {
			case err == nil:
				if !check.Enabled {
					delete(items, key)
					continue
				}
				if item.name != "" && check.Name != item.name {
					check.Name = item.name
					if err := repositories.Checks.Update(ctx, check); err != nil {
						return err
					}
				}
				checks[key] = *check
			case errors.Is(err, gorm.ErrRecordNotFound):
				intervalSeconds := 30
				if strings.HasPrefix(item.checkID, "kline_freshness:") {
					intervalSeconds = klineIntervalSeconds
				}
				check := domain.Check{
					SpaceID: item.spaceID, CheckID: item.checkID, Name: item.name,
					GroupName: "business", Kind: domain.CheckKindExternal,
					Source: domain.CheckSourceObservability, Enabled: true,
					IntervalSeconds: intervalSeconds, TimeoutMS: 20000,
				}
				if err := repositories.Checks.Create(ctx, &check); err != nil {
					return err
				}
				checks[key] = check
			default:
				return err
			}
		}
		if err := ensureDefaultCheckAlertRules(ctx, repositories); err != nil {
			return err
		}

		now := time.Now().UTC()
		var errs []error
		for key, item := range items {
			check, ok := checks[key]
			if !ok {
				continue
			}
			if strings.HasPrefix(check.CheckID, "kline_freshness:") && check.IntervalSeconds != klineIntervalSeconds {
				check.IntervalSeconds = klineIntervalSeconds
			}
			status := domain.CheckStatusDown
			if item.success {
				status = domain.CheckStatusOK
			}
			result := domain.CheckResult{
				ResultID: fmt.Sprintf("%s-%d", item.checkID, now.UnixNano()),
				SpaceID:  item.spaceID, CheckID: item.checkID, InstanceID: "monitor",
				Success: item.success, Connected: item.success, Status: status,
				ErrorMessage: item.reason, BodyExcerpt: item.diagnostic, CheckedAt: now, CreatedAt: now,
			}
			inserted, err := repositories.Results.InsertIfAbsent(ctx, &result)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !inserted {
				continue
			}
			check.LastCheckedAt = &now
			if err := repositories.Checks.Update(ctx, &check); err != nil {
				errs = append(errs, err)
				continue
			}
			if hook != nil {
				hook(ctx, check, result)
			}
		}
		return errors.Join(errs...)
	}
}

// storageViewDatasetIDs keeps Collector freshness compatible with older View
// reporters that did not include dataset_id in the watermark labels. New
// reporters provide the exact primary dataset; the derived candidates cover
// the repository's historical view_<space>_<dataset> naming convention while
// the monitor fleet is upgraded one host at a time.
func storageViewDatasetIDs(spaceID, viewID, primaryDatasetID string) []string {
	ids := make([]string, 0, 3)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range ids {
			if existing == value {
				return
			}
		}
		ids = append(ids, value)
	}
	add(primaryDatasetID)
	viewID = strings.TrimPrefix(strings.TrimSpace(viewID), "view_")
	if viewID == "" {
		return ids
	}
	add("dataset_" + viewID)
	spaceID = strings.TrimSpace(spaceID)
	if spaceID != "" && strings.HasPrefix(viewID, spaceID+"_") {
		add("dataset_" + strings.TrimPrefix(viewID, spaceID+"_"))
	}
	return ids
}

// serviceDeploymentExpected 判断组件是否仍有启用的部署：有部署但全部停用时返回 false；完全没有部署检查时返回
// true（还没同步过部署时不压制告警）。
func serviceDeploymentExpected(
	ctx context.Context,
	checks *store.CheckRepository,
	componentID string,
) (bool, error) {
	if checks == nil || strings.TrimSpace(componentID) == "" {
		return true, nil
	}
	rows, err := checks.ListBySource(ctx, domain.CheckSourcePlacement)
	if err != nil {
		return false, err
	}
	found := false
	for _, check := range rows {
		if _, component, ok := placement.ParseCheckID(check.CheckID); ok && component == componentID {
			found = true
			if check.Enabled {
				return true, nil
			}
		}
	}
	return !found, nil
}

func reporterDeploymentExpected(
	ctx context.Context,
	checks *store.CheckRepository,
	service monitorobservability.ServiceStatus,
) (bool, error) {
	if checks == nil || strings.TrimSpace(service.NodeID) == "" || strings.TrimSpace(service.ServiceName) == "" {
		return true, nil
	}
	check, err := checks.Get(ctx, "", placement.CheckID(service.NodeID, service.ServiceName))
	switch {
	case err == nil:
		return check.Enabled, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 外部上报方（例如 SCF 采集函数）没有部署检查。
		return true, nil
	default:
		return false, err
	}
}

// noLongerExpected resolves a check whose subject was disabled or removed.
const noLongerExpected = "已停用或移除，不再检查"

// gatewayCheckPrefix 是主机网关状态检查 ID 的前缀，检查 ID 为 host_gateway:<主机>。
const gatewayCheckPrefix = "host_gateway:"

func gatewayCheckID(hostID string) string { return gatewayCheckPrefix + hostID }

func hostMonitoringDataset(dataset monitorobservability.DatasetFrequencyStatus) bool {
	for _, id := range []string{dataset.DatasetID, dataset.PrimaryDatasetID} {
		id = strings.ToLower(strings.TrimSpace(id))
		if strings.HasPrefix(id, "dataset_mooxsys_host_") || strings.HasPrefix(id, "view_mooxsys_host_") {
			return true
		}
	}
	return false
}
