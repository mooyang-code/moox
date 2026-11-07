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

	// workerOfflineLimit 是有到期运行时活动 worker 离线多久之后算异常。
	workerOfflineLimit = 30 * time.Minute
)

// healthInput 是计算任务健康等级需要的全部上下文。
type healthInput struct {
	Now     time.Time
	Task    store.Task
	LastRun *store.Run
	// LastReview 是最近一次复核或手动运行；耗时规则只看它（到点拉取每分钟一条，耗时与复核间隔无关）。
	LastReview *store.Run
	OpenParts  []store.Partition
	// OldestQueued 是最早一条排队中运行的计划时刻，没有则为零值。
	OldestQueued time.Time
	// WorkerOfflineSince 是活动 worker 离线的起点（最近一次心跳或从未心跳时的零值）；有活动 worker 时为 Active。
	WorkerActive    bool
	WorkerLastSeen  time.Time
	Lookback        time.Duration
	SpaceTZ         string
	SpaceLocalClose string
}

// consecutiveSkipsWarn 是连续跳过多少次算需关注：每分钟检查一次，复核运行期间跳过几次是常态。
const consecutiveSkipsWarn = 10

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
	if !in.OldestQueued.IsZero() && !in.WorkerActive {
		offlineSince := in.WorkerLastSeen
		if offlineSince.IsZero() || in.OldestQueued.After(offlineSince) {
			offlineSince = in.OldestQueued
		}
		if in.Now.Sub(offlineSince) > workerOfflineLimit {
			errs = append(errs, fmt.Sprintf("有到期的运行等待，活动 worker 已离线超过 %s", workerOfflineLimit))
		}
	}
	// 需关注。
	// 复核失败单独看：到点拉取每分钟一条，一次成功的拉取会把失败的复核盖住。
	for _, r := range []struct {
		run   *store.Run
		label string
	}{{in.LastRun, "最近一次运行失败"}, {in.LastReview, "最近一次复核失败"}} {
		if r.run == nil || r.run.Status != store.RunFailed || (r.label == "最近一次复核失败" && in.LastRun != nil && in.LastRun.RunID == r.run.RunID) {
			continue
		}
		reason := r.label
		if r.run.Message != "" {
			reason += "：" + r.run.Message
		}
		warns = append(warns, reason)
	}
	if in.Task.ConsecutiveSkips >= consecutiveSkipsWarn {
		warns = append(warns, fmt.Sprintf("已连续跳过 %d 次", in.Task.ConsecutiveSkips))
	}
	interval := time.Duration(in.Task.IntervalSeconds) * time.Second
	if r := in.LastReview; r != nil && !r.StartedAt.IsZero() && !r.FinishedAt.IsZero() && interval > 0 {
		if took := r.FinishedAt.Sub(r.StartedAt); took > interval/2 {
			warns = append(warns, fmt.Sprintf("最近一次复核耗时 %s，超过复核间隔的一半", took.Truncate(time.Second)))
		}
	}
	if overdue := overduePartitions(in); overdue > 0 {
		warns = append(warns, fmt.Sprintf("有 %d 个分区到时间超过 2 个复核间隔仍未导出", overdue))
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

func overduePartitions(in healthInput) int {
	if len(in.OpenParts) == 0 {
		return 0
	}
	loc, err := time.LoadLocation(in.SpaceTZ)
	if err != nil {
		loc = time.UTC
	}
	interval := time.Duration(in.Task.IntervalSeconds) * time.Second
	count := 0
	for _, p := range in.OpenParts {
		closeAt, err := schedule.PartitionClose(in.Task.PartitionUnit, loc, in.SpaceLocalClose, in.Task.Freq, p.RangeStart, p.RangeEnd)
		if err != nil {
			continue
		}
		if in.Now.After(closeAt.Add(in.Lookback).Add(2 * interval)) {
			count++
		}
	}
	return count
}
