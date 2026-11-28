package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/checks"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/report"
)

// klineCheckPrefix 是 K 线新鲜度检查的前缀：K 线本轮没有评估（没到评估时间、Collector 暂停、评估失败）时整类冻结。
const klineCheckPrefix = "kline_freshness:"

func unixSeconds(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.Unix())
}

// buildBusinessFreshnessReporterWithInterval 返回业务域每轮的评估：按本轮指标算出组件、模块健康、数据集、K 线、关注项与
// Monitor 自己的检查的事实和冻结集合，交给 checks.Applier 推进检查表。本轮不再产出且没有冻结的检查当轮删除，
// 所以产出方读不到依赖、无法判断时必须把它负责的检查冻结，不能省略不报。
func buildBusinessFreshnessReporterWithInterval(
	builder *monitorobservability.Builder,
	repositories *store.Repositories,
	applier *checks.Applier,
	observation func(now time.Time) observationState,
	klineInterval time.Duration,
	klineEvaluators ...*monmetrics.KlineFreshnessEvaluator,
) func(context.Context) error {
	if builder == nil || repositories == nil || applier == nil {
		return nil
	}
	if klineInterval <= 0 {
		klineInterval = 30 * time.Second
	}
	var kline *monmetrics.KlineFreshnessEvaluator
	if len(klineEvaluators) > 0 {
		kline = klineEvaluators[0]
	}
	var lastKlineEvaluation time.Time
	// 上一次成功评估时被 K 线新鲜度覆盖的结果数据集与视图（键：空间\x00ID\x00频率）。评估按自己的间隔进行，
	// 没到评估时间的轮次沿用它；评估失败或没有评估器时清空，dataset 新鲜度检查恢复兜底。
	var lastCoveredDatasets map[string]struct{}
	return func(ctx context.Context) error {
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
		round := checks.Round{Domain: checks.DomainBusiness}
		facts := make(map[store.CheckKey]checks.Fact)
		add := func(fact checks.Fact) { facts[fact.Key()] = fact }
		var obsState observationState
		obsSuspended := false
		if observation != nil {
			obsState = observation(overview.GeneratedAt)
			obsSuspended = obsState.Suspended
		}
		// K 线新鲜度已覆盖的结果数据集：同一条数据链路（Storage 写入水位、View 水位）不再各自生成 dataset 新鲜度检查。
		coveredKey := func(spaceID, id, freq string) string {
			return spaceID + "\x00" + id + "\x00" + strings.ToLower(freq)
		}
		coveredDatasets := lastCoveredDatasets
		if kline == nil {
			// 没有 K 线评估器（关闭了 K 线新鲜度）：K 线检查不再产出，随之删除。
			coveredDatasets = nil
		} else if obsSuspended || collectorHalted(overview.Services) || (!lastKlineEvaluation.IsZero() && overview.GeneratedAt.Sub(lastKlineEvaluation) < klineInterval) {
			// 观测输入暂停时 storage-view 摘要不会更新；Collector 暂停或恢复追赶时采集是有意停下的；
			// 没到评估时间也不评估：以上情形 K 线检查保持原状态。
			round.FrozenPrefixes = append(round.FrozenPrefixes, klineCheckPrefix)
		} else {
			lastKlineEvaluation = overview.GeneratedAt
			coveredDatasets = map[string]struct{}{}
			reports, err := kline.Evaluate(ctx, overview.GeneratedAt)
			if err != nil {
				var inventoryErr *monmetrics.TaskResultInventoryRefreshError
				if !errors.As(err, &inventoryErr) {
					// K 线评估没有成功：清空覆盖集合，下一轮 dataset 新鲜度检查恢复兜底（评估时间已推进，不会在间隔内重试）。
					lastCoveredDatasets = nil
					return err
				}
				// 任务清单拿不到：K 线检查保持原状态，任务清单检查本身照常产出失败事实。
				round.FrozenPrefixes = append(round.FrozenPrefixes, klineCheckPrefix)
				coveredDatasets = nil
				state := inventoryErr.State
				add(checks.Fact{
					SpaceID: events.SystemSpaceID, CheckID: monmetrics.TaskResultInventoryCheckID, Name: "采集任务清单",
					Status: domain.CheckStatusDown, Reason: "无法获取采集任务清单：采集服务接口异常，K线结果检查暂停",
					Diagnostic: fmt.Sprintf("cache_available=%t cache_age_seconds=%.0f last_success_timestamp_seconds=%.0f error=%s", state.Available, state.Age.Seconds(), unixSeconds(state.LastSuccess), inventoryErr.Cause),
				})
			} else {
				for _, report := range reports {
					if klineReportCoversDatasets(report) {
						coveredDatasets[coveredKey(report.Rule.SpaceID, report.Rule.DatasetID, report.Rule.Frequency)] = struct{}{}
					}
					key := store.CheckKey{SpaceID: report.Rule.SpaceID, CheckID: report.CheckID}
					if report.Skipped {
						// 规则已停用的不再产出（删除）；其余跳过（休市、预热等）本轮无法判断，保持原状态。
						if report.Reason != "disabled" {
							round.Freeze(key)
						}
						continue
					}
					name := "采集任务清单"
					reason := klineReasonText(report, overview.GeneratedAt)
					if report.CheckID == monmetrics.TaskResultInventoryCheckID {
						reason = "采集任务清单正常"
						if !report.Success {
							reason = "控制面数据过期：采集服务暂不可用，K线检查沿用最后一份有效的任务清单"
						}
					} else {
						name = klineCheckName(datasetSubject(builder.Metrics.DatasetDisplayName(ctx, report.Rule.SpaceID, report.Rule.DatasetID), report.Rule.DatasetID, report.Rule.Frequency))
					}
					status := passStatus(report.Success)
					if report.Success && (report.PendingCount > 0 || report.Reason == "first_bar_pending") {
						// 新上线对象在等首根 K 线：按通过记录，页面显示“需关注”。
						status = domain.CheckStatusDegraded
					}
					add(checks.Fact{SpaceID: report.Rule.SpaceID, CheckID: report.CheckID, Name: name, Status: status, Reason: reason, Diagnostic: report.Diagnostic})
				}
			}
		}
		lastCoveredDatasets = coveredDatasets

		for _, item := range componentHealthItems(overview.Services) {
			if item.frozen {
				round.Freeze(item.Key())
				continue
			}
			add(item.Fact)
		}
		// datasetOwners 记下每项数据集检查由哪个服务上报：上报方停止上报时（producer stale）检查保持原状态。
		datasetOwners := make(map[store.CheckKey]string)
		if err := addDatasetFacts(ctx, builder, repositories, overview, coveredDatasets, coveredKey, add, datasetOwners); err != nil {
			return err
		}
		business, err := builder.BusinessChecks(ctx, overview.GeneratedAt)
		if err != nil {
			return err
		}
		for _, item := range business {
			key := store.CheckKey{SpaceID: item.SpaceID, CheckID: item.CheckID}
			if item.Frozen {
				round.Freeze(key)
				continue
			}
			add(checks.Fact{SpaceID: item.SpaceID, CheckID: item.CheckID, Name: item.Name, Severity: item.Severity,
				Status: passStatus(item.Status == "healthy"), Reason: item.Reason, RawError: item.RawError})
		}
		moduleFacts, err := moduleHealthItems(ctx, builder.Metrics, report.BuiltInModuleHealthChecks(), overview.GeneratedAt)
		if err != nil {
			return err
		}
		for _, fact := range moduleFacts {
			add(fact)
		}
		// 上报方停止上报（本轮有它的 reporter: 失败事实）时，它的关注项、数据集检查保持原状态，
		// 以组件“停止上报”为准，不推假恢复；组件停用后 reporter: 不再产出，这些检查随之删除。
		silent := silentReporters(facts)
		round.FreezeWhen = func(key store.CheckKey) bool {
			if service, _, ok := monitorobservability.ParseAttentionCheckID(key.CheckID); ok {
				return silent[service]
			}
			if owner, ok := datasetOwners[key]; ok {
				return silent[owner]
			}
			return false
		}
		// 观测输入（EventBus 上的服务指标，含主机网关的组件上报与 storage-view 视图摘要）中断，或刚恢复、上报方还没补上样本时：
		// 依赖它们的检查（含 K 线新鲜度）全部冻结——既不新增告警，也不恢复已有告警——只留"观测输入"这一项说明原因；
		// 行情探针（直读 Storage）与标签巡检在探针域，照常判定。
		if observation != nil {
			if obsSuspended {
				round.FrozenAll = true
				for key := range facts {
					if !observationIndependent(key.CheckID) {
						delete(facts, key)
					}
				}
			}
			ingest := checks.Fact{SpaceID: events.SystemSpaceID, CheckID: observationIngestCheckID, Name: "观测输入", Status: domain.CheckStatusOK, Reason: "观测输入正常"}
			if !obsState.Ready {
				ingest.Status, ingest.Reason = domain.CheckStatusDown, "观测输入中断：Monitor 的 EventBus 观测消费者没有连上，依赖服务指标的检查（含主机类检查）已冻结"
				ingest.RawError = obsState.Reason
			}
			add(ingest)
		}
		if len(facts) > store.MaxChecks {
			return fmt.Errorf("business freshness checks exceed limit %d", store.MaxChecks)
		}
		for _, fact := range facts {
			round.Facts = append(round.Facts, fact)
		}
		return applier.Apply(ctx, round)
	}
}

