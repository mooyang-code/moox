// Package runner 执行一次归档运行：follow 与 manual 按 Storage 变更日志读改动过的行、必要时分段整段重读（follow.go），
// range 按分区块读下发区间，import 接收 moox-cli 经数据端口写来的批次（import.go）；读到的行比对写进暂存区，
// 到期的分区导出、上传、定稿。
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
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
	// ReadChanges 从游标之后读一页变更日志（Spans 带标的）；cursor 为 nil 时只取日志头部的位点。
	ReadChanges(ctx context.Context, spaceID, datasetID string, cursor *source.ChangeCursor) (source.Changes, error)
}

// Pacer 控制请求节奏。
type Pacer interface {
	// AllowedAt 返回不早于 t 的第一个允许读 Storage 的时刻（秒窗口与整点窗口）。
	AllowedAt(t time.Time) time.Time
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
	// Uploaded 是本次运行上传到对象存储并校验通过的分区数。
	Uploaded   uint32
	Partitions []*archivepb.PartitionReport
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
	// Remote 是对象存储客户端；nil 表示没有配置对象存储（不上传，读回时也不取回）。
	Remote *objectstore.Client
}

// errStopped 表示运行被取消。
var errStopped = errors.New("archive run stopped")

// 运行种类（与 mgr 的 store.Kind* 一致）。
const (
	kindFollow = "follow"
	kindManual = "manual"
	kindRange  = "range"
	kindImport = "import"
)

type runState struct {
	bound   bool // 任务已绑定且列信息齐全，暂存状态属于当前任务，可以收集分区报告
	r       *Runner
	a       *archivepb.RunAssignment
	task    staging.Task
	loc     *time.Location
	unit    string
	started time.Time
	// lateness 是允许迟到：分区收盘后再等这么久才导出。
	lateness time.Duration
	span     time.Duration
	hold     time.Duration
	report   func(Progress) bool
	progress Progress
	subjects map[string]struct{}
	added    uint64
	changed  uint64
	reopened map[string]bool
	previews map[string]string
	// exported 是本次运行导出的分区：它们总要尝试上传，不受补传预算限制。
	exported map[string]bool
	upload   uploadState
	// notes 是成功时追加到运行消息里的说明（例如从对象存储取回了哪个分区）。
	notes []string
	// same 是同键已存在且值相同的行数（导入的运行消息用）；touched 是本次写入涉及的分区。
	same    uint64
	touched map[string]bool
	// cancelNote 是运行被取消时追加到消息里的说明（导入用）。
	cancelNote string
	// roundStart 是 follow、manual 这一轮读取预算的起点。
	roundStart time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Execute 执行一次运行。report 在每页之后调用，返回 true 表示运行已被取消；feed 是导入运行的批次来源（其余种类为 nil）。
// follow、manual 见 follow.go，range 见 runRange，import 见 import.go。出错或取消时已提交的行留在暂存区（写入幂等），
// 位点与重读进度停在最后一个读完的单位之后；返回的 Outcome 仍带上待上报的分区登记。
func (r *Runner) Execute(ctx context.Context, a *archivepb.RunAssignment, report func(Progress) bool, feed *ImportFeed) (out *Outcome, err error) {
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
	span, err := schedule.BarSpan(a.GetFreq())
	if err != nil {
		return nil, err
	}
	if _, err := export.DatasetDir(a.GetSpaceId(), a.GetPurpose(), a.GetDirName()); err != nil {
		return nil, fmt.Errorf("run %s 的任务目录无效（用途 %q、目录名 %q）: %w", a.GetRunId(), a.GetPurpose(), a.GetDirName(), err)
	}
	s := &runState{
		r: r, a: a, loc: loc, unit: a.GetPartitionUnit(), started: started.UTC(),
		lateness: time.Duration(a.GetAllowedLatenessSeconds()) * time.Second, span: span, hold: time.Duration(a.GetStagingRetentionSeconds()) * time.Second,
		report: report, subjects: map[string]struct{}{}, reopened: map[string]bool{}, previews: map[string]string{}, exported: map[string]bool{}, touched: map[string]bool{},
		task: staging.Task{SpaceID: a.GetSpaceId(), DatasetID: a.GetDatasetId(), Purpose: a.GetPurpose(), DirName: a.GetDirName()},
	}
	out = &Outcome{Status: "succeeded"}
	defer func() {
		// 不论成功、失败还是取消，都带上本次涉及的分区的当前登记。
		out.Progress = s.progress
		out.RowsAdded, out.RowsChanged = s.added, s.changed
		// 任务绑定之前暂存库里可能还是旧任务的状态，不能把它的分区报给新任务。
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
			if s.cancelNote != "" {
				out.Message += "；" + s.cancelNote
			}
		case err != nil:
			out.Status, out.Message = "failed", err.Error()
			err = nil
		}
	}()
	// 任务被删除后重建时，旧任务的位点和暂存数据不能带到新任务上。
	if err = r.Staging.BindTask(ctx, s.task, a.GetTaskId()); err != nil {
		return out, err
	}
	// 暂存库丢失后，按 mgr 的登记补齐本地缺的分区状态：已有文件的分区不会被当成新分区从残缺数据导出。
	if err = s.restorePartitions(ctx); err != nil {
		return out, err
	}
	if err = s.prepareTable(ctx); err != nil {
		return out, err
	}
	switch a.GetKind() {
	case kindFollow, kindManual:
		err = s.runFollow(ctx, out)
	case kindRange:
		err = s.runRange(ctx, out)
	case kindImport:
		err = s.runImport(ctx, out, feed)
	default:
		err = fmt.Errorf("未知的运行种类 %q", a.GetKind())
	}
	return out, err
}

