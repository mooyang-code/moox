package healthview

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

type Builder struct {
	Facts         *observability.Builder
	Checks        *store.CheckRepository
	Results       *store.ResultRepository
	Alerts        *store.AlertRepository
	Notifications *store.NotificationRepository
	Now           func() time.Time
}

func (b Builder) Build(ctx context.Context, spaceID string) (Overview, error) {
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	out := Overview{GeneratedAt: now, Alerts: []Alert{}, BusinessItems: []Item{}, ServiceItems: []Item{}}
	var facts observability.Overview
	latestResults := map[string]domain.CheckResult{}
	business := make(map[string]Item, len(businessCatalog))
	for _, definition := range businessCatalog {
		business["business:"+definition.Name] = Item{Group: "business", Name: definition.Name, Description: definition.Remark, Status: "unknown"}
	}
	if b.Facts != nil {
		builtFacts, err := b.Facts.Build(ctx, spaceID)
		if err != nil {
			return out, err
		}
		facts = builtFacts
		storageScopes := storageDatasetScopes(facts.Datasets)
		for _, item := range facts.Datasets {
			// 主机指标由主机工作台统一展示和告警。健康检查页不再把
			// host_* Primary/View 数据集混入“行情采集”，避免主机视图
			// 重建或积累历史数据时产生无关的行情告警。
			if isHostMonitoringDataset(item) {
				continue
			}
			// Timer 采集器只负责调度 SCF，不上报每个数据集的完成时间。
			// 只要 Primary Storage 已经有同一数据集/周期的事实，就以
			// Storage 的成功时间和输出水位作为行情健康依据，避免采集器
			// 缺少 last_run 指标时把整张“行情采集”卡片误判为尚未上报。
			if collectorCoveredByStorage(item, storageScopes) {
				continue
			}
			id := item.Producer + ":" + item.DatasetID
			name, desc := businessName(id)
			checkedAt := item.LastReportedAt
			if isBusinessName(name) {
				reason := ChineseReason(item.Reason)
				mergeItem(business, businessKey(id), Item{Group: "business", Name: name, Description: desc, Status: Status(item.Status), Reason: reason, CheckedAt: checkedAt, Instances: []Instance{{Name: item.DatasetID, NodeID: item.Producer, InstanceID: item.Freq, Status: Status(item.Status), Conclusion: reason, LastCheckedAt: checkedAt}}})
			}
		}
		for _, item := range facts.BusinessChecks {
			id := item.Module + ":" + item.Kind
			name, desc := businessName(id)
			if isBusinessName(name) {
				reason := ChineseReason(item.Reason)
				mergeItem(business, businessKey(id), Item{Group: "business", Name: name, Description: desc, Status: Status(item.Status), Reason: reason, CheckedAt: item.LastCheckedAt, Instances: []Instance{{Name: item.Kind, NodeID: item.Module, InstanceID: item.SpaceID, Status: Status(item.Status), Conclusion: reason, LastCheckedAt: item.LastCheckedAt}}})
			}
		}
		for key, item := range business {
			item.ID = key
			out.BusinessItems = append(out.BusinessItems, item)
		}
		sort.Slice(out.BusinessItems, func(i, j int) bool {
			if healthRank(out.BusinessItems[i].Status) != healthRank(out.BusinessItems[j].Status) {
				return healthRank(out.BusinessItems[i].Status) > healthRank(out.BusinessItems[j].Status)
			}
			return out.BusinessItems[i].Name < out.BusinessItems[j].Name
		})
		services := make(map[string]Item)
		for _, item := range facts.Services {
			name, desc := serviceName(item.ServiceName)
			// 主机工作台已经提供主机监控详情，健康页只展示业务和核心服务状态。
			if name == "主机监控" {
				continue
			}
			reason := ChineseReason(item.Reason)
			mergeItem(services, "service:"+name, Item{Group: "service", Name: name, Description: desc, Status: Status(item.Status), Reason: reason, CheckedAt: item.LastSeenAt, Instances: []Instance{{Name: item.ServiceName, NodeID: item.NodeID, InstanceID: item.InstanceID, Status: Status(item.Status), Conclusion: reason, LastCheckedAt: item.LastSeenAt}}})
		}
		for key, item := range services {
			item.ID = key
			out.ServiceItems = append(out.ServiceItems, item)
		}
		sort.Slice(out.ServiceItems, func(i, j int) bool {
			if healthRank(out.ServiceItems[i].Status) != healthRank(out.ServiceItems[j].Status) {
				return healthRank(out.ServiceItems[i].Status) > healthRank(out.ServiceItems[j].Status)
			}
			return out.ServiceItems[i].Name < out.ServiceItems[j].Name
		})
	}
	if b.Facts == nil {
		for key, item := range business {
			item.ID = key
			out.BusinessItems = append(out.BusinessItems, item)
		}
		sort.Slice(out.BusinessItems, func(i, j int) bool { return out.BusinessItems[i].Name < out.BusinessItems[j].Name })
	}
	if b.Alerts != nil {
		if b.Results != nil {
			results, err := b.Results.Latest(ctx, 5000)
			if err != nil {
				return out, err
			}
			for _, result := range results {
				latestResults[checkResultKey(result.SpaceID, result.CheckID)] = result
			}
		}
		states, err := b.Alerts.ListEnabledFiringStates(ctx, spaceID, 100)
		if err != nil {
			return out, err
		}
		for _, state := range states {
			if isHostMonitoringCheck(state.CheckID) {
				continue
			}
			name := alertTitle(state.CheckID)
			reason := "监控项持续异常"
			checked := state.UpdatedAt.UTC()
			if checked.IsZero() && state.TriggeredAt != nil {
				checked = state.TriggeredAt.UTC()
			}
			if dataset, ok := findDatasetAlertFact(state.CheckID, facts.Datasets); ok {
				name = datasetAlertTitle(state.CheckID, dataset)
				reason = datasetAlertReason(dataset, now)
			} else if result, ok := latestResults[checkResultKey(state.SpaceID, state.CheckID)]; ok {
				if result.Success && b.Results != nil {
					// An alert can remain firing during its recovery threshold. In
					// that window the newest result is healthy, so retain the latest
					// failed probe as the operator-facing explanation.
					if recent, recentErr := b.Results.Recent(ctx, state.SpaceID, state.CheckID, 10); recentErr == nil {
						for _, candidate := range recent {
							if !candidate.Success {
								result = candidate
								break
							}
						}
					}
				}
				reason = checkResultReason(result.ErrorMessage)
				if !result.CheckedAt.IsZero() {
					checked = result.CheckedAt.UTC()
				}
			}
			out.Alerts = append(out.Alerts, Alert{ID: state.DedupeKey, Title: name, Status: "down", Reason: reason, Severity: "critical", CheckedAt: checked})
		}
		sort.Slice(out.Alerts, func(i, j int) bool { return out.Alerts[i].CheckedAt.After(out.Alerts[j].CheckedAt) })
	}
	if b.Notifications != nil {
		channel, err := b.Notifications.GetGlobal(ctx)
		if err == nil && channel != nil {
			out.NotificationType = channel.ChannelType
			out.NotificationConfigured = strings.TrimSpace(channel.WebhookURL) != ""
			out.NotificationMasked = MaskURL(channel.WebhookURL)
		}
	}
	return out, nil
}

