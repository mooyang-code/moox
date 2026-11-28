package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/checks"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"trpc.group/trpc-go/trpc-go/log"
)

// hostChecks 把主机事实转换成检查事实：主机失联、只有 host-agent 停止上报、CPU / 内存 / 磁盘空间超过阈值，以及磁盘写满预测。
// 这些事实和业务检查进同一张检查表、由同一套告警推进，页面也从同一份告警状态展示，
// 所以同一件事在页面和推送里是同一个标题、同一个级别、同一段原因。
type hostChecks struct {
	// evaluated 记录每台主机已经评估过的最近一次采样时间（键为主机 ID）。HostAgent 每分钟采样一次，评估每 30 秒一轮：
	// 同一次采样只算一次，“连续 N 次采样超限才告警”才成立；本轮写入成功后才更新。重启后清空，最多把重启前最后一次采样多算一次。
	evaluated map[string]time.Time
}

func newHostChecks() *hostChecks { return &hostChecks{evaluated: make(map[string]time.Time)} }

// hostTitle 是主机在告警标题里的称呼：登记的主机 ID，与页面主机卡片一致。
func hostTitle(host monitorobservability.HostStatus) string { return host.HostID }

// hostCheckTimeout 是主机类检查一轮的时限。它只读写 Monitor 自己的库，不沿用检查定时器那一轮剩下的时间。
const hostCheckTimeout = 10 * time.Second

// buildHostCheckReporter 返回每轮评估最先执行的主机类检查。它独立于业务评估：只需要采集器登记、部署拓扑和主机网关的组件上报，
// 都在本地库里，所以业务评估读 Storage 超时或整体失败时，主机失联、host-agent 停报与资源告警照常产出。
// 观测输入中断时整轮冻结：不推进，既不新增告警，也不恢复已有告警（失联判断要用经 EventBus 的网关上报）。
// 主机从登记里移除或停用后，它的检查不再产出，当轮删除（在告警的推一条恢复）。
func buildHostCheckReporter(
	builder *monitorobservability.Builder,
	applier *checks.Applier,
	observation func(now time.Time) observationState,
) func(context.Context) error {
	if builder == nil || applier == nil {
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
		rows, err := applier.Load(ctx, checks.DomainHost)
		if err != nil {
			return err
		}
		round, sampled := hosts.round(ctx, facts, rows, builder.Hosts)
		if err := applier.Apply(ctx, round); err != nil {
			return err
		}
		// 写进去之后才把采样记为已评估：写失败的采样下一轮重新评估，不会被吃掉。
		for hostID, at := range sampled {
			hosts.evaluated[hostID] = at
		}
		return nil
	}
}

