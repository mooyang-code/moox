package klinecheck

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// 暂停恢复后的追赶（设计 2.2）：对暂停区间 [since, 现在) 补采全部数据集。
// 追平的判定：区间内没有缺口（确认行情源缺失的周期已扣除，按「不可补」另外统计），并且没有未结束的补采作业。

// recoverySource 是恢复追赶作业的来源：库里 c_source 只允许 manual 与 auto，自动补采会跳过叠加 auto 作业，
// 恢复追赶用 manual，和自动补采互不影响。
const recoverySource = "manual"

// CatchupState 是一次追赶评估的结果。
type CatchupState struct {
	// Expected 是区间内应有的 K 线根数；Missing 是仍缺的根数；Unfillable 是确认行情源没有、不再补采的根数。
	Expected   int
	Missing    int
	Unfillable int
	// ActiveJobs 是未结束的补采作业数（含这次新提交的）。
	ActiveJobs int
	// JobID 是这次评估新提交的作业（没有提交为空）。
	JobID string
	// Truncated 为真表示缺口太多，检查结果被截断，不能判定追平。
	Truncated bool
}

// CaughtUp 报告暂停区间是否已补齐。
func (s CatchupState) CaughtUp() bool {
	return s.Missing == 0 && s.ActiveJobs == 0 && !s.Truncated
}

// Catchup 评估空间在 since 之后的补采进度：检查缺口，有缺口且没有未结束的作业时提交一个补采作业。
// 区间展开的分块太多时，把作业缩到最早的一个窗口，其余的后续评估再补。
func (b *Backfiller) Catchup(ctx context.Context, spaceID string, since time.Time) (CatchupState, error) {
	if b == nil || b.Checker == nil || b.Store == nil {
		return CatchupState{}, errors.New("backfiller is not initialized")
	}
	to := b.now()
	if !since.Before(to) {
		return CatchupState{}, nil
	}
	result, err := b.Checker.Check(ctx, Request{SpaceID: spaceID, From: since, To: to})
	if err != nil {
		return CatchupState{}, fmt.Errorf("检查暂停区间的缺口：%w", err)
	}
	active, err := b.Store.ActiveJobs(ctx, spaceID)
	if err != nil {
		return CatchupState{}, err
	}
	state := CatchupState{Expected: result.Expected, Missing: result.Missing, Unfillable: result.Absent, ActiveJobs: len(active), Truncated: result.Truncated}
	if result.Missing == 0 || len(active) > 0 {
		return state, nil
	}
	end := to
	for {
		jobID, _, err := b.submit(ctx, JobRequest{SpaceID: spaceID, From: since, To: end}, recoverySource, MaxChunksPerJob)
		if errors.Is(err, errTooManyChunks) && end.Sub(since) > autoScanWindow {
			// 缺口太多：缩短区间，只提交最早的一段，追赶是逐窗口推进的。
			end = since.Add(end.Sub(since) / 2)
			if end.Sub(since) < autoScanWindow {
				end = since.Add(autoScanWindow)
			}
			continue
		}
		if err != nil {
			return state, fmt.Errorf("提交暂停区间的补采作业：%w", err)
		}
		if jobID != "" {
			state.JobID, state.ActiveJobs = jobID, 1
		}
		return state, nil
	}
}

// Quiesce 等待已认领、正在执行的补采分块结束（暂停之后不再认领新的分块）。
func (b *Backfiller) Quiesce(ctx context.Context) error {
	done := make(chan struct{})
	go func() { b.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("补采分块仍在执行：%w", ctx.Err())
	}
}
