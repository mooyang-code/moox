// Package runner executes one archive run: compute the read range, read it page by page, stage the
// changed rows, export due partitions, then finalize partitions and advance the watermark atomically.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

// Source 读取数据集的列与时间区间行。
type Source interface {
	Columns(ctx context.Context, spaceID, datasetID string) ([]staging.Column, error)
	ReadRange(ctx context.Context, req source.Range) (source.Page, error)
}

// Pacer 控制请求节奏。
type Pacer interface {
	WaitTurn(ctx context.Context) error
	Throttle(ctx context.Context, bytes int64) error
	Sleep(ctx context.Context, d time.Duration) error
	Interval() time.Duration
}

// Progress 是运行中的进度统计。
type Progress struct {
	SubjectsDone uint64
	RequestCount uint64
	BytesRead    uint64
	RowsRead     uint64
}

// Outcome 是一次运行的结果，用来填 CompleteArchiveRun。
type Outcome struct {
	Status        string
	Message       string
	Progress      Progress
	RowsAdded     uint64
	RowsChanged   uint64
	Exported      uint32
	Finalized     uint32
	Partitions    []*archivepb.PartitionReport
	Unrecoverable []*archivepb.UnrecoverableRange
	FinalUntil    time.Time
}

// Runner 执行运行。
type Runner struct {
	Source   Source
	Staging  *staging.Store
	Files    export.Files
	Pace     Pacer
	PageRows int
	Now      func() time.Time
	// ReadAttempts 是单页读取失败时的尝试次数（含第一次）。
	ReadAttempts int
}

// errStopped 表示运行被取消。
var errStopped = errors.New("archive run stopped")