// round 返回本轮的主机类检查（事实与冻结集合），以及本轮评估了新采样的主机及其采样时间。
// 资源与磁盘写满预测检查在没有新采样、主机失联或指标不可用时不产生事实、保持原状态，既不累计失败也不恢复。
// 只有上报过、且主机与 host-agent 部署都没有停用的登记主机才有主机类检查。rows 是已有的主机类检查行（判断是否在告警）。
func (h *hostChecks) round(ctx context.Context, overview monitorobservability.Overview, rows map[store.CheckKey]domain.Check, hostStore *hostmetrics.Store) (checks.Round, map[string]time.Time) {
	round := checks.Round{Domain: checks.DomainHost}
	sampled := make(map[string]time.Time)
	selected := make(map[string]bool)
	for _, presence := range overview.HostPresences() {
		if presence.Disabled {
			continue
		}
		selected[presence.Host.HostID] = true
		title := "主机 " + hostTitle(presence.Host)
		lost := checks.Fact{
			SpaceID: events.SystemSpaceID, CheckID: hostmetrics.CheckID(presence.Host.HostID, hostmetrics.CheckKindPresence),
			Name: title + " · 失联", Status: domain.CheckStatusOK, Reason: "主机在线",
		}
		agent := checks.Fact{
			SpaceID: events.SystemSpaceID, CheckID: hostmetrics.CheckID(presence.Host.HostID, hostmetrics.CheckKindAgent),
			Name: title + " · host-agent 停止上报", Status: domain.CheckStatusOK, Reason: "host-agent 正常上报",
		}
		switch presence.State {
		case monitorobservability.HostLost:
			lost.Status, lost.Reason = domain.CheckStatusDown, presence.Reason
			agent.Reason = "主机已判为失联，改由“" + lost.Name + "”告警跟进"
		case monitorobservability.HostAgentStopped:
			agent.Status, agent.Reason = domain.CheckStatusDown, presence.Reason
			lost.Reason = "主机网关心跳正常，主机在线；host-agent 仍未上报，由“" + agent.Name + "”告警跟进"
		}
		round.Facts = append(round.Facts, lost, agent)
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
			key := store.CheckKey{SpaceID: events.SystemSpaceID, CheckID: hostmetrics.CheckID(host.HostID, metric)}
			current := values[metric]
			if !fresh || !current.available {
				round.Freeze(key)
				continue
			}
			round.Facts = append(round.Facts, hostMetricFact(host, metric, current.value, rows[key].Firing()))
		}
		forecastKey := store.CheckKey{SpaceID: events.SystemSpaceID, CheckID: hostmetrics.CheckID(host.HostID, hostmetrics.CheckKindDiskForecast)}
		if !fresh || !host.DiskAvailable || hostStore == nil {
			round.Freeze(forecastKey)
		} else {
			history, err := hostStore.DailyLatestHistory(ctx, host.HostID, overview.GeneratedAt.Add(-hostmetrics.DiskForecastWindow), overview.GeneratedAt)
			if err != nil {
				log.WarnContextf(ctx, "读取主机 %s 的磁盘历史样本失败，本轮冻结磁盘写满预测检查：%v", host.HostID, err)
				round.Freeze(forecastKey)
			} else {
				round.Facts = append(round.Facts, hostDiskForecastFact(host, hostmetrics.ForecastDisks(history, overview.GeneratedAt), rows[forecastKey].Firing()))
			}
		}
		if fresh {
			sampled[host.HostID] = host.LastSeenAt
		}
	}
	return round, sampled
}

