package hostmetrics

import (
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
)

// DefaultHostStaleAfter 是采集器的停报时限：HostAgent 每分钟上报一次，连续三次没收到就算停报。
const DefaultHostStaleAfter = 3 * time.Minute

// 主机类检查的种类。主机的在线情形和资源使用率都是普通检查，check_id 为 host:<host_id>:<种类>，
// 和其他检查走同一条评估、记状态、推送、展示的路径。
const (
	// CheckKindPresence 是主机失联：采集器停报，主机网关心跳也不正常。
	CheckKindPresence = "presence"
	// CheckKindAgent 是只有 host-agent 停止上报：采集器停报，但主机网关心跳正常，主机仍在线。
	CheckKindAgent = "agent"
	// CheckKindDiskForecast 是磁盘写满预测：按近 7 天主机快照的日增长率中位数估算耗尽安全余量的剩余天数。
	CheckKindDiskForecast = "disk_forecast"

	HostMetricCPU             = "cpu"
	HostMetricMemory          = "memory"
	HostMetricFilesystemUsage = "filesystem_usage"
)

const checkPrefix = "host:"

// CheckID 返回主机类检查的 check_id。
func CheckID(hostID, kind string) string {
	return checkPrefix + strings.TrimSpace(hostID) + ":" + kind
}

// ParseCheckID 拆出主机类检查的主机 ID 与种类；不是主机类检查时 ok 为 false。
func ParseCheckID(checkID string) (hostID, kind string, ok bool) {
	parts := strings.Split(checkID, ":")
	if len(parts) != 3 || parts[0]+":" != checkPrefix || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	switch parts[2] {
	case CheckKindPresence, CheckKindAgent, CheckKindDiskForecast, HostMetricCPU, HostMetricMemory, HostMetricFilesystemUsage:
		return parts[1], parts[2], true
	}
	return "", "", false
}

// Threshold 是一项主机资源检查的参数（百分比）：使用率不低于 Alert 算超限，连续 Samples 次采样超限才告警；
// 告警后要回落到 Recovery 及以下才算恢复，免得在告警线附近来回抖动。
type Threshold struct {
	Alert, Recovery float64
	Samples         int
}

// HostAgent 每分钟采样一次（整分 0 秒）：CPU 3 次约 3 分钟、内存 5 次约 5 分钟、磁盘 3 次约 3 分钟。
var metricThresholds = map[string]Threshold{
	// CPU 取 80%、连续约 3 分钟：2026-10-14 两个 1h 因子任务回填，Storage 吃掉 4 核主机的
	// 330%–380% CPU（整机 82%–95%），读窗口一再超时，却没有一次连续 5 分钟都不低于 90%，原来的规则一条告警也没出。
	HostMetricCPU:    {Alert: 80, Recovery: 70, Samples: 3},
	HostMetricMemory: {Alert: 90, Recovery: 80, Samples: 5},
	// 磁盘写满是 Storage / View 故障的首要原因：在写满之前尽早告警，留出清理时间。
	HostMetricFilesystemUsage: {Alert: 85, Recovery: 80, Samples: 3},
}

// Metrics 是有阈值检查的主机指标，顺序固定。
var Metrics = []string{HostMetricCPU, HostMetricMemory, HostMetricFilesystemUsage}

// MetricThreshold 返回主机指标的检查参数；不是有阈值的指标时返回零值。
func MetricThreshold(metric string) Threshold { return metricThresholds[metric] }

var metricLabels = map[string]string{
	HostMetricCPU:             "CPU 使用率",
	HostMetricMemory:          "内存使用率",
	HostMetricFilesystemUsage: "磁盘空间使用率",
}

// MetricLabel 返回主机指标的中文名称；未知指标返回空字符串。
func MetricLabel(metric string) string { return metricLabels[metric] }

// maxProcesses 是从一份快照里最多取用的进程数；HostAgent 最多上报 10 个，这里只防异常数据。
const maxProcesses = 20

// TopProcesses 取出快照里的进程排行。排行只是辅助信息：不合理的条目（没有名字、CPU 不是有限的非负数）就地忽略，
// 不因此拒收整份主机快照。
func TopProcesses(snapshot *hostmetricpb.HostSnapshot) []alerttext.Process {
	items := make([]alerttext.Process, 0, len(snapshot.GetTopProcesses()))
	for _, process := range snapshot.GetTopProcesses() {
		name := strings.TrimSpace(process.GetName())
		if name == "" || len(items) == maxProcesses {
			continue
		}
		item := alerttext.Process{PID: process.GetPid(), Name: name, RSSBytes: process.GetRssBytes()}
		if cpu := process.GetCpuPercent(); process.GetCpuAvailable() && cpu >= 0 && !math.IsInf(cpu, 0) && !math.IsNaN(cpu) {
			item.CPUPercent, item.CPUAvailable = cpu, true
		}
		items = append(items, item)
	}
	return items
}