type runState struct {
	r        *Runner
	a        *archivepb.RunAssignment
	task     staging.Task
	loc      *time.Location
	unit     string
	started  time.Time
	lookback time.Duration
	hold     time.Duration
	report   func(Progress) bool
	progress Progress
	subjects map[string]struct{}
	added    uint64
	changed  uint64
	touched  map[string]bool
	reopened map[string]bool
	previews map[string]string
	// reported 为真表示 finalize 已经生成了最终的分区登记（定稿分区的暂存行已删除，不能再统计）。
	reported bool
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Execute 执行一次运行。report 在每页之后调用，返回 true 表示运行已被取消。
// 出错或取消时已提交的批次留在暂存区，水位不推进；返回的 Outcome 仍带上本次涉及的分区登记，便于 mgr 展示「暂存中」。
func (r *Runner) Execute(ctx context.Context, a *archivepb.RunAssignment, report func(Progress) bool) (out *Outcome, err error) {
	started, err := time.Parse(time.RFC3339, a.GetStartedAt())
	if err != nil {
		return nil, fmt.Errorf("run %s has invalid started_at: %w", a.GetRunId(), err)
	}
	loc, err := time.LoadLocation(a.GetSpaceTz())
	if err != nil {
		return nil, fmt.Errorf("run %s has invalid space time zone %q: %w", a.GetRunId(), a.GetSpaceTz(), err)
	}
	if a.GetPartitionUnit() != "day" && a.GetPartitionUnit() != "month" {
		return nil, fmt.Errorf("run %s has invalid partition unit %q", a.GetRunId(), a.GetPartitionUnit())
	}
	columns, err := r.Source.Columns(ctx, a.GetSpaceId(), a.GetDatasetId())
	if err != nil {
		return nil, err
	}
	s := &runState{
		r: r, a: a, loc: loc, unit: a.GetPartitionUnit(), started: started.UTC(),
		lookback: time.Duration(a.GetMinLookbackSeconds()) * time.Second, hold: time.Duration(a.GetStagingRetentionSeconds()) * time.Second,
		report: report, subjects: map[string]struct{}{}, touched: map[string]bool{}, reopened: map[string]bool{}, previews: map[string]string{},
		task: staging.Task{SpaceID: a.GetSpaceId(), DatasetID: a.GetDatasetId(), Columns: columns},
	}
	if err := r.Staging.EnsureTable(ctx, s.task); err != nil {
		return nil, err
	}
	if err := r.Files.WriteDatasetJSON(a.GetSpaceId(), a.GetDatasetId(), a.GetFreq(), columns, r.now()); err != nil {
		return nil, fmt.Errorf("write _dataset.json: %w", err)
	}
	out = &Outcome{Status: "succeeded"}
	defer func() {
		// 不论成功、失败还是取消，都带上本次涉及的分区的当前登记。
		out.Progress = s.progress
		out.RowsAdded, out.RowsChanged = s.added, s.changed
		if !s.reported {
			if reports, rerr := s.partitionReports(context.WithoutCancel(ctx), out, nil); rerr == nil {
				out.Partitions = reports
			}
		}
		switch {
		case errors.Is(err, errStopped) || errors.Is(err, context.Canceled):
			out.Status, out.Message, err = "canceled", "运行已取消", nil
		case err != nil:
			out.Status, out.Message = "failed", err.Error()
			err = nil
		}
	}()
	ranges, unrecoverable, err := s.readRanges(ctx)
	if err != nil {
		return out, err
	}
	out.Unrecoverable = unrecoverable
	for _, rng := range ranges {
		if err := s.readChunk(ctx, rng[0], rng[1]); err != nil {
			return out, err
		}
	}
	exported, err := s.exportDue(ctx, out)
	if err != nil {
		return out, err
	}
	if err := s.finalize(ctx, out, exported); err != nil {
		return out, err
	}
	return out, nil
}

// readRanges 算出本次要读的区间，并按分区边界切成块。
func (s *runState) readRanges(ctx context.Context) ([][2]time.Time, []*archivepb.UnrecoverableRange, error) {
	a := s.a
	var start, end time.Time
	if a.GetKind() == "range" {
		var err error
		if start, err = time.Parse(time.RFC3339, a.GetRangeStart()); err != nil {
			return nil, nil, fmt.Errorf("invalid range_start: %w", err)
		}
		if end, err = time.Parse(time.RFC3339, a.GetRangeEnd()); err != nil {
			return nil, nil, fmt.Errorf("invalid range_end: %w", err)
		}
		start, end = start.UTC(), end.UTC()
	} else {
		span, err := schedule.BarSpan(a.GetFreq())
		if err != nil {
			return nil, nil, err
		}
		end = s.started
		watermark, ok, err := s.r.Staging.Watermark(ctx, a.GetSpaceId(), a.GetDatasetId())
		if err != nil {
			return nil, nil, err
		}
		switch {
		case ok:
			start = watermark.Add(-s.lookback - span)
		case a.GetBackfillFrom() != "":
			if start, err = time.Parse(time.RFC3339, a.GetBackfillFrom()); err != nil {
				return nil, nil, fmt.Errorf("invalid backfill_from: %w", err)
			}
			start = start.UTC()
		default:
			start = s.started.Add(-time.Duration(a.GetIntervalSeconds())*time.Second - s.lookback - span)
		}
	}
	var unrecoverable []*archivepb.UnrecoverableRange
	if limit, forever, err := parseRetention(a.GetRetention()); err != nil {
		return nil, nil, err
	} else if !forever {
		if earliest := s.started.Add(-limit); start.Before(earliest) {
			cut := earliest
			if cut.After(end) {
				cut = end
			}
			unrecoverable = append(unrecoverable, &archivepb.UnrecoverableRange{StartTime: start.Format(time.RFC3339), EndTime: cut.Format(time.RFC3339)})
			start = earliest
		}
	}
	var ranges [][2]time.Time
	for cur := start; cur.Before(end); {
		_, _, partEnd := schedule.PartitionOf(s.unit, s.loc, cur)
		next := partEnd
		if next.After(end) {
			next = end
		}
		ranges = append(ranges, [2]time.Time{cur, next})
		cur = next
	}
	return ranges, unrecoverable, nil
}

func parseRetention(value string) (time.Duration, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "forever" {
		return 0, true, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, false, fmt.Errorf("invalid retention %q", value)
	}
	return d, false, nil
}

func (s *runState) partitionOf(at time.Time) staging.PartitionRef {
	key, start, end := schedule.PartitionOf(s.unit, s.loc, at)
	return staging.PartitionRef{Key: key, Start: start, End: end}
}

// readChunk 读取一个分区范围内的 [start, end)，逐页写入暂存区。
func (s *runState) readChunk(ctx context.Context, start, end time.Time) error {
	cursor := ""
	pageRows := s.r.PageRows
	if pageRows <= 0 || pageRows > 2000 {
		pageRows = 2000
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.r.Pace.WaitTurn(ctx); err != nil {
			return err
		}
		page, err := s.readPage(ctx, source.Range{SpaceID: s.a.GetSpaceId(), DatasetID: s.a.GetDatasetId(), Columns: s.task.Columns, Start: start, End: end, Cursor: cursor, PageRows: pageRows})
		if err != nil {
			return err
		}
		s.progress.RequestCount++
		s.progress.BytesRead += uint64(page.Bytes)
		s.progress.RowsRead += uint64(len(page.Rows))
		if err := s.r.Pace.Throttle(ctx, page.Bytes); err != nil {
			return err
		}
		if err := s.stage(ctx, page.Rows); err != nil {
			return err
		}
		s.progress.SubjectsDone = uint64(len(s.subjects))
		if s.report != nil && s.report(s.progress) {
			return errStopped
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
		if err := s.r.Pace.Sleep(ctx, s.r.Pace.Interval()); err != nil {
			return err
		}
	}
}

func (s *runState) readPage(ctx context.Context, req source.Range) (source.Page, error) {
	attempts := s.r.ReadAttempts
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := s.r.Pace.Sleep(ctx, time.Duration(i)*2*time.Second); err != nil {
				return source.Page{}, err
			}
			if err := s.r.Pace.WaitTurn(ctx); err != nil {
				return source.Page{}, err
			}
		}
		page, err := s.r.Source.ReadRange(ctx, req)
		if err == nil {
			return page, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return source.Page{}, ctx.Err()
		}
	}
	return source.Page{}, lastErr
}