func isHostMonitoringDataset(item observability.DatasetFrequencyStatus) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.DatasetID)), "dataset_mooxsys_host_")
}

func storageDatasetScopes(items []observability.DatasetFrequencyStatus) map[string]struct{} {
	scopes := make(map[string]struct{})
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Producer), "storage") {
			scopes[datasetScopeKey(item.SpaceID, item.DatasetID, item.Freq)] = struct{}{}
		}
	}
	return scopes
}

func collectorCoveredByStorage(item observability.DatasetFrequencyStatus, storageScopes map[string]struct{}) bool {
	if !strings.EqualFold(strings.TrimSpace(item.Producer), "collector") {
		return false
	}
	_, ok := storageScopes[datasetScopeKey(item.SpaceID, item.DatasetID, item.Freq)]
	return ok
}

func datasetScopeKey(spaceID, datasetID, freq string) string {
	return strings.Join([]string{
		strings.TrimSpace(spaceID),
		strings.TrimSpace(datasetID),
		strings.ToLower(strings.TrimSpace(freq)),
	}, "\x00")
}

func isHostMonitoringCheck(checkID string) bool {
	checkID = strings.ToLower(strings.TrimSpace(checkID))
	if strings.HasPrefix(checkID, "host:") {
		return true
	}
	_, datasetID, _, ok := datasetCheckParts(checkID)
	return ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(datasetID)), "dataset_mooxsys_host_")
}

func checkResultKey(spaceID, checkID string) string {
	return spaceID + "\x00" + checkID
}

func checkResultReason(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "监控项持续异常"
	}
	if strings.Contains(raw, "context deadline exceeded") {
		return "健康检查超时：服务未能在规定时间内返回就绪状态"
	}
	if strings.Contains(raw, "connection refused") {
		return "健康检查失败：服务端口拒绝连接"
	}
	if strings.Contains(raw, "unexpected HTTP status") {
		return "健康检查失败：返回了非预期的 HTTP 状态码（" + raw + "）"
	}
	translated := ChineseReason(raw)
	if translated == "监控检查失败，请查看日志详情" {
		return "健康检查失败：" + raw
	}
	return translated
}

