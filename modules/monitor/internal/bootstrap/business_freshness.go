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
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

type businessFreshnessItem struct {
	spaceID, checkID, name, reason, diagnostic, rawError string
	success                                              bool
}

// maxFreshnessChecks 覆盖 1024 台主机各自的网关/上报检查（每台数条）及业务检查。
const maxFreshnessChecks = 1024 * 8

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
	// 上一次成功评估时被 K 线新鲜度覆盖的结果数据集与视图（键：空间\x00ID\x00频率）。评估按自己的间隔进行，
	// 没到评估时间的轮次沿用它；评估失败或没有评估器时清空，dataset 新鲜度检查恢复兜底。
	var lastCoveredDatasets, lastCoveredViews map[string]struct{}
	return func(ctx context.Context) error {
		klineIntervalSeconds := 30
		if seconds := int(klineInterval.Round(time.Second) / time.Second); seconds > 0 {
			klineIntervalSeconds = seconds
		}
		overview, err := builder.Build(ctx, "")
		if err != nil {
			return err
		}
		if overview.TopologyKnown {
			states := make([]domain.ComponentHealthState, 0, len(overview.Services))
			for _, item := range overview.Services {
				states = append(states, domain.ComponentHealthState{HostID: item.NodeID, ComponentID: item.ServiceName, Status: domain.HealthStatus(item.Status)})
			}
			if err := repositories.ComponentHealth.Reconcile(ctx, states, overview.GeneratedAt); err != nil {
				return err
			}
		}
		items := make(map[string]businessFreshnessItem, len(overview.Services)+len(overview.Datasets)+len(overview.BusinessChecks)+1)
		suppressed := make(map[string]struct{})
		retired := make(map[string]struct{})
		frozenGateway := make(map[string]bool)
		// 每台主机的网关三项信号（心跳、实例冲突、路由同步）合并成一条检查：任何一项异常都要处理，没必要分三处告警。
		type gatewayHost struct {
			unknown, unhealthy bool
			reasons, raws      []string
		}
		gatewayHosts := make(map[string]*gatewayHost)
		gatewayNames := map[string]string{"heartbeat": "心跳", "instance_conflict": "实例", "route_sync": "路由同步"}
		for _, signal := range overview.GatewaySignals {
			host := gatewayHosts[signal.HostID]
			if host == nil {
				host = &gatewayHost{}
				gatewayHosts[signal.HostID] = host
			}
			switch {
			case signal.Status == "unknown":
				host.unknown = true
			case signal.Status != "healthy":
				host.unhealthy = true
				host.reasons = append(host.reasons, gatewayNames[signal.Kind]+"："+signal.Reason)
				if signal.RawError != "" {
					host.raws = append(host.raws, signal.RawError)
				}
			}
		}
		for hostID, host := range gatewayHosts {
			checkID := "gateway:" + hostID + ":status"
			key := monmetrics.InternalMetricSpaceID + "\x00" + checkID
			// 没有任何异常信号、但有信号无法确认时保持原状态，不伪造成功去恢复既有告警。
			if host.unknown && !host.unhealthy {
				frozenGateway[key] = true
				continue
			}
			reason := "网关心跳、实例与路由同步正常"
			if host.unhealthy {
				reason = strings.Join(host.reasons, "；")
			}
			items[key] = businessFreshnessItem{spaceID: monmetrics.InternalMetricSpaceID, checkID: checkID, name: "主机网关（" + hostID + "）", success: !host.unhealthy, reason: reason, rawError: strings.Join(host.raws, "；")}
		}
		klineEvaluationRan := len(klineEvaluators) == 0 || klineEvaluators[0] == nil
		klineEvaluated := make(map[string]struct{})
		// K 线新鲜度已覆盖的结果数据集/视图：同一条数据链路（Storage 写入水位、View 水位）不再各自生成 dataset 新鲜度检查。
		coveredKey := func(spaceID, id, freq string) string {
			return spaceID + "\x00" + id + "\x00" + strings.ToLower(freq)
		}
		coveredDatasets, coveredViews := lastCoveredDatasets, lastCoveredViews
		if len(klineEvaluators) == 0 || klineEvaluators[0] == nil {
			coveredDatasets, coveredViews = nil, nil
		}
		if len(klineEvaluators) > 0 && klineEvaluators[0] != nil &&
			(lastKlineEvaluation.IsZero() || overview.GeneratedAt.Sub(lastKlineEvaluation) >= klineInterval) {
			lastKlineEvaluation = overview.GeneratedAt
			klineEvaluationRan = true
			coveredDatasets, coveredViews = map[string]struct{}{}, map[string]struct{}{}
			reports, err := klineEvaluators[0].Evaluate(ctx, overview.GeneratedAt)
			if err != nil {
				var inventoryErr *monmetrics.TaskResultInventoryRefreshError
				if !errors.As(err, &inventoryErr) {
					// K 线评估没有成功：清空覆盖集合，下一轮 dataset 新鲜度检查恢复兜底（评估时间已推进，不会在间隔内重试）。
					lastCoveredDatasets, lastCoveredViews = nil, nil
					return err
				}
				klineEvaluationRan = false
				coveredDatasets, coveredViews = nil, nil
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
					if klineReportCoversDatasets(report) {
						coveredDatasets[coveredKey(report.Rule.SpaceID, report.Rule.DatasetID, report.Rule.Frequency)] = struct{}{}
						coveredViews[coveredKey(report.Rule.SpaceID, report.Rule.ViewID, report.Rule.Frequency)] = struct{}{}
					}
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
						if !report.Success {
							reason = "控制面数据过期：采集服务暂不可用，K线检查沿用最后一份有效的任务清单"
						}
					}
					items[report.Rule.SpaceID+"\x00"+report.CheckID] = businessFreshnessItem{
						spaceID: report.Rule.SpaceID, checkID: report.CheckID, name: name,
						success: report.Success, reason: reason, diagnostic: report.Diagnostic,
					}
				}
			}
		}
		lastCoveredDatasets, lastCoveredViews = coveredDatasets, coveredViews
		for _, service := range overview.Services {
			if service.ReporterStatus == "" {
				continue
			}
			if !service.Enabled {
				continue
			}
			checkID := strings.Join([]string{"reporter", service.NodeID, service.ServiceName}, ":")
			item := businessFreshnessItem{
				spaceID: monmetrics.InternalMetricSpaceID,
				checkID: checkID,
				name:    reporterCheckName(service.ServiceName, service.NodeID),
				success: service.ReporterStatus == "healthy",
				reason:  reporterReasonText(service.ReporterStatus),
			}
			items[item.spaceID+"\x00"+item.checkID] = item
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
			// Storage 写入水位按结果数据集匹配，View 水位按确切的视图 ID 匹配：同一主数据集上另有独立视图时，它仍要自己的检查。
			covered := false
			switch dataset.Producer {
			case "storage":
				_, covered = coveredDatasets[coveredKey(dataset.SpaceID, dataset.DatasetID, dataset.Freq)]
			case "storage_view":
				_, covered = coveredViews[coveredKey(dataset.SpaceID, dataset.DatasetID, dataset.Freq)]
			}
			if covered {
				retired[dataset.SpaceID+"\x00"+checkID] = struct{}{}
				continue
			}
			if dataset.Producer == "factor" {
				if !factorExpectedKnown {
					factorExpected, err = serviceDeploymentExpected(ctx, repositories.Topology, "factor-mgr")
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
				success: datasetCheckPasses(dataset),
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
			checkID, name := "balance:"+business.Module, "账户余额同步 "+business.Module
			if business.Kind == "market_fetch" {
				checkID, name = "market_fetch:"+business.Module, "行情采集 "+business.Module+" 协调"
			} else if business.Kind == "data_delivery" {
				checkID, name = "data_delivery:"+business.Module, "数据投递队列"
			}
			item := businessFreshnessItem{spaceID: spaceID, checkID: checkID, name: name, success: business.Status == "healthy", reason: business.Reason}
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
		for page := 1; len(existing) < maxFreshnessChecks; page++ {
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
		if len(existing) >= maxFreshnessChecks {
			total, err := repositories.Checks.Count(ctx, store.ListChecksOptions{
				Source: domain.CheckSourceObservability, Enabled: &enabled,
			})
			if err != nil {
				return err
			}
			if total > maxFreshnessChecks {
				return fmt.Errorf("business freshness checks exceed limit %d", maxFreshnessChecks)
			}
		}
		for _, check := range existing {
			// Market canaries are evaluated by the dedicated watchdog below the
			// scheduler. Business freshness must not synthesize a success for an
			// absent overview item, otherwise a real canary failure is immediately
			// resolved by a concurrent no_longer_expected result.
			// 标签检查（subject_tag:）同样由行情探针独占其状态更新。
			if strings.HasPrefix(check.CheckID, "market_canary:") || strings.HasPrefix(check.CheckID, tagCheckPrefix) {
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
			if frozenGateway[key] {
				continue
			}
			if _, covered := retired[key]; covered {
				if err := disableCheckAndDeleteRules(ctx, repositories, &check); err != nil {
					return err
				}
				continue
			}
			if strings.HasPrefix(check.CheckID, "gateway:") || strings.HasPrefix(check.CheckID, "reporter:") {
				if _, expected := items[key]; !expected {
					if err := disableCheckAndDeleteRules(ctx, repositories, &check); err != nil {
						return err
					}
					continue
				}
			}
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
		if len(items) > maxFreshnessChecks {
			return fmt.Errorf("business freshness checks exceed limit %d", maxFreshnessChecks)
		}

		checks := make(map[string]domain.Check, len(items))
		for key, item := range items {
			check, err := repositories.Checks.Get(ctx, item.spaceID, item.checkID)
			switch {
			case err == nil:
				if !check.Enabled {
					// K 线检查曾因关闭新鲜度监控而退役：重新开启后，当前任务清单仍期望的检查必须恢复启用（规则随后重建）
					reenableKline := strings.HasPrefix(item.checkID, "kline_freshness:") && item.reason != noLongerExpected
					// 数据集新鲜度检查因被 K 线新鲜度覆盖而停用，覆盖解除（K 线评估失败、规则移除）后需要恢复兜底。
					reenableDataset := strings.HasPrefix(item.checkID, "dataset:") && item.reason != noLongerExpected
					if strings.HasPrefix(item.checkID, "gateway:") || strings.HasPrefix(item.checkID, "reporter:") || reenableKline || reenableDataset {
						check.Enabled = true
						if err := repositories.Checks.Update(ctx, check); err != nil {
							return err
						}
					} else {
						delete(items, key)
						continue
					}
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
				ResultID: domain.ScopedResultID(item.spaceID, item.checkID, now),
				SpaceID:  item.spaceID, CheckID: item.checkID, InstanceID: "monitor",
				Success: item.success, Connected: item.success, Status: status,
				ErrorMessage: item.reason, RawError: item.rawError, BodyExcerpt: item.diagnostic, CheckedAt: now, CreatedAt: now,
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

// datasetCheckPasses 决定数据集新鲜度检查按通过还是失败记录。数据积累中（warming）页面显示降级，但生产者在正常工作、
// 只是数据还没攒够，不是需要有人处理的故障：按通过记录，不触发也不延续告警；其余非健康状态仍按失败记录。
func datasetCheckPasses(dataset monitorobservability.DatasetFrequencyStatus) bool {
	return dataset.Status == "healthy" || dataset.Reason == monitorobservability.DatasetReasonWarming
}

// klineReportCoversDatasets 判断一条 K 线新鲜度报告是否让对应结果的 dataset 新鲜度检查成为重复：
// 规则仍在评估（含休市、预热等正常的日历跳过，此时 dataset 的固定周期水位判断反而会误报）就算覆盖；
// 任务清单自身的报告、以及规则已停用（disabled）的不算。
func klineReportCoversDatasets(report monmetrics.KlineFreshnessReport) bool {
	if report.CheckID == monmetrics.TaskResultInventoryCheckID {
		return false
	}
	return !(report.Skipped && report.Reason == "disabled")
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

func serviceDeploymentExpected(ctx context.Context, topology *store.TopologyRepository, componentID string) (bool, error) {
	if topology == nil {
		return false, nil
	}
	snapshot, err := topology.Snapshot(ctx)
	if err != nil || snapshot == nil {
		return false, err
	}
	hosts := make(map[string]bool, len(snapshot.Hosts))
	for _, host := range snapshot.Hosts {
		hosts[host.HostID] = host.Status == "enabled"
	}
	for _, placement := range snapshot.Placements {
		if placement.ComponentID == componentID && placement.Status == "enabled" && hosts[placement.HostID] {
			return true, nil
		}
	}
	return false, nil
}

// noLongerExpected resolves a check whose subject was disabled or removed.
const noLongerExpected = "已停用或移除，不再检查"

func hostMonitoringDataset(dataset monitorobservability.DatasetFrequencyStatus) bool {
	for _, id := range []string{dataset.DatasetID, dataset.PrimaryDatasetID} {
		id = strings.ToLower(strings.TrimSpace(id))
		if strings.HasPrefix(id, "dataset_mooxsys_host_") || strings.HasPrefix(id, "view_mooxsys_host_") {
			return true
		}
	}
	return false
}