// stage 把一页写入暂存区。要写入的行落在已定稿分区时，先把原文件读回暂存表、标回 open，再写入。
func (s *runState) stage(ctx context.Context, rows []staging.Row) error {
	if len(rows) == 0 {
		return nil
	}
	keys := map[string]bool{}
	for _, row := range rows {
		s.subjects[row.SubjectID] = struct{}{}
		keys[s.partitionOf(row.DataTime).Key] = true
	}
	for key := range keys {
		p, ok, err := s.r.Staging.Partition(ctx, s.task.SpaceID, s.task.DatasetID, key)
		if err != nil {
			return err
		}
		if !ok || p.Status != staging.StatusFinal {
			continue
		}
		rel, err := export.RelPath(s.task.SpaceID, s.task.DatasetID, key)
		if err != nil {
			return err
		}
		if err := s.r.Staging.ReopenFinal(ctx, s.task, key, s.r.Files.Abs(rel)); err != nil {
			return fmt.Errorf("reopen final partition %s: %w", key, err)
		}
		s.reopened[key] = true
	}
	result, err := s.r.Staging.Stage(ctx, s.task, rows, s.partitionOf, s.r.now())
	if err != nil {
		return err
	}
	s.added += result.Added
	s.changed += result.Changed
	for key := range result.Touched {
		s.touched[key] = true
	}
	return nil
}

// closeAt 返回分区的收盘时间。
func (s *runState) closeAt(p staging.PartitionState) (time.Time, error) {
	return schedule.PartitionClose(s.unit, s.loc, s.a.GetLocalClose(), s.a.GetFreq(), p.Start, p.End)
}

// exportDue 导出所有到时间且需要导出的分区：运行开始 ≥ 分区收盘时间 + L。
func (s *runState) exportDue(ctx context.Context, out *Outcome) (map[string]export.Info, error) {
	exported := map[string]export.Info{}
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		if !p.NeedsExport() {
			continue
		}
		closeAt, err := s.closeAt(p)
		if err != nil {
			return nil, err
		}
		if s.started.Before(closeAt.Add(s.lookback)) {
			continue
		}
		info, err := s.r.Files.Write(ctx, s.r.Staging, s.task, p)
		if err != nil {
			return nil, err
		}
		if err := s.r.Staging.MarkExported(ctx, s.task.SpaceID, s.task.DatasetID, p.Key, s.r.now(), info.SHA256); err != nil {
			return nil, err
		}
		exported[p.Key] = info
		s.previews[p.Key] = info.Preview
		s.touched[p.Key] = true
		out.Exported++
	}
	return exported, nil
}