// hostDiskForecastFact 把 hostmetrics.ForecastDisks 的逐挂载点预测结果收成一条主机检查：
// 任意挂载点预计在 DiskForecastAlertDays（7 天）内耗尽安全余量（10% 且不少于 5 GiB）即判失败告警；
// 已在告警中的检查需回落到超过 DiskForecastRecoveryDays（10 天）或停止增长才恢复；历史样本不足 3 个自然日间隔时按正常记录、不告警。
func hostDiskForecastFact(host monitorobservability.HostStatus, forecasts []hostmetrics.DiskForecast, firing bool) checks.Fact {
	fact := checks.Fact{
		SpaceID: events.SystemSpaceID,
		CheckID: hostmetrics.CheckID(host.HostID, hostmetrics.CheckKindDiskForecast),
		Name:    "主机 " + hostTitle(host) + " · 磁盘写满预测",
		Status:  domain.CheckStatusOK,
	}
	var alerting []hostmetrics.DiskForecast
	var tightest *hostmetrics.DiskForecast
	evaluated := 0
	for i := range forecasts {
		f := forecasts[i]
		if f.Status == "UNKNOWN" {
			continue
		}
		evaluated++
		if f.GrowthBytesPerDay <= 0 {
			continue
		}
		if tightest == nil || f.RemainingDays < tightest.RemainingDays || (f.RemainingDays == tightest.RemainingDays && f.Mountpoint < tightest.Mountpoint) {
			copied := f
			tightest = &copied
		}
		if f.RemainingDays <= hostmetrics.DiskForecastAlertDays || (firing && f.RemainingDays <= hostmetrics.DiskForecastRecoveryDays) {
			alerting = append(alerting, f)
		}
	}
	if len(alerting) > 0 {
		sort.Slice(alerting, func(i, j int) bool {
			if alerting[i].RemainingDays != alerting[j].RemainingDays {
				return alerting[i].RemainingDays < alerting[j].RemainingDays
			}
			return alerting[i].Mountpoint < alerting[j].Mountpoint
		})
		reasons := make([]string, 0, len(alerting))
		for _, f := range alerting {
			rate := formatBytesPerDay(f.GrowthBytesPerDay)
			if f.RemainingDays <= hostmetrics.DiskForecastAlertDays {
				reasons = append(reasons, fmt.Sprintf("%s 预计 %.1f 天后耗尽安全余量（当前日增 %s，阈值 %d 天）",
					f.Mountpoint, f.RemainingDays, rate, hostmetrics.DiskForecastAlertDays))
			} else {
				reasons = append(reasons, fmt.Sprintf("%s 预计 %.1f 天后耗尽安全余量，还没有回落到恢复线 %d 天（当前日增 %s）",
					f.Mountpoint, f.RemainingDays, hostmetrics.DiskForecastRecoveryDays, rate))
			}
		}
		fact.Status = domain.CheckStatusDown
		fact.Reason = strings.Join(reasons, "；")
		return fact
	}
	if evaluated == 0 {
		fact.Reason = "磁盘历史样本不足（需至少 3 个自然日间隔），暂不预测"
		return fact
	}
	if tightest != nil {
		fact.Reason = fmt.Sprintf("磁盘增长正常（最紧 %s 预计 %.1f 天后耗尽安全余量，当前日增 %s）",
			tightest.Mountpoint, tightest.RemainingDays, formatBytesPerDay(tightest.GrowthBytesPerDay))
		return fact
	}
	fact.Reason = "磁盘占用当前没有持续增长"
	return fact
}

func formatBytesPerDay(bytesPerDay float64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case bytesPerDay >= gib:
		return fmt.Sprintf("%.1f GiB/天", bytesPerDay/gib)
	case bytesPerDay >= mib:
		return fmt.Sprintf("%.1f MiB/天", bytesPerDay/mib)
	case bytesPerDay >= kib:
		return fmt.Sprintf("%.1f KiB/天", bytesPerDay/kib)
	default:
		return fmt.Sprintf("%.0f B/天", bytesPerDay)
	}
}

// hostMetricFact 判断一次采样的资源使用率。达到告警线算超限；已经在告警的（firing），要回落到恢复线及以下才算恢复。
func hostMetricFact(host monitorobservability.HostStatus, metric string, value float64, firing bool) checks.Fact {
	threshold, label := hostmetrics.MetricThreshold(metric), hostmetrics.MetricLabel(metric)
	percent := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "%" }
	fact := checks.Fact{
		SpaceID: events.SystemSpaceID, CheckID: hostmetrics.CheckID(host.HostID, metric), Name: "主机 " + hostTitle(host) + " · " + label,
		Status: domain.CheckStatusOK, Reason: fmt.Sprintf("%s %.1f%%", label, value),
	}
	switch {
	case value >= threshold.Alert:
		fact.Status, fact.Reason = domain.CheckStatusDown, fmt.Sprintf("%s %.1f%%，超过阈值 %s", label, value, percent(threshold.Alert))
	case value > threshold.Recovery && firing:
		fact.Status, fact.Reason = domain.CheckStatusDown, fmt.Sprintf("%s %.1f%%，还没有回落到恢复线 %s", label, value, percent(threshold.Recovery))
	}
	// CPU、内存超限时带上这次采样的进程排行；磁盘空间看不出是哪个进程占的，不带。
	if !fact.Passed() && metric != hostmetrics.HostMetricFilesystemUsage {
		fact.Diagnostic = alerttext.ProcessDetail{Metric: metric, TopProcesses: host.Processes}.Encode()
	}
	return fact
}