// addDatasetFacts 按数据集新鲜度生成检查事实，并记下每项检查的上报服务。被 K 线新鲜度覆盖的、生产者没有部署的、
// 上报方停止上报的不产出（后者由上报方的组件检查决定冻结还是删除）。
func addDatasetFacts(
	ctx context.Context,
	builder *monitorobservability.Builder,
	repositories *store.Repositories,
	overview monitorobservability.Overview,
	coveredDatasets map[string]struct{},
	coveredKey func(spaceID, id, freq string) string,
	add func(checks.Fact),
	owners map[store.CheckKey]string,
) error {
	factorExpected, factorExpectedKnown := false, false
	storageScopes := make(map[string]struct{})
	for _, dataset := range overview.Datasets {
		if dataset.Producer == "storage" {
			storageScopes[dataset.SpaceID+"\x00"+dataset.DatasetID+"\x00"+strings.ToLower(dataset.Freq)] = struct{}{}
		} else if dataset.Producer == "storage_view" && strings.TrimSpace(dataset.PrimaryDatasetID) != "" {
			storageScopes[dataset.SpaceID+"\x00"+strings.TrimSpace(dataset.PrimaryDatasetID)+"\x00"+strings.ToLower(dataset.Freq)] = struct{}{}
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
		checkID := strings.Join([]string{"dataset", dataset.Producer, dataset.DatasetID, dataset.Freq}, ":")
		key := store.CheckKey{SpaceID: dataset.SpaceID, CheckID: checkID}
		if dataset.ServiceName != "" {
			owners[key] = dataset.ServiceName
		}
		// Storage 写入水位与 View 水位都按结果数据集匹配：每个数据集恰好一个 View。
		covered := false
		switch dataset.Producer {
		case "storage", "storage_view":
			_, covered = coveredDatasets[coveredKey(dataset.SpaceID, dataset.DatasetID, dataset.Freq)]
		}
		if covered {
			continue
		}
		if dataset.Producer == "factor" {
			if !factorExpectedKnown {
				expected, err := serviceDeploymentExpected(ctx, repositories.Topology, "factor-mgr")
				if err != nil {
					return err
				}
				factorExpected, factorExpectedKnown = expected, true
			}
			if !factorExpected {
				continue
			}
		}
		if dataset.Reason == monitorobservability.DatasetReasonHistorySeeding {
			// 待生效：生产者因上游历史补齐暂停，不检查新鲜度；本轮没有事实，已有的检查随之按“不再检查”收尾。
			continue
		}
		if dataset.Reason == "producer stale" {
			continue
		}
		// 名称优先取生产者随清单上报的（策略实例名等），再取 Storage 里登记的数据集名称，都没有时用数据集 ID。
		displayName := dataset.DisplayName
		if displayName == "" {
			nameDatasetID := dataset.DatasetID
			if dataset.Producer == "storage_view" && dataset.PrimaryDatasetID != "" {
				nameDatasetID = dataset.PrimaryDatasetID
			}
			displayName = builder.Metrics.DatasetDisplayName(ctx, dataset.SpaceID, nameDatasetID)
		}
		add(checks.Fact{
			SpaceID: dataset.SpaceID, CheckID: checkID,
			Name:   datasetCheckName(dataset.Producer, datasetSubject(displayName, dataset.DatasetID, dataset.Freq)),
			Status: passStatus(datasetCheckPasses(dataset)), Reason: datasetReasonText(dataset, overview.GeneratedAt),
		})
	}
	return nil
}

// silentReporters 返回本轮有 reporter:<主机>:<服务> 失败事实的服务，即停止上报的服务。
func silentReporters(facts map[store.CheckKey]checks.Fact) map[string]bool {
	silent := make(map[string]bool)
	for key, fact := range facts {
		parts := strings.Split(key.CheckID, ":")
		if len(parts) == 3 && parts[0] == "reporter" && !fact.Passed() {
			silent[parts[2]] = true
		}
	}
	return silent
}

// collectorHalted 报告 Collector 模块是否处于暂停或恢复追赶：按它最近一次新鲜上报带的生命周期判断，
// 上报不新鲜或没有生命周期时算没有暂停。
func collectorHalted(services []monitorobservability.ServiceStatus) bool {
	for _, service := range services {
		if service.ServiceName != "collector" || service.Lifecycle == nil {
			continue
		}
		if stage := lifecycle.Stage(service.Lifecycle.Stage); stage == lifecycle.StagePaused || stage == lifecycle.StageResuming {
			return true
		}
	}
	return false
}

// observationIngestCheckID 是"观测输入"检查：观测消费者连上时通过，没连上时失败，说明其他检查为什么冻结。
const observationIngestCheckID = "observation:ingest"

// observationIndependent 报告一项业务检查是否不依赖观测输入：
// 业务域中只有“观测输入”本身不依赖观测输入；K 线新鲜度读的是经 EventBus 入库的 storage-view 视图摘要，观测暂停时一并冻结。
// 行情探针（直读 Storage）与标签巡检在探针域，不受观测输入冻结影响。
func observationIndependent(checkID string) bool {
	return checkID == observationIngestCheckID
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
	for _, deployment := range snapshot.Deployments {
		if deployment.ComponentID == componentID && deployment.Status == "enabled" && hosts[deployment.HostID] {
			return true, nil
		}
	}
	return false, nil
}
