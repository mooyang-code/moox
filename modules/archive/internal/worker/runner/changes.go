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

// lateRanges 读 Storage 的变更日志（从暂存库里的位点起），找出回看窗口之外被改动过的时间范围：
// 早于 windowStart 的补采、因子补算写回、历史导入。每个受影响的分区返回一段 [最早改动, 最晚改动]，
// 调用方照常读这一段，落在已定稿分区的行会走读回原文件、重新导出的路径。
//
// 重读的下界取线上保留期截止与暂存库里最早一个分区的开始两者的较晚者：早于归档起点的改动不重读，
// 否则会新建一个只含改动段的分区并定稿，看起来和完整分区一样。一个分区都没有时不重读。
//
// 位点只在运行成功后推进（s.nextCursor）；没有位点（首次）或位点断档（日志已清理、存储重建）时
// 取日志头部当新位点并立即落盘，本次只按回看窗口读取。读变更日志失败不让运行失败，位点不动，下次重试。
func (s *runState) lateRanges(ctx context.Context, windowStart time.Time) [][2]time.Time {
	spaceID, datasetID := s.task.SpaceID, s.task.DatasetID
	degrade := func(reason string, err error) [][2]time.Time {
		log.Warnf("archive worker %s/%s 读变更日志失败（%s），本次只按回看窗口读取: %v", spaceID, datasetID, reason, err)
		s.notes = append(s.notes, "读变更日志失败，本次只按回看窗口读取："+err.Error())
		return nil
	}
	storeID, sequence, ok, err := s.r.Staging.ChangeCursor(ctx, spaceID, datasetID)
	if err != nil {
		return degrade("读位点", err)
	}
	if !ok {
		if err := s.rebaseChangeCursor(ctx); err != nil {
			return degrade("取日志头部", err)
		}
		return nil
	}
	cursor := source.ChangeCursor{StoreID: storeID, Sequence: sequence}
	// 线上保留期之前的数据 Storage 已经删除，不重读。
	var earliest time.Time
	if limit, forever, err := parseRetention(s.a.GetRetention()); err == nil && !forever {
		earliest = s.started.Add(-limit)
	}
	parts, err := s.r.Staging.Partitions(ctx, spaceID, datasetID)
	if err != nil {
		return degrade("读分区状态", err)
	}
	var first time.Time
	for _, p := range parts {
		if first.IsZero() || p.Start.Before(first) {
			first = p.Start
		}
	}
	if first.After(earliest) {
		earliest = first
	}
	// seen 按时间范围去重：同一周期各标的的范围通常相同。
	seen := map[[2]int64]bool{}
	// hull 按分区键记这个分区里被改动的最早与最晚时间点。
	hull := map[string][2]time.Time{}
	for {
		if err := s.r.Pace.WaitTurn(ctx); err != nil {
			return degrade("等待节奏", err)
		}
		page, err := s.r.Source.ReadChanges(ctx, spaceID, datasetID, &cursor)
		if err != nil {
			return degrade("读日志", err)
		}
		if page.Gap {
			log.Warnf("archive worker %s/%s 的变更日志位点 %s/%d 已断档（日志已清理或存储重建），本次只按回看窗口读取，窗口之外的晚到写入可能没有归档",
				spaceID, datasetID, cursor.StoreID, cursor.Sequence)
			s.notes = append(s.notes, "变更日志位点已断档，本次只按回看窗口读取；窗口之外的晚到写入需要用补拉区间重读")
			if err := s.rebaseChangeCursor(ctx); err != nil {
				return degrade("取日志头部", err)
			}
			return nil
		}
		if err := s.r.Pace.Throttle(ctx, page.Bytes); err != nil {
			return degrade("限速", err)
		}
		for _, span := range page.Spans {
			id := [2]int64{span[0].UnixNano(), span[1].UnixNano()}
			if len(parts) == 0 || seen[id] {
				continue
			}
			seen[id] = true
			s.addLateSpan(hull, span[0], span[1], earliest, windowStart)
		}
		cursor = page.Next
		if !page.HasMore {
			break
		}
	}
	s.nextCursor = &cursor
	if len(hull) == 0 {
		return nil
	}
	ranges := make([][2]time.Time, 0, len(hull))
	for _, h := range hull {
		// 改动范围两端都含，读取区间右开。
		ranges = append(ranges, [2]time.Time{h[0], h[1].Add(time.Nanosecond)})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0].Before(ranges[j][0]) })
	log.Infof("archive worker %s/%s 的变更日志显示 %d 个分区在回看窗口之外有改动，重读这些分区的改动范围", spaceID, datasetID, len(ranges))
	s.notes = append(s.notes, fmt.Sprintf("重读了 %d 个在回看窗口之外有改动的分区", len(ranges)))
	return ranges
}

// addLateSpan 把一段改动范围 [start, end]（含两端）里早于 windowStart、不早于 earliest 的部分按分区并进 hull。
func (s *runState) addLateSpan(hull map[string][2]time.Time, start, end, earliest, windowStart time.Time) {
	if start.Before(earliest) {
		start = earliest
	}
	if !end.Before(windowStart) {
		end = windowStart.Add(-time.Nanosecond)
	}
	for cur := start; !cur.After(end); {
		key, _, partEnd := schedule.PartitionOf(s.unit, s.loc, cur)
		last := partEnd.Add(-time.Nanosecond)
		if last.After(end) {
			last = end
		}
		h, ok := hull[key]
		if !ok || cur.Before(h[0]) {
			h[0] = cur
		}
		if !ok || last.After(h[1]) {
			h[1] = last
		}
		hull[key] = h
		cur = partEnd
	}
}

// rebaseChangeCursor 取变更日志头部当新位点并立即落盘：之后才开始读数据，头部之前的写入由本次及之后的窗口读取覆盖。
func (s *runState) rebaseChangeCursor(ctx context.Context) error {
	head, err := s.r.Source.ReadChanges(ctx, s.task.SpaceID, s.task.DatasetID, nil)
	if err != nil {
		return err
	}
	return s.r.Staging.SetChangeCursor(ctx, s.task.SpaceID, s.task.DatasetID, head.Next.StoreID, head.Next.Sequence)
}
