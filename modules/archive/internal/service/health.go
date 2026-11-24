package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
)

const (
	HealthOK      = "ok"
	HealthWarning = "warning"
	HealthError   = "error"
	HealthPaused  = "paused"

	// workerOfflineLimit 是有到期运行时 worker 离线多久之后算异常。
	workerOfflineLimit = 30 * time.Minute
	// followStaleAfter 是最近一次成功的跟随（follow 或 manual）距今多久算需关注：覆盖 worker 卡住、follow 连续失败、名额长期被占。
	followStaleAfter = 30 * time.Minute
	// exportOverdueAfter 是分区到时间（收盘 + 允许迟到）之后多久仍未导出算需关注。
	exportOverdueAfter = time.Hour
)

// healthInput 是计算任务健康等级需要的全部上下文。
type healthInput struct {
	Now  time.Time
	Task store.Task
	// LastRun 是最近一次运行（任何种类）；LastFollow 是最近一次成功的 follow 或 manual（消息里带整段重读进度）。
	LastRun    *store.Run
	LastFollow *store.Run
	OpenParts  []store.Partition
	// OldestQueued 是最早一条排队中运行的计划时刻，没有则为零值。
	OldestQueued time.Time
	// WorkerLastSeen 是 worker 最近一次心跳的时间，从未心跳时为零值。
	WorkerLastSeen time.Time
	// Lateness 是允许迟到：分区收盘后再等这么久才导出。
	Lateness        time.Duration
	SpaceTZ         string
	SpaceLocalClose string
	// RemoteEnabled：worker 报告启用了对象存储，远端副本规则才生效；Remote 是该任务未复制分区的汇总。
	RemoteEnabled bool
	Remote        store.RemoteBacklog
}

// 远端副本规则：最早一个未复制分区（按当前版本的导出时间）超过 24 小时需关注，超过 3 天异常。
const (
	remoteWarnAfter  = 24 * time.Hour
	remoteErrorAfter = 72 * time.Hour
)

// evaluateHealth 计算任务健康等级，返回等级与原因（多条用分号分隔）。
func evaluateHealth(in healthInput) (string, string) {
	if in.Task.Status == store.StatusPaused {
		return HealthPaused, "任务已停用"
	}
	var errs, warns []string
	// 异常：最近一次成功运行距今超过「线上保留期 − 2 天」。
	limit, forever, retentionErr := parseRetention(in.Task.Retention)
	if retentionErr != nil {
		errs = append(errs, "没有读到数据集的线上保留期，无法判断是否会漏数据")
	} else if !forever {
		threshold := limit - 48*time.Hour
		if threshold < limit/2 {
			threshold = limit / 2
		}
		reference := in.Task.LastSuccessAt
		label := "最近一次成功运行"
		if reference.IsZero() {
			reference, label = in.Task.CreatedAt, "任务创建后一直没有成功运行；创建"
		}
		if !reference.IsZero() && in.Now.Sub(reference) > threshold {
			errs = append(errs, fmt.Sprintf("%s距今 %s，已接近线上保留期 %s", label, in.Now.Sub(reference).Truncate(time.Minute), limit))
		}
	}
	if in.Task.BlockedReason != "" {
		errs = append(errs, "分区登记被拒："+in.Task.BlockedReason)
	}
	if len(in.Task.Unrecoverable) > 0 {
		errs = append(errs, fmt.Sprintf("有 %d 段数据已超出线上保留期、无法补齐", len(in.Task.Unrecoverable)))
	}
	if !in.OldestQueued.IsZero() {
		offlineSince := in.WorkerLastSeen
		if offlineSince.IsZero() || in.OldestQueued.After(offlineSince) {
			offlineSince = in.OldestQueued
		}
		if in.Now.Sub(offlineSince) > workerOfflineLimit {
			errs = append(errs, fmt.Sprintf("有到期的运行等待，worker 已离线超过 %s", workerOfflineLimit))
		}
	}
	remoteErr, remoteWarn := remoteReason(in)
	if remoteErr != "" {
		errs = append(errs, remoteErr)
	}
	// 需关注。
	if remoteWarn != "" {
		warns = append(warns, remoteWarn)
	}
	// follow 每分钟一条：失败的补拉区间、导入会被下一次成功的 follow 盖住（这两种由用户发起，当场能看到结果）。
	if r := in.LastRun; r != nil && r.Status == store.RunFailed {
		reason := "最近一次运行失败"
		if r.Message != "" {
			reason += "：" + r.Message
		}
		warns = append(warns, reason)
	}
	reference := in.Task.LastSuccessAt
	if reference.IsZero() {
		reference = in.Task.CreatedAt
	}
	if !reference.IsZero() && in.Now.Sub(reference) > followStaleAfter {
		warns = append(warns, fmt.Sprintf("最近一次成功的跟随距今超过 %s", followStaleAfter))
	}
	if overdue := overduePartitions(in); overdue > 0 {
		warns = append(warns, fmt.Sprintf("有 %d 个分区到时间超过 1 小时仍未导出", overdue))
	}
	switch {
	case len(errs) > 0:
		return HealthError, strings.Join(append(errs, warns...), "；")
	case len(warns) > 0:
		return HealthWarning, strings.Join(warns, "；")
	default:
		return HealthOK, ""
	}
}

// remoteReason 按远端副本规则返回异常或需关注的原因（至多一条非空）。
func remoteReason(in healthInput) (errReason, warnReason string) {
	b := in.Remote
	if !in.RemoteEnabled || b.Unreplicated == 0 || b.OldestExportedAt.IsZero() {
		return "", ""
	}
	age := in.Now.Sub(b.OldestExportedAt)
	if age <= remoteWarnAfter {
		return "", ""
	}
	window := "24 小时"
	if age > remoteErrorAfter {
		window = "3 天"
	}
	reason := fmt.Sprintf("共 %d 个分区还没有已校验的远端副本，最早的 %s 导出已超过 %s", b.Unreplicated, b.OldestKey, window)
	if b.LastError != "" {
		reason += "；最近错误：" + b.LastError
	}
	if age > remoteErrorAfter {
		return reason, ""
	}
	return "", reason
}

// overduePartitions 数到时间（收盘 + 允许迟到）超过 1 小时仍未导出的分区；整段重读进行中不判，
// 那时导出本来就只到重读进度为止。
func overduePartitions(in healthInput) int {
	if len(in.OpenParts) == 0 || (in.LastFollow != nil && strings.Contains(in.LastFollow.Message, schedule.RescanProgressLabel)) {
		return 0
	}
	loc, err := time.LoadLocation(in.SpaceTZ)
	if err != nil {
		loc = time.UTC
	}
	count := 0
	for _, p := range in.OpenParts {
		closeAt, err := schedule.PartitionClose(in.Task.PartitionUnit, loc, in.SpaceLocalClose, in.Task.Freq, p.RangeStart, p.RangeEnd)
		if err != nil {
			continue
		}
		if in.Now.After(closeAt.Add(in.Lateness).Add(exportOverdueAfter)) {
			count++
		}
	}
	return count
}
