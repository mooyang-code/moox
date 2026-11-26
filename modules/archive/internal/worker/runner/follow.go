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
	// followBudget 是一轮读取（日志与整段重读共用）最多花的墙钟时间；用完就在段的边界停下，剩下的留给下一轮，
	// 免得一条运行长时间占着读 Storage 的名额。
	followBudget = time.Minute
	// quietWaitLimit：下一个允许读取的时刻比现在晚这么多以上，说明落在整点窗口里（秒窗口最多等 30 秒）。
	quietWaitLimit = 2 * time.Minute
	// rescanSegment 是低于 1d 的数据集一段的数据时间（整段重读与日志里读全部标的的范围共用，不跨分区）；1d 及以上一段是一个分区。
	rescanSegment = time.Hour
	// selectorSegmentRows 是日志里带 selector 的范围一段最多的行数（标的数 × K 线根数），与 crypto 1m 读全部标的一小时一段
	// （约 500 × 60）差不多大：100 个标的的 1m 日分区切成 5 段，单标的补一整天仍是一段。
	selectorSegmentRows = 30000
)

// rescanOpenEnd 是整段重读最后一段的终点：最后一段不设上界，晚于重建基线时刻的行也一并读到，不依赖“数据时间不晚于写入时刻”。
var rescanOpenEnd = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// pendingPage 是读了一半的变更日志页：还没读的段与这一页之后的位点。只记在内存里（Runner.pending）：
// 下一轮暂存库里的位点仍是 cursor、任务 ID 也没变时接着读剩下的段，不再取这一页；否则（manual、断档重建、
// 任务重建、worker 重启）丢掉，从暂存库里的位点重读整页（写暂存区按主键比对替换，重读幂等）。
type pendingPage struct {
	taskID   string
	cursor   source.ChangeCursor // 这一页的起始位点（暂存库里的位点，整页读完之前不动）
	next     source.ChangeCursor
	hasMore  bool
	segments []changeRead // 还没读的段，按时间先后
}