// runRange 执行补拉区间：读 [range_start, range_end)（截在线上保留期内），按分区块读，每块之后导出、上传、定稿；不推进位点。
func (s *runState) runRange(ctx context.Context, out *Outcome) error {
	ranges, unrecoverable, err := s.rangeChunks()
	if err != nil {
		return err
	}
	out.Unrecoverable = unrecoverable
	for _, rng := range ranges {
		if err := s.readChunk(ctx, rng[0], rng[1], nil); err != nil {
			return err
		}
		// 每读完一块就导出、定稿到期的分区，暂存库不会随长区间无限变大。
		if err := s.settle(ctx, out); err != nil {
			return err
		}
	}
	if len(ranges) == 0 {
		if err := s.settle(ctx, out); err != nil {
			return err
		}
	}
	// 上传失败不让运行失败（文件已经在本地），只写进消息；健康等级按远端副本规则反映。
	out.Message = s.successMessage()
	return nil
}

// settle 导出、上传、定稿到期的分区；整段重读进行中只处理结束不晚于重读进度的分区（还没重读完的分区不导出，
// 否则一个分区会随每段重读被反复导出、读回，并在暂存保留期已过时定稿成只有半段数据的分区）。
func (s *runState) settle(ctx context.Context, out *Outcome) error {
	until, _, rescanning, err := s.r.Staging.Rescan(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	if !rescanning {
		until = time.Time{}
	}
	return s.settleUntil(ctx, out, until)
}

// settleUntil 导出、上传并定稿结束不晚于 until 的分区（until 为零表示全部）。
func (s *runState) settleUntil(ctx context.Context, out *Outcome, until time.Time) error {
	if err := s.exportDue(ctx, out, until); err != nil {
		return err
	}
	s.uploadPending(ctx, out)
	return s.finalize(ctx, out, until)
}

// prepareTable 取数据集的列、建暂存表并写 dataset.json；成功后任务算已绑定，可以收集分区报告。
func (s *runState) prepareTable(ctx context.Context) error {
	r, a := s.r, s.a
	columns, err := r.Source.Columns(ctx, a.GetSpaceId(), a.GetDatasetId())
	if err != nil {
		return err
	}
	s.task.Columns = columns
	s.bound = true
	if err := r.Staging.EnsureTable(ctx, s.task); err != nil {
		return err
	}
	if err := r.Files.WriteDatasetJSON(r.Files.BuildDatasetMeta(export.DatasetMetaInput{
		SpaceID: a.GetSpaceId(), DatasetID: a.GetDatasetId(), Purpose: a.GetPurpose(), DirName: a.GetDirName(), Freq: a.GetFreq(), PartitionUnit: a.GetPartitionUnit(),
		Timezone: a.GetSpaceTz(), DatasetJSON: a.GetDatasetMetaJson(), Columns: columns, Infos: r.columnMetas(a.GetSpaceId(), a.GetDatasetId()),
	}, r.now())); err != nil {
		return fmt.Errorf("写 dataset.json 失败: %w", err)
	}
	return nil
}

// restorePartitions 把下发的有文件的分区登记补进本地分区状态（只插入本地没有的分区，一律按 final）。
func (s *runState) restorePartitions(ctx context.Context) error {
	regs := make([]staging.PartitionState, 0, len(s.a.GetRegisteredPartitions()))
	for _, p := range s.a.GetRegisteredPartitions() {
		if p.GetFilePath() == "" {
			continue
		}
		state := staging.PartitionState{
			Key: p.GetPartitionKey(), Status: p.GetStatus(), FilePath: p.GetFilePath(), SHA256: p.GetFileSha256(),
			ExportCount: p.GetExportCount(), RowCount: p.GetRowCount(), SubjectCount: p.GetSubjectCount(),
			Remote: staging.Remote{Key: p.GetRemoteKey(), SHA256: p.GetRemoteSha256(), ETag: p.GetRemoteEtag(), Error: p.GetRemoteError()},
		}
		var err error
		for _, item := range []struct {
			value string
			dest  *time.Time
		}{
			{p.GetRangeStart(), &state.Start}, {p.GetRangeEnd(), &state.End}, {p.GetLastChangedAt(), &state.LastChangedAt},
			{p.GetExportedAt(), &state.ExportedAt}, {p.GetFinalizedAt(), &state.FinalizedAt},
			{p.GetMinDataTime(), &state.MinDataTime}, {p.GetMaxDataTime(), &state.MaxDataTime},
			{p.GetRemoteUploadedAt(), &state.Remote.UploadedAt}, {p.GetRemoteVerifiedAt(), &state.Remote.VerifiedAt},
		} {
			if *item.dest, err = parseOptionalTime(item.value); err != nil {
				return fmt.Errorf("分区登记 %s: %w", p.GetPartitionKey(), err)
			}
		}
		regs = append(regs, state)
	}
	if len(regs) == 0 {
		return nil
	}
	_, err := s.r.Staging.RestorePartitions(ctx, s.task.SpaceID, s.task.DatasetID, regs, s.r.now())
	return err
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("无效的时间 %q", value)
	}
	return t.UTC(), nil
}

