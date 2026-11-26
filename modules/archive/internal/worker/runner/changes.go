package runner

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"trpc.group/trpc-go/trpc-go/log"
)

// changeRead 是一页变更日志在一个分区里要重读的范围：时间并集 [Start, End) 与涉及的标的；切段之后是其中一段（Key 不变）。
// Subjects 为 nil 表示读这段时间里的全部标的（标的数超过 selector 上限，或日志条目没带标的）。
type changeRead struct {
	Key        string
	Start, End time.Time
	Subjects   []string
}

// lowerBound 返回读取的下界 S = max(归档起点 A, 保留期起点 R)：A = min(暂存库里最早一个分区的开始（含按 mgr 登记补齐的）, 回补起点)；
// R 是本次运行开始 − 线上保留期（forever 时没有）。早于 S 的写入不读：早于 A 的会新建只含改动段、看起来完整的分区
// （[回补起点, 最早分区) 首次运行已整段读过，之后那里的写入都在变更日志里，不会这样）；早于 R 的 Storage 已经删掉了。
// 先建任务、后补的历史（补采、补算、导入 Storage）落在最早分区之前、回补起点之后，靠取 min 才不被截掉。
func (s *runState) lowerBound(ctx context.Context) (time.Time, error) {
	bound, err := parseOptionalTime(s.a.GetBackfillFrom())
	if err != nil {
		return time.Time{}, fmt.Errorf("backfill_from: %w", err)
	}
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return time.Time{}, err
	}
	for _, p := range parts {
		if bound.IsZero() || p.Start.Before(bound) {
			bound = p.Start
		}
	}
	if retention, ok, err := s.retentionStart(); err != nil {
		return time.Time{}, err
	} else if ok && retention.After(bound) {
		bound = retention
	}
	return bound, nil
}

// retentionStart 返回保留期起点 R = 本次运行开始 − 线上保留期；保留期为 forever 时 ok 为假。
func (s *runState) retentionStart() (time.Time, bool, error) {
	limit, forever, err := parseRetention(s.a.GetRetention())
	if err != nil || forever {
		return time.Time{}, false, err
	}
	return s.started.Add(-limit), true, nil
}

// groupChanges 把一页日志的范围（含两端）截掉早于 bound 的部分，再按分区整理：每个分区一段时间并集与标的集合。
// 标的数超过 selector 上限（实时写入每分钟涉及全部标的）时读这段时间里的全部标的；补采常常只补一两个标的的一长段，
// 按标的读就不会把全部标的的同一段重读一遍。
func (s *runState) groupChanges(spans []source.ChangeSpan, bound time.Time) []changeRead {
	type group struct {
		start, last time.Time
		subjects    map[string]bool
		all         bool
	}
	groups := map[string]*group{}
	for _, span := range spans {
		start, end := span.Start, span.End
		if start.Before(bound) {
			start = bound
		}
		for cur := start; !cur.After(end); {
			key, _, partEnd := schedule.PartitionOf(s.unit, s.loc, cur)
			last := partEnd.Add(-time.Nanosecond)
			if last.After(end) {
				last = end
			}
			g, ok := groups[key]
			if !ok {
				g = &group{start: cur, last: last, subjects: map[string]bool{}}
				groups[key] = g
			}
			if cur.Before(g.start) {
				g.start = cur
			}
			if last.After(g.last) {
				g.last = last
			}
			if span.SubjectID == "" {
				g.all = true
			} else {
				g.subjects[span.SubjectID] = true
			}
			cur = partEnd
		}
	}
	out := make([]changeRead, 0, len(groups))
	for key, g := range groups {
		read := changeRead{Key: key, Start: g.start, End: g.last.Add(time.Nanosecond)}
		if !g.all && len(g.subjects) <= source.MaxSelectors {
			for subject := range g.subjects {
				read.Subjects = append(read.Subjects, subject)
			}
			sort.Strings(read.Subjects)
		}
		out = append(out, read)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// rebase 重建基线：取日志头部位点 H，在一个事务里写位点 = H、整段重读进度 = [S, 取到 H 之后的时刻)。
// 先落位点再读数据：H 之后的写入由之后的日志读取覆盖，之前的由整段重读覆盖。gap 为真表示位点断档（日志已清理或存储重建）。
// 无法补齐的区间：位点断档或暂存库丢失（mgr 有最近成功时间）时是 [最近成功, R)；真正的首次运行是 [回补起点, R)。
// 读了一半的变更日志页随之作废：它的条目都在新位点 H 之前，由整段重读覆盖。
func (s *runState) rebase(ctx context.Context, out *Outcome, gap bool) error {
	s.page = nil
	s.r.keepPage(s.pageKey(), nil)
	_, _, hadCursor, err := s.r.Staging.ChangeCursor(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	// 只取位点、不返回行，开销可以忽略：不等秒窗口与整点窗口（整点窗口内的 manual 也要能登记重读进度）。
	head, err := s.r.Source.ReadChanges(ctx, s.task.SpaceID, s.task.DatasetID, nil)
	if err != nil {
		return err
	}
	s.progress.RequestCount++
	until := s.r.now()
	bound, err := s.lowerBound(ctx)
	if err != nil {
		return err
	}
	if retention, ok, err := s.retentionStart(); err != nil {
		return err
	} else if ok && s.a.GetKind() != kindManual {
		lastSuccess := timeOfMillis(s.a.GetLastSuccessAt())
		backfill, err := parseOptionalTime(s.a.GetBackfillFrom())
		if err != nil {
			return fmt.Errorf("backfill_from: %w", err)
		}
		switch {
		case (gap || !hadCursor) && !lastSuccess.IsZero() && lastSuccess.Before(retention):
			out.Unrecoverable = s.unrecoverable(out.Unrecoverable, lastSuccess, retention)
		case !hadCursor && lastSuccess.IsZero() && !backfill.IsZero() && backfill.Before(retention):
			out.Unrecoverable = s.unrecoverable(out.Unrecoverable, backfill, retention)
		}
	}
	if !bound.Before(until) {
		// 下界不早于现在（回补起点在未来等异常）：只落位点，不重读。
		return s.r.Staging.SetChangeCursor(ctx, s.task.SpaceID, s.task.DatasetID, head.Next.StoreID, head.Next.Sequence)
	}
	if gap {
		log.Warnf("archive worker %s/%s 的变更日志位点已断档（日志已清理或存储重建），从 %s 起整段重读", s.task.SpaceID, s.task.DatasetID, bound.Format(time.RFC3339))
		s.notes = append(s.notes, "变更日志位点已断档，已登记整段重读")
	}
	return s.r.Staging.SetRebase(ctx, s.task.SpaceID, s.task.DatasetID, head.Next.StoreID, head.Next.Sequence, bound, until)
}