// runFollow 执行 follow 与 manual 的一轮（T = 运行开始）：
//  1. 先导出、上传、定稿到期的分区（不读 Storage，休市时也靠它定稿）；
//  2. 整点窗口内不读，本轮到此结束；
//  3. 没有位点（首次运行、暂存库新建）或 manual 时重建基线（rebase）；
//  4. 从位点起按页读变更日志，每页按分区整理、切段，逐段读行、写暂存区，整页写完才推进位点；任何一页断档就重建基线、本轮不再读日志；
//  5. 用剩余预算整段重读，每段写完推进重读进度；
//  6. 再导出、上传、定稿一次。
//
// 读取预算按本轮墙钟计（followBudget，且下一次读取不必等窗口），在段的边界检查；每轮至少读一段，保证有进展。
func (s *runState) runFollow(ctx context.Context, out *Outcome) error {
	s.roundStart = s.r.now()
	if s.a.GetKind() == kindManual {
		// manual 不论位点是否有效都重建基线：整段重读线上保留期，之后的 follow 分段读完。
		if err := s.rebase(ctx, out, false); err != nil {
			return err
		}
	}
	storeID, sequence, ok, err := s.r.Staging.ChangeCursor(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	cursor := source.ChangeCursor{StoreID: storeID, Sequence: sequence}
	if ok {
		// 在导出之前取出上一轮读了一半的页：正在读的分区这时不能导出。
		s.page = s.r.takePage(s.pageKey(), s.a.GetTaskId(), cursor)
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
	if !ok {
		if err := s.rebase(ctx, out, false); err != nil {
			return err
		}
		if storeID, sequence, _, err = s.r.Staging.ChangeCursor(ctx, s.task.SpaceID, s.task.DatasetID); err != nil {
			return err
		}
		cursor = source.ChangeCursor{StoreID: storeID, Sequence: sequence}
	}
	if err := s.followChanges(ctx, out, cursor); err != nil {
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

// followChanges 从 cursor 起按页读变更日志：每页的范围按分区整理（groupChanges）、切成段（changeSegments），按时间先后逐段
// 读行、写暂存区，一页的段全部写完才把位点推进到这一页之后。每轮至少读一段，之后预算用完就停在段的边界：剩下的段记在内存里
// （pendingPage），下一轮接着读，位点不动；中途失败或取消时同样从下一段接着读。任何一页断档时重建基线，本轮不再读日志。
func (s *runState) followChanges(ctx context.Context, out *Outcome, cursor source.ChangeCursor) error {
	spaceID, datasetID := s.task.SpaceID, s.task.DatasetID
	bound, err := s.lowerBound(ctx)
	if err != nil {
		return err
	}
	read := 0 // 本轮已读的段数
	for pages := 0; ; pages++ {
		if s.page == nil {
			if pages > 0 && !s.budgetLeft() {
				return nil
			}
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
			s.page = &pendingPage{taskID: s.a.GetTaskId(), cursor: cursor, next: page.Next, hasMore: page.HasMore,
				segments: s.changeSegments(s.groupChanges(page.Spans, bound))}
			s.r.keepPage(s.pageKey(), s.page)
		}
		for len(s.page.segments) > 0 {
			if read > 0 && !s.budgetLeft() {
				return nil
			}
			seg := s.page.segments[0]
			if err := s.readChunk(ctx, seg.Start, seg.End, seg.Subjects); err != nil {
				return err
			}
			s.page.segments = s.page.segments[1:]
			read++
			s.r.keepPage(s.pageKey(), s.page)
		}
		next, more := s.page.next, s.page.hasMore
		if err := s.r.Staging.SetChangeCursor(ctx, spaceID, datasetID, next.StoreID, next.Sequence); err != nil {
			return err
		}
		s.page = nil
		s.r.keepPage(s.pageKey(), nil)
		cursor = next
		if s.report != nil && s.report(s.progress) {
			return errStopped
		}
		if !more {
			return nil
		}
	}
}

// changeSegments 把一页日志整理出的读取范围切成段，都不跨分区：读全部标的的按整段重读同样的粒度切（segmentEnd：低于 1d 最多 1 小时）；
// 带 selector 的（不超过 100 个标的）按“标的数 × K 线根数 ≤ selectorSegmentRows”切，标的少时一个分区一段，单标的补一长段时不把请求数放大。
func (s *runState) changeSegments(reads []changeRead) []changeRead {
	var out []changeRead
	for _, read := range reads {
		if read.Subjects != nil {
			out = append(out, s.selectorSegments(read)...)
			continue
		}
		for from := read.Start; from.Before(read.End); {
			end := s.segmentEnd(from)
			if end.After(read.End) {
				end = read.End
			}
			out = append(out, changeRead{Key: read.Key, Start: from, End: end})
			from = end
		}
	}
	return out
}

// selectorSegments 把一个分区里带 selector 的范围按每段最多 selectorSegmentRows 行（标的数 × K 线根数）切段。
func (s *runState) selectorSegments(read changeRead) []changeRead {
	bars := int64(max(1, selectorSegmentRows/len(read.Subjects)))
	if int64(read.End.Sub(read.Start)/s.span) < bars {
		// 整段不超过上限：一段读完（先比根数再乘，月分区、长周期时也不会溢出）。
		return []changeRead{read}
	}
	step := time.Duration(bars) * s.span
	var out []changeRead
	for from := read.Start; from.Before(read.End); {
		end := from.Add(step)
		if end.After(read.End) {
			end = read.End
		}
		out = append(out, changeRead{Key: read.Key, Start: from, End: end, Subjects: read.Subjects})
		from = end
	}
	return out
}

// reading 报告分区是不是变更日志读了一半的页正在读的那个分区（剩下的第一段所在的分区）：它的改动还没读完，
// 这时导出会让一个分区随之后每一轮被反复导出、定稿、读回（与整段重读只导出到 rescan_from 同理）。
func (s *runState) reading(key string) bool {
	return s.page != nil && len(s.page.segments) > 0 && s.page.segments[0].Key == key
}

func (s *runState) pageKey() string { return s.task.SpaceID + "/" + s.task.DatasetID }

// takePage 取出数据集读了一半的页：任务 ID 与起始位点都对得上才用，对不上就丢掉（位点已重设或任务已重建）。
func (r *Runner) takePage(key, taskID string, cursor source.ChangeCursor) *pendingPage {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[key]
	if !ok {
		return nil
	}
	if p.taskID != taskID || p.cursor != cursor {
		delete(r.pending, key)
		return nil
	}
	return &p
}

// keepPage 记下数据集读了一半的页；p 为 nil 时清掉。
func (r *Runner) keepPage(key string, p *pendingPage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p == nil {
		delete(r.pending, key)
		return
	}
	if r.pending == nil {
		r.pending = map[string]pendingPage{}
	}
	r.pending[key] = *p
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

// segmentEnd 返回从 from 起一段的终点：低于 1d 为下一个整点（最多 1 小时数据时间），且不跨分区；1d 及以上为分区结束。
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
// mgr 的健康规则据此跳过“到期未导出”）；变更日志一页没读完时带上还剩几段（这时每轮都成功，积压只在这里看得见）。
func (s *runState) followMessage(ctx context.Context, lead string) string {
	var parts []string
	if lead != "" {
		parts = append(parts, lead)
	}
	if s.page != nil && len(s.page.segments) > 0 {
		parts = append(parts, fmt.Sprintf("变更日志这一页还剩 %d 段没读（从 %s 起），下一轮接着读", len(s.page.segments), s.page.segments[0].Start.Format(time.RFC3339)))
	}
	if from, until, ok, err := s.r.Staging.Rescan(ctx, s.task.SpaceID, s.task.DatasetID); err == nil && ok {
		parts = append(parts, fmt.Sprintf("%s：已读到 %s，截止 %s", schedule.RescanProgressLabel, from.Format(time.RFC3339), until.Format(time.RFC3339)))
	}
	if extra := s.successMessage(); extra != "" {
		parts = append(parts, extra)
	}
	return strings.Join(parts, "；")
}