// rangeChunks 算出补拉区间按分区边界切成的块：起点早于线上保留期时截到保留期起点，截掉的一段记为无法补齐。
func (s *runState) rangeChunks() ([][2]time.Time, []*archivepb.UnrecoverableRange, error) {
	a := s.a
	start, err := time.Parse(time.RFC3339, a.GetRangeStart())
	if err != nil {
		return nil, nil, fmt.Errorf("invalid range_start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, a.GetRangeEnd())
	if err != nil {
		return nil, nil, fmt.Errorf("invalid range_end: %w", err)
	}
	start, end = start.UTC(), end.UTC()
	var unrecoverable []*archivepb.UnrecoverableRange
	if limit, forever, err := parseRetention(a.GetRetention()); err != nil {
		return nil, nil, err
	} else if !forever {
		if earliest := s.started.Add(-limit); start.Before(earliest) {
			cut := earliest
			if cut.After(end) {
				cut = end
			}
			unrecoverable = s.unrecoverable(unrecoverable, start, cut)
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

// unrecoverable 把 [start, end) 记为无法补齐；长度不到一根 K 线跨度时不记：Storage 按天桶删除，这几秒的数据其实还在。
func (s *runState) unrecoverable(list []*archivepb.UnrecoverableRange, start, end time.Time) []*archivepb.UnrecoverableRange {
	if end.Sub(start) < s.span {
		return list
	}
	return append(list, &archivepb.UnrecoverableRange{StartTime: start.Format(time.RFC3339), EndTime: end.Format(time.RFC3339)})
}

// parseRetention 解析线上保留期：只有 forever 是永久，空串按错误处理（数据集信息异常时不能跳过保留期截断）。
func parseRetention(value string) (time.Duration, bool, error) {
	value = strings.TrimSpace(value)
	if value == "forever" {
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

// readChunk 读取 [start, end)，逐页写入暂存区；subjects 非空时只读这些标的（不超过 source.MaxSelectors 个）。
func (s *runState) readChunk(ctx context.Context, start, end time.Time, subjects []string) error {
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
		page, err := s.readPage(ctx, source.Range{
			SpaceID: s.a.GetSpaceId(), DatasetID: s.a.GetDatasetId(), Columns: s.task.Columns, Start: start, End: end,
			Subjects: subjects, Freq: s.a.GetFreq(), Cursor: cursor, PageRows: pageRows,
		})
		if err != nil {
			return err
		}
		s.progress.RequestCount++
		s.progress.BytesRead += uint64(page.Bytes)
		s.progress.RowsRead += uint64(len(page.Rows))
		if err := s.r.Pace.Throttle(ctx, page.Bytes); err != nil {
			return err
		}
		if _, err := s.stage(ctx, page.Rows); err != nil {
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

// stage 把一页写入暂存区（同键比对替换）。要写入的行落在已定稿分区时，先把原文件读回暂存表、标回 open，再写入。
func (s *runState) stage(ctx context.Context, rows []staging.Row) (staging.StageResult, error) {
	if len(rows) == 0 {
		return staging.StageResult{}, nil
	}
	keys := map[string]bool{}
	for _, row := range rows {
		s.subjects[row.SubjectID] = struct{}{}
		keys[s.partitionOf(row.DataTime).Key] = true
	}
	for key := range keys {
		s.touched[key] = true
		p, ok, err := s.r.Staging.Partition(ctx, s.task.SpaceID, s.task.DatasetID, key)
		if err != nil {
			return staging.StageResult{}, err
		}
		if !ok || p.Status != staging.StatusFinal {
			continue
		}
		// 用导出时记下的路径读回，先确认文件没有被改动或截断；本地缺文件或校验不符时从对象存储取回。
		if err := s.r.Files.Verify(p.FilePath, p.SHA256); err != nil {
			if err := s.fetchFinal(ctx, p, err); err != nil {
				return staging.StageResult{}, fmt.Errorf("读回已定稿分区 %s: %w", key, err)
			}
		}
		if err := s.r.Staging.ReopenFinal(ctx, s.task, key, s.r.Files.Abs(p.FilePath)); err != nil {
			return staging.StageResult{}, fmt.Errorf("读回已定稿分区 %s: %w", key, err)
		}
		s.reopened[key] = true
	}
	result, err := s.r.Staging.Stage(ctx, s.task, rows, s.partitionOf, s.r.now())
	if err != nil {
		return staging.StageResult{}, err
	}
	s.added += result.Added
	s.same += result.Same
	s.changed += result.Changed
	return result, nil
}

// closeAt 返回分区的收盘时间。
func (s *runState) closeAt(p staging.PartitionState) (time.Time, error) {
	return schedule.PartitionClose(s.unit, s.loc, s.a.GetLocalClose(), s.a.GetFreq(), p.Start, p.End)
}

// exportDue 导出所有到时间且需要导出的分区：运行开始 ≥ 分区收盘时间 + 允许迟到。
// until 非零时只处理结束不晚于它的分区（整段重读进行中用，见 settle）。
func (s *runState) exportDue(ctx context.Context, out *Outcome, until time.Time) error {
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	for _, p := range parts {
		if !until.IsZero() && p.End.After(until) {
			continue
		}
		if !p.NeedsExport() && !s.exportedFileMissing(p) {
			continue
		}
		closeAt, err := s.closeAt(p)
		if err != nil {
			return err
		}
		if s.started.Before(closeAt.Add(s.lateness)) {
			continue
		}
		// 导出时间取 COPY 开始之前的时刻：此后（含 COPY 与登记之间）写入的变化，其 last_changed_at 一定晚于它，会触发下一次重写。
		exportStarted := s.r.now()
		info, err := s.r.Files.Write(ctx, s.r.Staging, s.task, p)
		if info.Path != "" {
			// 文件已就位就必须记下（运行被取消也要记），否则下次会因“文件已存在却没有记录”拒绝导出。
			if markErr := s.r.Staging.MarkExported(context.WithoutCancel(ctx), s.task.SpaceID, s.task.DatasetID, p.Key, exportStarted, info.Path, info.SHA256); markErr != nil {
				return errors.Join(err, markErr)
			}
		}
		if err != nil {
			return err
		}

		s.previews[p.Key] = info.Preview
		s.exported[p.Key] = true
		out.Exported++
	}
	return nil
}

// finalize 定稿满足条件的分区：运行开始 ≥ 分区收盘时间 + 允许迟到 + 暂存保留期，且没有待导出的变化。
// 定稿前确认文件还在且 sha256 与记录一致（不一致就报错，运行失败）。每个分区一个事务。
// until 非零时只处理结束不晚于它的分区（同 exportDue）。
func (s *runState) finalize(ctx context.Context, out *Outcome, until time.Time) error {
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return err
	}
	var keys []string
	for _, p := range parts {
		if !until.IsZero() && p.End.After(until) {
			continue
		}
		if p.Status == staging.StatusFinal || p.NeedsExport() || p.ExportedAt.IsZero() {
			continue
		}
		closeAt, err := s.closeAt(p)
		if err != nil {
			return err
		}
		if s.started.Before(closeAt.Add(s.lateness).Add(s.hold)) {
			continue
		}
		if err := s.r.Files.Verify(p.FilePath, p.SHA256); err != nil {
			return fmt.Errorf("定稿分区 %s: %w", p.Key, err)
		}
		keys = append(keys, p.Key)
	}
	if err := s.r.Staging.Finalize(ctx, s.task, keys, s.r.now()); err != nil {
		return err
	}
	out.Finalized += uint32(len(keys))
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
		if p.FilePath != "" {
			report.FilePath, report.FileSha256, report.Columns = p.FilePath, p.SHA256, names
			// 本地文件不在时大小为 0：mgr 在路径与 sha256 不变时保留已登记的大小。
			if info, err := os.Stat(s.r.Files.Abs(p.FilePath)); err == nil {
				report.FileBytes = uint64(info.Size())
			}
			report.Preview = s.previews[p.Key] // 本次没有重新导出时留空，mgr 保留已登记的预览
		}
		report.RemoteKey, report.RemoteSha256, report.RemoteEtag, report.RemoteError = p.Remote.Key, p.Remote.SHA256, p.Remote.ETag, p.Remote.Error
		report.RemoteUploadedAt, report.RemoteVerifiedAt = formatTime(p.Remote.UploadedAt), formatTime(p.Remote.VerifiedAt)
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

// exportedFileMissing 报告已导出但还没定稿的分区（含读回后的分区）的文件不在了：
// 它的行还在暂存区，重新导出即可。已定稿分区的行已不在暂存区，不在此列。
func (s *runState) exportedFileMissing(p staging.PartitionState) bool {
	if p.Status == staging.StatusFinal || p.FilePath == "" {
		return false
	}
	_, err := os.Stat(s.r.Files.Abs(p.FilePath))
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
			Name: info.Name, Type: info.Type, DisplayName: info.DisplayName, Group: info.Group,
			Required: info.Required, Aliases: info.Aliases, Attributes: info.Attributes,
		})
	}
	return out
}
