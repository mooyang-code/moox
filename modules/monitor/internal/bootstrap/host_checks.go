package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"gorm.io/gorm"
)

// hostChecks 把主机事实转换成普通的检查结果：主机失联、只有 host-agent 停止上报、CPU / 内存 / 磁盘空间超过阈值。
// 这些结果和业务检查写入同一张检查结果表，由同一个告警评估器记状态、推送，页面也从同一份告警状态展示，
// 所以同一件事在页面和推送里是同一个标题、同一个级别、同一段原因。
type hostChecks struct {
	// evaluated 记录每台主机已经评估过的最近一次采样时间（键为主机 ID）。HostAgent 每分钟采样一次，评估每 30 秒一轮：
	// 同一次采样只算一次，“连续 N 次采样超限才告警”才成立；结果写入成功后才更新。重启后清空，最多把重启前最后一次采样多算一次。
	evaluated map[string]time.Time
}

func newHostChecks() *hostChecks { return &hostChecks{evaluated: make(map[string]time.Time)} }

// hostCheckFailureThreshold 返回主机类检查告警所需的连续失败次数；不是主机类检查时 ok 为 false。
// 失联与 host-agent 停报在采集器停报满 3 分钟后才产生失败结果，这 3 分钟就是它们的宽限，失败一次即告警；
// 资源检查每次采样产生一个结果，按指标要求的连续采样次数告警。恢复都只要成功一次。
func hostCheckFailureThreshold(checkID string) (int, bool) {
	_, kind, ok := hostmetrics.ParseCheckID(checkID)
	if !ok {
		return 0, false
	}
	if samples := hostmetrics.MetricThreshold(kind).Samples; samples > 0 {
		return samples, true
	}
	return 1, true
}

// hostTitle 是主机在告警标题里的称呼：登记的主机 ID，与页面主机卡片一致。
func hostTitle(host monitorobservability.HostStatus) string { return host.HostID }

// hostCheckTimeout 是主机类检查一轮的时限。它只读写 Monitor 自己的库，不沿用检查定时器那一轮剩下的时间。
const hostCheckTimeout = 10 * time.Second

// buildHostCheckReporter 返回每轮评估最先执行的主机类检查。它独立于业务评估：只需要采集器登记、部署拓扑和网关信号，
// 都在本地库里，所以业务评估读 Storage 超时或整体失败时，主机失联、host-agent 停报与资源告警照常产出。
// 观测输入中断时整轮冻结：不写结果，既不新增告警，也不恢复已有告警。
func buildHostCheckReporter(
	builder *monitorobservability.Builder,
	repositories *store.Repositories,
	hook func(context.Context, domain.Check, domain.CheckResult),
	observation func(now time.Time) observationState,
) func(context.Context) error {
	if builder == nil || repositories == nil {
		return nil
	}
	hosts := newHostChecks()
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hostCheckTimeout)
		defer cancel()
		facts, err := builder.HostFacts(ctx)
		if err != nil {
			return err
		}
		if observation != nil && observation(facts.GeneratedAt).Suspended {
			return nil
		}
		list, frozen, sampled, err := hosts.items(ctx, facts, repositories.Alerts)
		if err != nil {
			return err
		}
		items := make(map[string]businessFreshnessItem, len(list))
		for _, item := range list {
			items[item.spaceID+"\x00"+item.checkID] = item
		}
		// 本轮既没有结果也不保持原状态的旧检查（主机从登记里移除或停用）按“不再检查”收尾，已有告警随之恢复。
		enabled := true
		for page := 1; ; page++ {
			batch, err := repositories.Checks.List(ctx, store.ListChecksOptions{
				Source: domain.CheckSourceObservability, Enabled: &enabled, Page: store.Page{Page: page, PageSize: 500},
			})
			if err != nil {
				return err
			}
			for _, check := range batch {
				key := check.SpaceID + "\x00" + check.CheckID
				if _, _, ok := hostmetrics.ParseCheckID(check.CheckID); !ok || frozen[key] {
					continue
				}
				if _, expected := items[key]; !expected {
					items[key] = businessFreshnessItem{spaceID: check.SpaceID, checkID: check.CheckID, name: check.Name, success: true, reason: noLongerExpected}
				}
			}
			if len(batch) < 500 {
				break
			}
		}
		written, err := writeCheckResults(ctx, repositories, items, 30, hook, func(ctx context.Context, checks []domain.Check) error {
			return ensureDefaultRules(ctx, repositories, checks)
		})
		// 结果写进去之后才把采样记为已评估：写失败的采样下一轮重新评估，不会被吃掉。
		for hostID, at := range sampled {
			done := true
			for _, metric := range hostmetrics.Metrics {
				key := events.SystemSpaceID + "\x00" + hostmetrics.CheckID(hostID, metric)
				if _, produced := items[key]; produced && !written[key] {
					done = false
				}
			}
			if done {
				hosts.evaluated[hostID] = at
			}
		}
		return err
	}
}