func businessKey(id string) string {
	name, _ := businessName(id)
	return "business:" + name
}

func isBusinessName(name string) bool {
	for _, definition := range businessCatalog {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func businessName(id string) (string, string) {
	name, desc := ChineseName(id)
	if name == "数据与视图" {
		return "行情采集", "检查行情数据和视图的最新水位"
	}
	return name, desc
}

func mergeItem(items map[string]Item, key string, candidate Item) {
	current, ok := items[key]
	if !ok {
		candidate.Instances = limitInstances(candidate.Instances, &candidate.OmittedInstanceCount)
		items[key] = candidate
		return
	}
	combined := append(append([]Instance{}, current.Instances...), candidate.Instances...)
	omitted := current.OmittedInstanceCount + candidate.OmittedInstanceCount
	combined = limitInstances(combined, &omitted)
	candidate.Instances = combined
	candidate.OmittedInstanceCount = omitted
	preferCandidate := current.Status == "unknown" && len(current.Instances) == 0 && candidate.Status != "unknown"
	if preferCandidate || worse(candidate.Status, current.Status) || (candidate.Status == current.Status && candidate.CheckedAt.After(current.CheckedAt)) {
		if candidate.Reason == "" {
			candidate.Reason = current.Reason
		}
		items[key] = candidate
		return
	}
	if candidate.CheckedAt.After(current.CheckedAt) {
		current.CheckedAt = candidate.CheckedAt
	}
	if current.Reason == "" && candidate.Reason != "" {
		current.Reason = candidate.Reason
	}
	current.OmittedInstanceCount = omitted
	current.Instances = combined
	items[key] = current
}

const maxInstancesPerItem = 100

func limitInstances(instances []Instance, omitted *int32) []Instance {
	if len(instances) <= maxInstancesPerItem {
		return instances
	}
	if omitted != nil {
		*omitted += int32(len(instances) - maxInstancesPerItem)
	}
	return instances[:maxInstancesPerItem]
}

func worse(left, right string) bool {
	return healthRank(left) > healthRank(right)
}

func healthRank(status string) int {
	switch status {
	case "down":
		return 4
	case "degraded":
		return 3
	case "unknown":
		return 2
	case "healthy":
		return 1
	default:
		return 0
	}
}

func alertTitle(checkID string) string {
	if producer, datasetID, freq, ok := datasetCheckParts(checkID); ok {
		switch producer {
		case "factor":
			return "因子计算任务 · " + datasetID + " / " + freq
		case "storage":
			return datasetResultTitle(datasetID, false) + " · " + datasetID + " / " + freq
		case "storage_view":
			return datasetResultTitle(datasetID, true) + " · " + datasetID + " / " + freq
		}
	}
	parts := strings.Split(strings.TrimSpace(checkID), ":")
	if len(parts) >= 3 && parts[0] == "host" {
		metricNames := map[string]string{
			"cpu": "CPU 使用率", "memory": "内存使用率", "filesystem_usage": "磁盘占用率",
			"disk_utilization": "磁盘利用率", "network_errors": "网络错误", "presence": "在线状态",
		}
		metric := metricNames[strings.ToLower(parts[len(parts)-1])]
		if metric == "" {
			metric = "主机健康状态"
		}
		return "主机 " + parts[1] + " · " + metric
	}
	if len(parts) >= 2 && parts[0] == "sysdeploy" {
		name, _ := serviceName(parts[len(parts)-1])
		if name == "因子计算" {
			return "因子计算服务"
		}
		return name
	}
	name, _ := ChineseName(checkID)
	return name
}

func datasetCheckParts(checkID string) (producer, datasetID, freq string, ok bool) {
	parts := strings.Split(strings.TrimSpace(checkID), ":")
	if len(parts) < 4 || parts[0] != "dataset" {
		return "", "", "", false
	}
	producer = strings.TrimSpace(parts[1])
	freq = strings.TrimSpace(parts[len(parts)-1])
	datasetID = strings.TrimSpace(strings.Join(parts[2:len(parts)-1], ":"))
	return producer, datasetID, freq, producer != "" && datasetID != "" && freq != ""
}

func findDatasetAlertFact(checkID string, datasets []observability.DatasetFrequencyStatus) (observability.DatasetFrequencyStatus, bool) {
	producer, datasetID, freq, ok := datasetCheckParts(checkID)
	if !ok {
		return observability.DatasetFrequencyStatus{}, false
	}
	for _, item := range datasets {
		if item.Producer == producer && item.DatasetID == datasetID && sameFrequency(item.Freq, freq) {
			return item, true
		}
	}
	return observability.DatasetFrequencyStatus{}, false
}

func sameFrequency(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func datasetAlertTitle(checkID string, item observability.DatasetFrequencyStatus) string {
	producer, _, _, ok := datasetCheckParts(checkID)
	if !ok {
		return alertTitle(checkID)
	}
	name := map[string]string{"factor": "因子计算任务"}[producer]
	if producer == "storage" {
		name = datasetResultTitle(item.DatasetID, false)
	} else if producer == "storage_view" {
		name = datasetResultTitle(item.DatasetID, true)
	}
	if name == "" {
		name = "数据处理"
	}
	return name + " · " + item.DatasetID + " / " + item.Freq
}

func datasetResultTitle(datasetID string, view bool) string {
	if strings.Contains(strings.ToLower(datasetID), "factor") {
		if view {
			return "因子结果视图"
		}
		return "因子结果数据"
	}
	if view {
		return "行情结果视图"
	}
	return "数据结果"
}

func datasetAlertReason(item observability.DatasetFrequencyStatus, now time.Time) string {
	prefix := "数据集 " + item.DatasetID + "（" + item.Freq + "）"
	if item.Reason == "尚未上报" || item.LastRunAt.IsZero() {
		return prefix + " 尚未收到运行上报，请检查任务是否启动及消费队列是否正常"
	}
	if item.Reason == "producer stale" {
		return prefix + " 的生产端监控上报已中断，最近上报 " + formatHealthTime(item.LastReportedAt)
	}
	if strings.Contains(item.Reason, "输出水位已落后") {
		return prefix + " " + item.Reason
	}
	if item.Reason == "run stale" || item.Reason == "success stale" {
		reason := prefix + " 已超过允许时间未更新"
		if !item.LastRunAt.IsZero() {
			reason += "；最近运行 " + formatHealthTime(item.LastRunAt)
		}
		if !item.LastSuccessAt.IsZero() {
			reason += "；最近成功 " + formatHealthTime(item.LastSuccessAt)
		}
		if !item.OutputWatermarkAt.IsZero() {
			reason += "；最新输出 " + formatHealthTime(item.OutputWatermarkAt)
		}
		if item.LagSeconds > 0 {
			reason += "；当前落后 " + formatDatasetLag(item.LagSeconds)
		}
		if !now.IsZero() {
			reason += "；检查时间 " + formatHealthTime(now)
		}
		return reason
	}
	if translated := ChineseReason(item.Reason); translated != "" && translated != item.Reason {
		return prefix + " " + translated
	}
	return prefix + " " + ChineseReason(item.Reason)
}

func formatHealthTime(value time.Time) string {
	if value.IsZero() {
		return "未知"
	}
	return value.UTC().Format("2006-01-02 15:04:05 UTC")
}

func formatDatasetLag(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", maxInt64(0, seconds))
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

func maxInt64(value, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}

func serviceName(raw string) (string, string) {
	id := strings.ToLower(raw)
	switch {
	case strings.Contains(id, "storage"):
		return "数据存储", "保存原始数据并维护查询视图"
	case strings.Contains(id, "eventbus"):
		return "事件总线", "在采集、存储和计算模块之间传递任务与数据"
	case strings.Contains(id, "gateway"):
		return "服务网关", "为管理页面和内部服务提供统一访问入口"
	case strings.Contains(id, "cloud"):
		return "云端执行", "管理 SCF 节点和远程采集任务"
	case strings.Contains(id, "monitor"):
		return "系统监控", "汇总服务、主机和业务健康状态"
	case strings.Contains(id, "collector"):
		return "行情采集", "负责获取和写入行情数据"
	case strings.Contains(id, "factor"):
		return "因子计算", "负责计算并写入因子结果"
	case strings.Contains(id, "trade"):
		return "交易管理", "负责账户和交易状态同步"
	case strings.Contains(id, "web_host") || strings.Contains(id, "web-host") || strings.Contains(id, "webhost"):
		return "管理台", "提供浏览器页面和管理接口"
	case strings.Contains(id, "strategy"):
		return "策略管理", "负责策略配置和运行状态"
	case strings.Contains(id, "archive"):
		return "数据归档", "负责历史数据归档和保留"
	case strings.Contains(id, "hostagent") || strings.Contains(id, "host-agent"):
		return "主机监控", "采集主机在线状态、CPU、内存和磁盘"
	default:
		return "核心服务", "检查服务是否在线并持续上报"
	}
}

func latest(values ...time.Time) time.Time {
	var out time.Time
	for _, value := range values {
		if value.After(out) {
			out = value
		}
	}
	return out
}
