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
	Status      string
	Message     string
	Progress    Progress
	RowsAdded   uint64
	RowsChanged uint64
	Exported    uint32
	Finalized   uint32
	Partitions  []*archivepb.PartitionReport
	// ReportedKeys 是 Partitions 里的分区键；mgr 确认登记之后调用方据此清除待上报标记。
	ReportedKeys  []string
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
	bound    bool // 任务实例已确认，暂存状态属于当前任务
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
	reopened map[string]bool
	previews map[string]string
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
	s := &runState{
		r: r, a: a, loc: loc, unit: a.GetPartitionUnit(), started: started.UTC(),
		lookback: time.Duration(a.GetMinLookbackSeconds()) * time.Second, hold: time.Duration(a.GetStagingRetentionSeconds()) * time.Second,
		report: report, subjects: map[string]struct{}{}, reopened: map[string]bool{}, previews: map[string]string{},
		task: staging.Task{SpaceID: a.GetSpaceId(), DatasetID: a.GetDatasetId()},
	}
	out = &Outcome{Status: "succeeded"}
	defer func() {
		// 不论成功、失败还是取消，都带上本次涉及的分区的当前登记。
		out.Progress = s.progress
		out.RowsAdded, out.RowsChanged = s.added, s.changed
		// 任务实例确认之前（绑定失败）暂存库里可能还是旧任务的状态，不能把它的分区报给新任务。
		if s.bound {
			if reports, rerr := s.partitionReports(context.WithoutCancel(ctx), out); rerr == nil {
				out.Partitions = reports
				for _, p := range reports {
					out.ReportedKeys = append(out.ReportedKeys, p.GetPartitionKey())
				}
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
	// 任务被删除后重建时，旧任务的水位和暂存数据不能带到新任务上。
	if _, err = r.Staging.BindEpoch(ctx, a.GetSpaceId(), a.GetDatasetId(), a.GetTaskEpoch()); err != nil {
		return out, err
	}
	ranges, unrecoverable, err := s.readRanges(ctx)
	if err != nil {
		return out, err
	}
	// 读取起点（含首次运行的起点）已落盘，再去取列信息、建表：这一步失败重试时不会让起点后移而漏读。
	columns, err := r.Source.Columns(ctx, a.GetSpaceId(), a.GetDatasetId())
	if err != nil {
		return out, err
	}
	s.task.Columns = columns
	s.bound = true // 实例已确认且列信息齐全，才能收集分区报告
	if err := r.Staging.EnsureTable(ctx, s.task); err != nil {
		return out, err
	}
	if err := r.Files.WriteDatasetJSON(r.Files.BuildDatasetMeta(export.DatasetMetaInput{
		SpaceID: a.GetSpaceId(), DatasetID: a.GetDatasetId(), FilePrefix: s.filePrefix(), Freq: a.GetFreq(), PartitionUnit: a.GetPartitionUnit(),
		Timezone: a.GetSpaceTz(), DatasetJSON: a.GetDatasetMetaJson(), Columns: columns, Infos: r.columnMetas(a.GetSpaceId(), a.GetDatasetId()),
	}, r.now())); err != nil {
		return out, fmt.Errorf("write _dataset.json: %w", err)
	}
	out.Unrecoverable = unrecoverable
	for _, rng := range ranges {
		if err := s.readChunk(ctx, rng[0], rng[1]); err != nil {
			return out, err
		}
	}
	if err := s.exportDue(ctx, out); err != nil {
		return out, err
	}
	if err := s.finalize(ctx, out); err != nil {
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
			// 首次运行没有水位：起点从本次运行开始往前推。失败后重试必须从同一个起点读起，否则会跳过两次起点之间
			// 尚未读到的数据，所以第一次算出的起点记在暂存库里，水位前进之前一直沿用更早的那个。
			derived := s.started.Add(-time.Duration(a.GetIntervalSeconds())*time.Second - s.lookback - span)
			start = derived
			persisted, found, err := s.r.Staging.FirstStart(ctx, a.GetSpaceId(), a.GetDatasetId())
			if err != nil {
				return nil, nil, err
			}
			if found && persisted.Before(derived) {
				start = persisted
			} else if err := s.r.Staging.SetFirstStart(ctx, a.GetSpaceId(), a.GetDatasetId(), derived); err != nil {
				return nil, nil, err
			}
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
		rel, err := s.r.Files.RelPath(s.task.SpaceID, s.filePrefix(), key)
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
	return nil
}

// closeAt 返回分区的收盘时间。
func (s *runState) closeAt(p staging.PartitionState) (time.Time, error) {
	return schedule.PartitionClose(s.unit, s.loc, s.a.GetLocalClose(), s.a.GetFreq(), p.Start, p.End)
}

// exportDue 导出所有到时间且需要导出的分区：运行开始 ≥ 分区收盘时间 + L。
func (s *runState) exportDue(ctx context.Context, out *Outcome) error {
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	for _, p := range parts {
		if !p.NeedsExport() && !s.exportedFileMissing(p) {
			continue
		}
		closeAt, err := s.closeAt(p)
		if err != nil {
			return err
		}
		if s.started.Before(closeAt.Add(s.lookback)) {
			continue
		}
		// 导出时间取 COPY 开始之前的时刻：此后（含 COPY 与登记之间）写入的变化，其 last_changed_at 一定晚于它，会触发下一次重写。
		exportStarted := s.r.now()
		info, err := s.r.Files.Write(ctx, s.r.Staging, s.task, s.filePrefix(), p)
		if err != nil {
			return err
		}
		if err := s.r.Staging.MarkExported(ctx, s.task.SpaceID, s.task.DatasetID, p.Key, exportStarted, info.SHA256); err != nil {
			return err
		}
		s.previews[p.Key] = info.Preview
		out.Exported++
	}
	return nil
}

// finalize 在一个暂存库事务里定稿满足条件的分区并推进读取水位：
// 运行开始 ≥ 分区收盘时间 + L + 暂存保留期，且没有待导出的变化。补拉区间不推进水位。
// 定稿时分区的统计（行数、标的数、数据时间范围）一起记进分区状态，登记不再依赖已删除的暂存行。
func (s *runState) finalize(ctx context.Context, out *Outcome) error {
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	var keys []string
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
		keys = append(keys, p.Key)
	}
	advance := s.a.GetKind() != "range"
	if len(keys) == 0 && !advance {
		return nil
	}
	if err := s.r.Staging.Finalize(ctx, s.task, keys, s.started, s.r.now(), advance); err != nil {
		return err
	}
	out.Finalized = uint32(len(keys))
	return nil
}

// partitionReports 为所有「最新状态还没被 mgr 确认」的分区生成登记：本次涉及的，加上之前上报失败而遗留的。
// 已定稿分区的统计来自定稿时记下的值，未定稿的分区现算。
func (s *runState) partitionReports(ctx context.Context, out *Outcome) ([]*archivepb.PartitionReport, error) {
	pending, err := s.r.Staging.PendingReports(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range s.task.Columns {
		names = append(names, c.Name)
	}
	var reports []*archivepb.PartitionReport
	for _, p := range pending {
		rows, subjects, minTime, maxTime := p.RowCount, p.SubjectCount, p.MinDataTime, p.MaxDataTime
		if p.Status != staging.StatusFinal {
			st, err := s.r.Staging.Stats(ctx, s.task, p)
			if err != nil {
				return nil, err
			}
			rows, subjects, minTime, maxTime = st.Rows, st.Subjects, st.Min, st.Max
		}
		report := &archivepb.PartitionReport{
			PartitionKey: p.Key, RangeStart: p.Start.Format(time.RFC3339), RangeEnd: p.End.Format(time.RFC3339), Status: p.Status,
			RowCount: rows, SubjectCount: subjects, ExportCount: p.ExportCount,
			LastChangedAt: formatTime(p.LastChangedAt), ExportedAt: formatTime(p.ExportedAt), FinalizedAt: formatTime(p.FinalizedAt),
		}
		if !minTime.IsZero() {
			report.MinDataTime, report.MaxDataTime = minTime.Format(time.RFC3339), maxTime.Format(time.RFC3339)
		}
		if !p.ExportedAt.IsZero() {
			rel, err := s.r.Files.RelPath(s.task.SpaceID, s.filePrefix(), p.Key)
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

// filePrefix 是任务下发的导出文件名前缀。
func (s *runState) filePrefix() string { return s.a.GetFilePrefix() }

// exportedFileMissing 报告已导出但还没定稿的分区没有文件（文件被删除）：
// 它的行还在暂存区，重新导出即可。已定稿分区的行已不在暂存区，不在此列。
func (s *runState) exportedFileMissing(p staging.PartitionState) bool {
	if p.Status != staging.StatusExported {
		return false
	}
	rel, err := s.r.Files.RelPath(s.task.SpaceID, s.filePrefix(), p.Key)
	if err != nil {
		return false
	}
	_, err = os.Stat(s.r.Files.Abs(rel))
	return errors.Is(err, os.ErrNotExist)
}

// ColumnDescriber 由能提供列元数据（展示名、来源等）的 Source 实现，用来丰富元数据文件。
type ColumnDescriber interface {
	ColumnInfos(spaceID, datasetID string) []source.ColumnInfo
}

// columnMetas 取最近一次读到的列元数据；Source 不提供时返回 nil。
func (r *Runner) columnMetas(spaceID, datasetID string) []export.ColumnMeta {
	d, ok := r.Source.(ColumnDescriber)
	if !ok {
		return nil
	}
	infos := d.ColumnInfos(spaceID, datasetID)
	out := make([]export.ColumnMeta, 0, len(infos))
	for _, info := range infos {
		out = append(out, export.ColumnMeta{
			Name: info.Name, Type: info.Type, DisplayName: info.DisplayName, OriginType: info.OriginType, OriginID: info.OriginID,
			Required: info.Required, Aliases: info.Aliases, Attributes: info.Attributes,
		})
	}
	return out
}