// finalize 在一个暂存库事务里定稿满足条件的分区并推进读取水位：
// 运行开始 ≥ 分区收盘时间 + L + 暂存保留期，且没有待导出的变化。补拉区间不推进水位。
func (s *runState) finalize(ctx context.Context, out *Outcome, exported map[string]export.Info) error {
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	var keys []string
	stats := map[string]staging.PartitionStats{}
	for _, p := range parts {
		if p.Status == staging.StatusFinal || p.NeedsExport() || p.ExportedAt.IsZero() {
			continue
		}
		closeAt, err := s.closeAt(p)
		if err != nil {
			return err
		}
		if s.started.Before(closeAt.Add(s.lookback).Add(s.hold)) {
			continue
		}
		st, err := s.r.Staging.Stats(ctx, s.task, p)
		if err != nil {
			return err
		}
		stats[p.Key] = st
		keys = append(keys, p.Key)
	}
	advance := s.a.GetKind() != "range"
	if len(keys) == 0 && !advance {
		return nil
	}
	if err := s.r.Staging.Finalize(ctx, s.task, keys, s.started, advance); err != nil {
		return err
	}
	for _, key := range keys {
		s.touched[key] = true
	}
	out.Finalized = uint32(len(keys))
	final := map[string]staging.PartitionStats{}
	for k, v := range stats {
		final[k] = v
	}
	reports, err := s.partitionReports(ctx, out, final)
	if err != nil {
		return err
	}
	out.Partitions = reports
	s.reported = true
	return nil
}

// partitionReports 为本次涉及的分区生成登记。finalStats 给出刚定稿分区在删除暂存行之前的统计。
func (s *runState) partitionReports(ctx context.Context, out *Outcome, finalStats map[string]staging.PartitionStats) ([]*archivepb.PartitionReport, error) {
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range s.task.Columns {
		names = append(names, c.Name)
	}
	var reports []*archivepb.PartitionReport
	for _, p := range parts {
		if !s.touched[p.Key] {
			continue
		}
		st, ok := finalStats[p.Key]
		if !ok {
			if st, err = s.r.Staging.Stats(ctx, s.task, p); err != nil {
				return nil, err
			}
		}
		report := &archivepb.PartitionReport{
			PartitionKey: p.Key, RangeStart: p.Start.Format(time.RFC3339), RangeEnd: p.End.Format(time.RFC3339), Status: p.Status,
			RowCount: st.Rows, SubjectCount: st.Subjects, ExportCount: p.ExportCount,
			LastChangedAt: formatTime(p.LastChangedAt), ExportedAt: formatTime(p.ExportedAt),
		}
		if !st.Min.IsZero() {
			report.MinDataTime, report.MaxDataTime = st.Min.Format(time.RFC3339), st.Max.Format(time.RFC3339)
		}
		if p.Status == staging.StatusFinal {
			report.FinalizedAt = formatTime(s.r.now())
		}
		if !p.ExportedAt.IsZero() {
			rel, err := export.RelPath(s.task.SpaceID, s.task.DatasetID, p.Key)
			if err != nil {
				return nil, err
			}
			report.FilePath, report.FileSha256, report.Columns = rel, p.SHA256, names
			if info, err := os.Stat(s.r.Files.Abs(rel)); err == nil {
				report.FileBytes = uint64(info.Size())
			}
			report.Preview = s.previews[p.Key] // 本次没有重新导出时留空，mgr 保留已登记的预览
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].PartitionKey < reports[j].PartitionKey })
	if all, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID); err == nil {
		out.FinalUntil = finalBoundary(all)
	}
	return reports, nil
}

// finalBoundary 返回定稿边界：已定稿分区中最晚的结束时间，且它之前没有未定稿的分区。
func finalBoundary(parts []staging.PartitionState) time.Time {
	sorted := append([]staging.PartitionState(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })
	var boundary time.Time
	for _, p := range sorted {
		if p.Status != staging.StatusFinal {
			break
		}
		boundary = p.End
	}
	return boundary
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
