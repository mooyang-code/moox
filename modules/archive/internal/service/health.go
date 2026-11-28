package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
)

const (
	HealthOK       = "ok"
	HealthWarning  = "warning"
	HealthError    = "error"
	HealthDisabled = "disabled"

	// workerOfflineLimit 是启用任务或有到期运行时 worker 离线多久之后算异常。
	workerOfflineLimit = 30 * time.Minute
	// followStaleAfter 是最近一次成功的跟随（或整段重读）距今多久算需关注：覆盖 worker 卡住、跟随连续失败、名额长期被占。
	followStaleAfter = 30 * time.Minute
	// exportOverdueAfter 是分区到时间（收盘 + 允许迟到）之后多久仍未导出算需关注。
	exportOverdueAfter = time.Hour
)

// healthInput 是计算任务健康等级需要的全部上下文。
type healthInput struct {
	Now  time.Time
	Task store.Task
	// Retention 是数据集当前的线上保留期（从 Storage 元数据内存缓存读取；读不到时为空）。
	Retention string
	// LastRun 是最近一次手动、补拉区间或导入运行。
	LastRun   *store.Run
	OpenParts []store.Partition
	// OldestQueued 是最早一条排队中运行（manual、range、import）的计划时刻，没有则为零值。
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
	if in.Task.Status == store.StatusDisabled {
		return HealthDisabled, "任务已停用"
	}
	sourceMissing := in.Task.SourceMissing
	var errs, warns []string
	if sourceMissing {
		errs = append(errs, "源数据集不存在")
	} else {
		// 异常：最近一次成功跟随距今超过「线上保留期 − 2 天」。
		limit, forever, retentionErr := parseRetention(in.Retention)
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
	}
	if in.Task.BlockedReason != "" {
		errs = append(errs, "分区登记被拒："+in.Task.BlockedReason)
	}
	if len(in.Task.Unrecoverable) > 0 {
		errs = append(errs, fmt.Sprintf("有 %d 段数据已超出线上保留期、无法补齐", len(in.Task.Unrecoverable)))
	}
	if reason := workerOfflineReason(in, sourceMissing); reason != "" {
		errs = append(errs, reason)
	}
	remoteErr, remoteWarn := remoteReason(in)
	if remoteErr != "" {
		errs = append(errs, remoteErr)
	}
	// 需关注。
	if remoteWarn != "" {
		warns = append(warns, remoteWarn)
	}
	if in.Task.LastFollowError != "" {
		warns = append(warns, "最近一次跟随失败："+in.Task.LastFollowError)
	}
	// 失败的补拉区间或导入会被之后的成功跟随覆盖（这两种由用户发起，当场能看到结果）。
	if r := in.LastRun; r != nil && r.Status == store.RunFailed && (in.Task.LastSuccessAt.IsZero() || r.FinishedAt.After(in.Task.LastSuccessAt)) {
		reason := "最近一次运行失败"
		if r.Message != "" {
			reason += "：" + r.Message
		}
		warns = append(warns, reason)
	}
	if !sourceMissing {
		reference := in.Task.LastSuccessAt
		if reference.IsZero() {
			reference = in.Task.CreatedAt
		}
		if !reference.IsZero() && in.Now.Sub(reference) > followStaleAfter {
			warns = append(warns, fmt.Sprintf("最近一次成功的跟随距今超过 %s", followStaleAfter))
		}
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

// workerOfflineReason 检查 worker 是否离线超过 workerOfflineLimit：
// 有排队中运行时按排队时刻与最近心跳较晚者算；启用且源数据集未缺失的任务每分钟都需要跟随，按任务创建时间与最近心跳较晚者算。
func workerOfflineReason(in healthInput, sourceMissing bool) string {
	if !in.OldestQueued.IsZero() {
		offlineSince := in.WorkerLastSeen
		if offlineSince.IsZero() || in.OldestQueued.After(offlineSince) {
			offlineSince = in.OldestQueued
		}
		if in.Now.Sub(offlineSince) > workerOfflineLimit {
			return fmt.Sprintf("有到期的运行等待，worker 已离线超过 %s", workerOfflineLimit)
		}
		return ""
	}
	if sourceMissing {
		return ""
	}
	offlineSince := in.WorkerLastSeen
	if offlineSince.IsZero() || (!in.Task.CreatedAt.IsZero() && in.Task.CreatedAt.After(offlineSince)) {
		offlineSince = in.Task.CreatedAt
	}
	if !offlineSince.IsZero() && in.Now.Sub(offlineSince) > workerOfflineLimit {
		return fmt.Sprintf("归档 worker 已离线超过 %s", workerOfflineLimit)
	}
	return ""
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
	if len(in.OpenParts) == 0 || in.Task.IsRescanning() {
		return 0
	}
	unit, err := schedule.PartitionUnit(in.Task.Freq)
	if err != nil {
		return 0
	}
	loc, err := time.LoadLocation(in.SpaceTZ)
	if err != nil {
		loc = time.UTC
	}
	count := 0
	for _, p := range in.OpenParts {
		closeAt, err := schedule.PartitionClose(unit, loc, in.SpaceLocalClose, in.Task.Freq, p.RangeStart, p.RangeEnd)
		if err != nil {
			continue
		}
		if in.Now.After(closeAt.Add(in.Lateness).Add(exportOverdueAfter)) {
			count++
		}
	}
	return count
}