// items 返回本轮的主机类检查结果、本轮要保持原状态的检查（键为 空间\x00检查ID），以及本轮评估了新采样的主机及其采样时间。
// 资源检查在没有新采样、主机失联或指标不可用时不产生结果，既不累计失败也不恢复。
// 只有上报过、且主机与 host-agent 部署都没有停用的登记主机才有主机类检查；其余主机的旧检查由调用方按“不再检查”收尾。
func (h *hostChecks) items(ctx context.Context, overview monitorobservability.Overview, alerts *store.AlertRepository) ([]businessFreshnessItem, map[string]bool, map[string]time.Time, error) {
	var items []businessFreshnessItem
	frozen := make(map[string]bool)
	sampled := make(map[string]time.Time)
	selected := make(map[string]bool)
	for _, presence := range overview.HostPresences() {
		if presence.Disabled {
			continue
		}
		selected[presence.Host.HostID] = true
		title := "主机 " + hostTitle(presence.Host)
		lost := businessFreshnessItem{
			spaceID: events.SystemSpaceID, checkID: hostmetrics.CheckID(presence.Host.HostID, hostmetrics.CheckKindPresence),
			name: title + " · 失联", success: true, reason: "主机在线",
		}
		agent := businessFreshnessItem{
			spaceID: events.SystemSpaceID, checkID: hostmetrics.CheckID(presence.Host.HostID, hostmetrics.CheckKindAgent),
			name: title + " · host-agent 停止上报", success: true, reason: "host-agent 正常上报",
		}
		switch presence.State {
		case monitorobservability.HostLost:
			lost.success, lost.reason = false, presence.Reason
			agent.reason = "主机已判为失联，改由“" + lost.name + "”告警跟进"
		case monitorobservability.HostAgentStopped:
			agent.success, agent.reason = false, presence.Reason
			lost.reason = "主机网关心跳正常，主机在线；host-agent 仍未上报，由“" + agent.name + "”告警跟进"
		}
		items = append(items, lost, agent)
	}
	for _, host := range overview.Hosts {
		if !selected[host.HostID] {
			continue
		}
		fresh := host.Status == "healthy" && host.LastSeenAt.After(h.evaluated[host.HostID])
		values := map[string]struct {
			value     float64
			available bool
		}{
			hostmetrics.HostMetricCPU:             {host.CPUPercent, host.CPUAvailable},
			hostmetrics.HostMetricMemory:          {host.MemoryPercent, host.MemoryAvailable},
			hostmetrics.HostMetricFilesystemUsage: {host.FilesystemMaxPercent, host.DiskAvailable},
		}
		for _, metric := range hostmetrics.Metrics {
			checkID := hostmetrics.CheckID(host.HostID, metric)
			current := values[metric]
			if !fresh || !current.available {
				frozen[events.SystemSpaceID+"\x00"+checkID] = true
				continue
			}
			item, err := hostMetricItem(ctx, alerts, host, metric, current.value)
			if err != nil {
				return nil, nil, nil, err
			}
			items = append(items, item)
		}
		if fresh {
			sampled[host.HostID] = host.LastSeenAt
		}
	}
	return items, frozen, sampled, nil
}

// hostMetricItem 判断一次采样的资源使用率。达到告警线算超限；已经在告警的，要回落到恢复线及以下才算恢复。
func hostMetricItem(ctx context.Context, alerts *store.AlertRepository, host monitorobservability.HostStatus, metric string, value float64) (businessFreshnessItem, error) {
	threshold, label := hostmetrics.MetricThreshold(metric), hostmetrics.MetricLabel(metric)
	checkID := hostmetrics.CheckID(host.HostID, metric)
	percent := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "%" }
	item := businessFreshnessItem{
		spaceID: events.SystemSpaceID, checkID: checkID, name: "主机 " + hostTitle(host) + " · " + label,
		success: true, reason: fmt.Sprintf("%s %.1f%%", label, value),
	}
	switch {
	case value >= threshold.Alert:
		item.success, item.reason = false, fmt.Sprintf("%s %.1f%%，超过阈值 %s", label, value, percent(threshold.Alert))
	case value > threshold.Recovery:
		firing, err := alertFiring(ctx, alerts, checkID)
		if err != nil {
			return item, err
		}
		if firing {
			item.success, item.reason = false, fmt.Sprintf("%s %.1f%%，还没有回落到恢复线 %s", label, value, percent(threshold.Recovery))
		}
	}
	// CPU、内存超限时带上这次采样的进程排行；磁盘空间看不出是哪个进程占的，不带。
	if !item.success && metric != hostmetrics.HostMetricFilesystemUsage {
		item.diagnostic = alerttext.ProcessDetail{Metric: metric, TopProcesses: host.Processes}.Encode()
	}
	return item, nil
}

// alertFiring 报告某项检查的默认告警当前是否在告警中。
func alertFiring(ctx context.Context, alerts *store.AlertRepository, checkID string) (bool, error) {
	if alerts == nil {
		return false, nil
	}
	state, err := alerts.GetState(ctx, events.SystemSpaceID, "default:"+checkID, checkID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return state.Status == domain.AlertStatusFiring, nil
}
