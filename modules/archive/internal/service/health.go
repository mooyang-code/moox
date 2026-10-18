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
	Now       time.Time
	Task      store.Task
	LastRun   *store.Run
	OpenParts []store.Partition
	// OldestQueued 是最早一条排队中运行的计划时刻，没有则为零值。
	OldestQueued time.Time
	// WorkerOfflineSince 是活动 worker 离线的起点（最近一次心跳或从未心跳时的零值）；有活动 worker 时为 Active。
	WorkerActive    bool
	WorkerLastSeen  time.Time
	Lookback        time.Duration
	SpaceTZ         string
	SpaceLocalClose string
}

// evaluateHealth 按设计第 13 节计算任务健康等级，返回等级与原因（多条用分号分隔）。
func evaluateHealth(in healthInput) (string, string) {
	if in.Task.Status == store.StatusPaused {
		return HealthPaused, "任务已停用"
	}
	var errs, warns []string
	// 异常：最近一次成功运行距今超过「线上保留期 − 2 天」。
	if limit, forever, err := parseRetention(in.Task.Retention); err == nil && !forever {
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
	if in.LastRun != nil && len(in.LastRun.Unrecoverable) > 0 {
		errs = append(errs, fmt.Sprintf("有 %d 段数据已超出线上保留期、无法补齐", len(in.LastRun.Unrecoverable)))
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
	if in.LastRun != nil && in.LastRun.Status == store.RunFailed {
		reason := "最近一次运行失败"
		if in.LastRun.Message != "" {
			reason += "：" + in.LastRun.Message
		}
		warns = append(warns, reason)
	}
	if in.Task.ConsecutiveSkips >= 3 {
		warns = append(warns, fmt.Sprintf("已连续跳过 %d 次", in.Task.ConsecutiveSkips))
	}
	interval := time.Duration(in.Task.IntervalSeconds) * time.Second
	if in.LastRun != nil && !in.LastRun.StartedAt.IsZero() && !in.LastRun.FinishedAt.IsZero() && interval > 0 {
		if took := in.LastRun.FinishedAt.Sub(in.LastRun.StartedAt); took > interval/2 {
			warns = append(warns, fmt.Sprintf("最近一次运行耗时 %s，超过执行间隔的一半", took.Truncate(time.Second)))
		}
	}
	if overdue := overduePartitions(in); overdue > 0 {
		warns = append(warns, fmt.Sprintf("有 %d 个分区到时间超过 2 个执行间隔仍未导出", overdue))
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
