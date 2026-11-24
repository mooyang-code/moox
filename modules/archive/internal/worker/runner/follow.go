package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
)

const (
	// followBudget 是一轮读取（日志与整段重读共用）最多花的墙钟时间；用完就在当前单位（日志页或重读段）的边界停下，
	// 剩下的留给下一轮，免得一条运行长时间占着读 Storage 的名额。
	followBudget = time.Minute
	// quietWaitLimit：下一个允许读取的时刻比现在晚这么多以上，说明落在整点窗口里（秒窗口最多等 30 秒）。
	quietWaitLimit = 2 * time.Minute
	// rescanSegment 是低于 1d 的数据集整段重读一段的数据时间（不跨分区）；1d 及以上一段是一个分区。
	rescanSegment = time.Hour
)

// rescanOpenEnd 是整段重读最后一段的终点：最后一段不设上界，晚于重建基线时刻的行也一并读到，不依赖“数据时间不晚于写入时刻”。
var rescanOpenEnd = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// runFollow 执行 follow 与 manual 的一轮（T = 运行开始）：
//  1. 先导出、上传、定稿到期的分区（不读 Storage，休市时也靠它定稿）；
//  2. 整点窗口内不读，本轮到此结束；
//  3. 没有位点（首次运行、暂存库新建）或 manual 时重建基线（rebase）；
//  4. 从位点起按页读变更日志，每页按分区读行、写暂存区，写完才推进位点；任何一页断档就重建基线、本轮不再读日志；
//  5. 用剩余预算整段重读，每段写完推进重读进度；
//  6. 再导出、上传、定稿一次。
//
// 读取预算按本轮墙钟计（followBudget，且下一次读取不必等窗口）；第一页日志不受预算限制。
func (s *runState) runFollow(ctx context.Context, out *Outcome) error {
	s.roundStart = s.r.now()
	if s.a.GetKind() == kindManual {
		// manual 不论位点是否有效都重建基线：整段重读线上保留期，之后的 follow 分段读完。
		if err := s.rebase(ctx, out, false); err != nil {
			return err
		}
	}
	if err := s.settle(ctx, out); err != nil {
		return err
	}
	now := s.r.now()
	if s.r.Pace.AllowedAt(now).Sub(now) > quietWaitLimit {
		// 否则两个读 Storage 的名额会被等着读的运行占满 6 分钟，别的任务连导出与定稿都排不上。
		out.Message = s.followMessage(ctx, "整点窗口内只做导出与定稿")
		return nil
	}
	storeID, sequence, ok, err := s.r.Staging.ChangeCursor(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	if !ok {
		if err := s.rebase(ctx, out, false); err != nil {
			return err
		}
		if storeID, sequence, _, err = s.r.Staging.ChangeCursor(ctx, s.task.SpaceID, s.task.DatasetID); err != nil {
			return err
		}
	}
	if err := s.followChanges(ctx, out, source.ChangeCursor{StoreID: storeID, Sequence: sequence}); err != nil {
		return err
	}
	if err := s.rescan(ctx); err != nil {
		return err
	}
	if err := s.settle(ctx, out); err != nil {
		return err
	}
	out.Message = s.followMessage(ctx, "")
	return nil
}

// budgetLeft 报告本轮还能不能开始下一个单位：没超过 followBudget，且现在就能读（不必等秒窗口或整点窗口）。
func (s *runState) budgetLeft() bool {
	now := s.r.now()
	return now.Sub(s.roundStart) < followBudget && !s.r.Pace.AllowedAt(now).After(now)
}

// followChanges 从 cursor 起按页读变更日志：每页的范围按分区整理后读行、写暂存区，写完才把位点推进到这一页之后。
// 中途失败时位点停在上一页之后（写暂存区按主键比对替换，重做幂等）。任何一页断档时重建基线，本轮不再读日志。
func (s *runState) followChanges(ctx context.Context, out *Outcome, cursor source.ChangeCursor) error {
	spaceID, datasetID := s.task.SpaceID, s.task.DatasetID
	bound, err := s.lowerBound(ctx)
	if err != nil {
		return err
	}
	for first := true; first || s.budgetLeft(); first = false {
		if err := s.r.Pace.WaitTurn(ctx); err != nil {
			return err
		}
		page, err := s.r.Source.ReadChanges(ctx, spaceID, datasetID, &cursor)
		if err != nil {
			return err
		}
		s.progress.RequestCount++
		s.progress.BytesRead += uint64(page.Bytes)
		if err := s.r.Pace.Throttle(ctx, page.Bytes); err != nil {
			return err
		}
		if page.Gap {
			return s.rebase(ctx, out, true)
		}
		for _, read := range s.groupChanges(page.Spans, bound) {
			if err := s.readChunk(ctx, read.Start, read.End, read.Subjects); err != nil {
				return err
			}
		}
		if err := s.r.Staging.SetChangeCursor(ctx, spaceID, datasetID, page.Next.StoreID, page.Next.Sequence); err != nil {
			return err
		}
		cursor = page.Next
		if s.report != nil && s.report(s.progress) {
			return errStopped
		}
		if !page.HasMore {
			return nil
		}
	}
	return nil
}

// rescan 用本轮剩余的预算整段重读：从 rescan_from 起按段读，每段写完把 rescan_from 推进到段末；
// 到达 rescan_until 所在的那一段时终点取 rescanOpenEnd，读完两列清空。
func (s *runState) rescan(ctx context.Context) error {
	spaceID, datasetID := s.task.SpaceID, s.task.DatasetID
	from, until, ok, err := s.r.Staging.Rescan(ctx, spaceID, datasetID)
	if err != nil || !ok {
		return err
	}
	for s.budgetLeft() {
		end := s.segmentEnd(from)
		last := !end.Before(until)
		if last {
			end = rescanOpenEnd
		}
		if err := s.readChunk(ctx, from, end, nil); err != nil {
			return err
		}
		if err := s.r.Staging.AdvanceRescan(ctx, spaceID, datasetID, end, last); err != nil {
			return err
		}
		if last {
			return nil
		}
		from = end
	}
	return nil
}

// segmentEnd 返回从 from 起一段整段重读的终点：低于 1d 为下一个整点（最多 1 小时数据时间），且不跨分区；1d 及以上为分区结束。
func (s *runState) segmentEnd(from time.Time) time.Time {
	_, _, partEnd := schedule.PartitionOf(s.unit, s.loc, from)
	if s.unit == "month" {
		return partEnd
	}
	end := from.Truncate(rescanSegment).Add(rescanSegment)
	if end.After(partEnd) {
		end = partEnd
	}
	return end
}

// followMessage 是 follow、manual 成功时的消息：整段重读进行中带上进度（任务列表的最近运行据此显示首次运行的进度，
// mgr 的健康规则据此跳过“到期未导出”）。
func (s *runState) followMessage(ctx context.Context, lead string) string {
	var parts []string
	if lead != "" {
		parts = append(parts, lead)
	}
	if from, until, ok, err := s.r.Staging.Rescan(ctx, s.task.SpaceID, s.task.DatasetID); err == nil && ok {
		parts = append(parts, fmt.Sprintf("%s：已读到 %s，截止 %s", schedule.RescanProgressLabel, from.Format(time.RFC3339), until.Format(time.RFC3339)))
	}
	if extra := s.successMessage(); extra != "" {
		parts = append(parts, extra)
	}
	return strings.Join(parts, "；")
}
