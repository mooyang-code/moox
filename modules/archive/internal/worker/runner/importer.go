package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
)

// importChunkRows 是第二遍每次写入暂存区的行数（不拆开同一 data_time）；测试里改小。
var importChunkRows = 50000

const (
	// kindImport 是导入 moox-cli 上传的本地 CSV 的运行。
	kindImport = "import"
	// importReportRows、importReportEvery：两遍都每 1 万行或每 30 秒更新一次进度（续租由 App 的定时器按最新进度上报）。
	importReportRows  = 10000
	importReportEvery = 30 * time.Second
	// maxImportErrors 是运行消息里列出的错误条数。
	maxImportErrors = 20
)

// ImportPath 把下发的 import_file（"<import_id>/<文件名>"）解析成 <root>/.import/<import_id> 与其中的文件；
// 两段都必须是安全的路径片段，".import" 是字面量，不接受其他位置。
func ImportPath(root, file string) (dir, path string, err error) {
	id, name, ok := strings.Cut(file, "/")
	if !ok || export.CheckComponent(id) != nil || export.CheckComponent(name) != nil {
		return "", "", fmt.Errorf("导入文件 %q 无效：应是 <import_id>/<文件名>", file)
	}
	dir = filepath.Join(root, export.ImportDirName, id)
	return dir, filepath.Join(dir, name), nil
}

// importLayout 是导入文件的表头：每列在数据集里的位置。
type importLayout struct {
	header []string
	// time、subject、tag 是三个系统列在表头里的下标（tag 为 -1 表示没有这一列，取 defaultTag）。
	time, subject, tag int
	// values[i] 是第 i 个值列（task.Columns 的顺序）在表头里的下标，-1 表示文件里没有（写入 NULL，覆盖时不允许）。
	values []int
	// knownTags 是数据集已有的 series_tag 取值（最多 2 个）；只有一个时缺列或空值自动补它、填了别的值就拒绝；
	// 多个时只要求文件带 series_tag 列，不校验取值。
	knownTags []string
}

// importError 是第一遍校验收集的错误（只保留前 maxImportErrors 条，总数另计）。
type importErrors struct {
	items []string
	total int
}

func (e *importErrors) add(line int, format string, args ...any) {
	e.total++
	if len(e.items) < maxImportErrors {
		e.items = append(e.items, fmt.Sprintf("第 %d 行："+format, append([]any{line}, args...)...))
	}
}

func (e *importErrors) err() error {
	if e.total == 0 {
		return nil
	}
	head := fmt.Sprintf("导入文件有 %d 处错误，整份拒绝，什么都没有写入", e.total)
	if e.total > len(e.items) {
		head += fmt.Sprintf("（只列前 %d 条）", len(e.items))
	}
	return errors.New(head + "：" + strings.Join(e.items, "；"))
}

// runImport 执行 kind=import：先整份校验（不写库），通过后分块写入暂存区；每块之后导出、上传、定稿已经写完的分区，
// 暂存行不随文件变大而堆积；最后再全量处理一次。导入目录由 App 在 mgr 确认运行成功之后删除，失败的保留供核对
// （维护时删除超过 7 天的）。
func (s *runState) runImport(ctx context.Context, out *Outcome) error {
	_, path, err := ImportPath(s.r.Files.Root, s.a.GetImportFile())
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("找不到导入文件 %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("导入文件 %s 不是普通文件", path)
	}
	if s.a.GetOverwrite() {
		s.mode = staging.StageReplace
	} else {
		s.mode = staging.StageFillGaps
	}
	layout, err := s.importLayout(ctx, path)
	if err != nil {
		return err
	}
	total, partitions, err := s.validateImport(path, layout)
	if err != nil {
		return err
	}
	if err := s.writeImport(ctx, out, path, layout); err != nil {
		return err
	}
	s.progress.SubjectsDone = uint64(len(s.subjects))
	if err := s.settleImport(ctx, out, time.Time{}); err != nil {
		return err
	}
	verdict := "未覆盖"
	if s.a.GetOverwrite() {
		verdict = "已覆盖"
	}
	differ := s.differ + s.changed
	message := fmt.Sprintf("导入 %d 行（新增 %d、已存在相同 %d、已存在不同 %d %s），涉及 %d 个分区", total, s.added, s.same, differ, verdict, partitions)
	if extra := s.successMessage(); extra != "" {
		message += "；" + extra
	}
	out.Message = message
	return nil
}

// settleImport 导出、上传并定稿结束不晚于 until 的分区（until 为零表示全部）。
func (s *runState) settleImport(ctx context.Context, out *Outcome, until time.Time) error {
	if err := s.exportDue(ctx, out, until); err != nil {
		return err
	}
	s.uploadPending(ctx, out)
	return s.finalize(ctx, out, false, until)
}

// knownSeriesTags 返回数据集已有的 series_tag 取值（最多 2 个）：先看暂存表，没有暂存行时读最近一个本地分区文件。
func (s *runState) knownSeriesTags(ctx context.Context) ([]string, error) {
	tags, err := s.r.Staging.DistinctSeriesTags(ctx, s.task, 2)
	if err != nil || len(tags) > 0 {
		return tags, err
	}
	parts, err := s.r.Staging.Partitions(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil {
		return nil, err
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].FilePath == "" {
			continue
		}
		abs := s.r.Files.Abs(parts[i].FilePath)
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		return staging.FileSeriesTags(abs, 2)
	}
	return nil, nil
}

// importLayout 读表头并核对列名：data_time、subject_id 必须有，其余必须是数据集的值列或 series_tag，不能重复；
// 覆盖时必须带齐全部值列（覆盖是整行替换，缺列会清空已有值）；没有 series_tag 列时数据集必须恰好有一个已知取值。
func (s *runState) importLayout(ctx context.Context, path string) (importLayout, error) {
	reader, err := staging.OpenCSV(path)
	if err != nil {
		return importLayout{}, fmt.Errorf("打开导入文件: %w", err)
	}
	defer reader.Close()
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return importLayout{}, errors.New("导入文件是空的")
	}
	if err != nil {
		return importLayout{}, fmt.Errorf("读取导入文件表头: %w", err)
	}
	layout := importLayout{time: -1, subject: -1, tag: -1, values: make([]int, len(s.task.Columns))}
	for i := range layout.values {
		layout.values[i] = -1
	}
	index := map[string]int{}
	for i, c := range s.task.Columns {
		index[c.Name] = i
	}
	seen := map[string]bool{}
	var unknown []string
	for i, cell := range header {
		name := strings.TrimSpace(cell.Value)
		if i == 0 {
			// Excel 等工具另存的 UTF-8 CSV 开头带 BOM。
			name = strings.TrimPrefix(name, "\ufeff")
		}
		layout.header = append(layout.header, name)
		if seen[name] {
			return importLayout{}, fmt.Errorf("表头里列 %q 重复", name)
		}
		seen[name] = true
		switch name {
		case "data_time":
			layout.time = i
		case "subject_id":
			layout.subject = i
		case "series_tag":
			layout.tag = i
		default:
			at, ok := index[name]
			if !ok {
				unknown = append(unknown, name)
				continue
			}
			layout.values[at] = i
		}
	}
	if len(unknown) > 0 {
		return importLayout{}, fmt.Errorf("表头里有数据集没有的列：%s", strings.Join(unknown, "、"))
	}
	if layout.time < 0 || layout.subject < 0 {
		return importLayout{}, errors.New("表头必须有 data_time 与 subject_id 两列")
	}
	if s.a.GetOverwrite() {
		var missing []string
		for i, at := range layout.values {
			if at < 0 {
				missing = append(missing, s.task.Columns[i].Name)
			}
		}
		if len(missing) > 0 {
			return importLayout{}, fmt.Errorf("覆盖导入必须带齐数据集的全部值列（覆盖是整行替换，缺列会清空已有值），缺少：%s", strings.Join(missing, "、"))
		}
	}
	if layout.knownTags, err = s.knownSeriesTags(ctx); err != nil {
		return importLayout{}, fmt.Errorf("查询数据集已有的 series_tag: %w", err)
	}
	if layout.tag < 0 && len(layout.knownTags) != 1 {
		if len(layout.knownTags) == 0 {
			return importLayout{}, errors.New("数据集还没有可参照的 series_tag（暂存库与本地分区文件里都没有），导入文件必须带 series_tag 列")
		}
		return importLayout{}, fmt.Errorf("数据集的 series_tag 不止一个取值（%s 等），导入文件必须带 series_tag 列", strings.Join(layout.knownTags, "、"))
	}
	return layout, nil
}

// parseImportRow 把一行解析成暂存行；错误只描述原因，行号由调用方加。
func (s *runState) parseImportRow(layout importLayout, record []staging.Cell) (staging.Row, error) {
	if len(record) != len(layout.header) {
		return staging.Row{}, fmt.Errorf("有 %d 个字段，表头是 %d 列", len(record), len(layout.header))
	}
	timeCell := record[layout.time]
	if timeCell.Null || timeCell.Value == "" {
		return staging.Row{}, errors.New("data_time 为空")
	}
	value, err := staging.ParseCell(staging.TypeTime, timeCell)
	if err != nil {
		return staging.Row{}, fmt.Errorf("data_time %w", err)
	}
	at := value.(time.Time)
	if !s.aligned(at) {
		return staging.Row{}, fmt.Errorf("data_time %s 没有按频率 %s 对齐", timeCell.Value, s.a.GetFreq())
	}
	subject := record[layout.subject]
	if subject.Null || strings.TrimSpace(subject.Value) == "" {
		return staging.Row{}, errors.New("subject_id 为空")
	}
	row := staging.Row{SubjectID: subject.Value, DataTime: at, Values: make([]any, len(s.task.Columns))}
	switch {
	case layout.tag >= 0 && !record[layout.tag].Null:
		row.SeriesTag = record[layout.tag].Value
		if len(layout.knownTags) == 1 && row.SeriesTag != layout.knownTags[0] {
			return staging.Row{}, fmt.Errorf("series_tag %q 与数据集的 %q 不一致", row.SeriesTag, layout.knownTags[0])
		}
	case len(layout.knownTags) == 1:
		row.SeriesTag = layout.knownTags[0]
	case len(layout.knownTags) == 0:
		// 带了列但留空、数据集又没有可参照的取值：按空字符串（与未填序列标签的数据一致）。
		row.SeriesTag = ""
	default:
		return staging.Row{}, fmt.Errorf("series_tag 为空，而数据集不止一个取值（%s 等）", strings.Join(layout.knownTags, "、"))
	}
	for i, at := range layout.values {
		if at < 0 {
			continue
		}
		parsed, err := staging.ParseCell(s.task.Columns[i].Type, record[at])
		if err != nil {
			return staging.Row{}, fmt.Errorf("列 %s %w", s.task.Columns[i].Name, err)
		}
		row.Values[i] = parsed
	}
	return row, nil
}

// aligned 报告数据时间是否按频率对齐：1d 及以上是空间时区的本地零点，其余是 Unix 纪元起频率时长的整数倍。
func (s *runState) aligned(at time.Time) bool {
	if daily, err := schedule.Daily(s.a.GetFreq()); err == nil && daily {
		local := at.In(s.loc)
		return local.Hour() == 0 && local.Minute() == 0 && local.Second() == 0 && local.Nanosecond() == 0
	}
	return s.span > 0 && at.UnixNano()%int64(s.span) == 0
}

// importReporter 每 importReportRows 行或 importReportEvery 更新一次进度，返回 true 表示运行已被取消。
type importReporter struct {
	s    *runState
	last time.Time
	rows int
}

func (p *importReporter) tick(rows uint64) bool {
	p.s.progress.RowsRead = rows
	p.rows++
	if p.rows < importReportRows && p.s.r.now().Sub(p.last) < importReportEvery {
		return false
	}
	p.rows, p.last = 0, p.s.r.now()
	p.s.progress.SubjectsDone = uint64(len(p.s.subjects))
	return p.s.report != nil && p.s.report(p.s.progress)
}

// blankRecord 报告一条记录是不是空行（读出来只有一个缺失值字段）。
func blankRecord(record []staging.Cell) bool {
	return len(record) == 1 && record[0].Null
}

// importChunkHook 在第二遍每块写入并处理完已写完的分区之后调用（测试用：检查暂存行、模拟中途出错）。
var importChunkHook func(s *runState, last time.Time) error

// validateImport 第一遍：流式解析整份文件（不写库），检查每行能解析、按频率对齐、按 data_time 升序、
// 同一 data_time 内主键不重复；有错就整份拒绝，返回前 20 条错误。文件末尾的空行忽略，中间的空行算错误。
// 返回行数与涉及的分区数。
func (s *runState) validateImport(path string, layout importLayout) (rows uint64, partitions int, err error) {
	reader, err := staging.OpenCSV(path)
	if err != nil {
		return 0, 0, err
	}
	defer reader.Close()
	if _, err := reader.Read(); err != nil {
		return 0, 0, err
	}
	var problems importErrors
	keys := map[string]bool{}
	type key struct{ subject, tag string }
	var last time.Time
	group := map[key]int{}
	reporter := &importReporter{s: s, last: s.r.now()}
	var records uint64
	blank := 0 // 最近一个还没被后续数据行“确认为中间空行”的空行行号
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("第 %d 行：%w", reader.Line(), err)
		}
		line := reader.Line()
		// 每条记录都计数并检查取消（解析失败的也算），错误很多的大文件也能及时停下。
		records++
		if reporter.tick(records) {
			return 0, 0, errStopped
		}
		if blankRecord(record) {
			if blank == 0 {
				blank = line
			}
			continue
		}
		if blank != 0 {
			problems.add(blank, "空行（只有文件末尾的空行会被忽略）")
			blank = 0
		}
		row, perr := s.parseImportRow(layout, record)
		if perr != nil {
			problems.add(line, "%v", perr)
			continue
		}
		rows++
		switch {
		case row.DataTime.Before(last):
			problems.add(line, "data_time %s 早于上一行的 %s，文件必须按 data_time 升序", row.DataTime.Format(time.RFC3339Nano), last.Format(time.RFC3339Nano))
			continue
		case row.DataTime.After(last):
			last, group = row.DataTime, map[key]int{}
		}
		k := key{row.SubjectID, row.SeriesTag}
		if first, dup := group[k]; dup {
			problems.add(line, "与第 %d 行的主键重复（%s、%s、%q）", first, row.DataTime.Format(time.RFC3339Nano), row.SubjectID, row.SeriesTag)
			continue
		}
		group[k] = line
		keys[s.partitionOf(row.DataTime).Key] = true
	}
	if err := problems.err(); err != nil {
		return 0, 0, err
	}
	if rows == 0 {
		return 0, 0, errors.New("导入文件只有表头，没有数据行")
	}
	return rows, len(keys), nil
}

// writeImport 第二遍：每 importChunkRows 行一块写入暂存区（不拆开同一 data_time，同一主键不会落在两块里），
// 落在已定稿分区时先读回原文件。每块之后导出、上传、定稿结束不晚于本块最后一行的分区（文件按 data_time 升序，
// 之后的行不会再落进这些分区），横跨下一块的分区留到之后。中途出错时运行消息写明已写入多少行。
func (s *runState) writeImport(ctx context.Context, out *Outcome, path string, layout importLayout) (err error) {
	var staged uint64
	defer func() {
		if err == nil || errors.Is(err, errStopped) || errors.Is(err, context.Canceled) {
			return
		}
		hint := "可用同一文件不带 --overwrite 重新导入补齐（已写入的行会算作已存在相同）"
		if s.a.GetOverwrite() {
			hint = "可用同一文件再次 --overwrite 导入"
		}
		err = fmt.Errorf("%w（已写入前 %d 行；%s）", err, staged, hint)
	}()
	reader, err := staging.OpenCSV(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	if _, err := reader.Read(); err != nil {
		return err
	}
	reporter := &importReporter{s: s, last: s.r.now()}
	var chunk []staging.Row
	var read uint64
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		last := chunk[len(chunk)-1].DataTime
		if err := s.stage(ctx, chunk); err != nil {
			return err
		}
		staged += uint64(len(chunk))
		chunk = chunk[:0]
		if err := s.settleImport(ctx, out, last); err != nil {
			return err
		}
		if importChunkHook != nil {
			return importChunkHook(s, last)
		}
		return nil
	}
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("第 %d 行：%w", reader.Line(), err)
		}
		if blankRecord(record) {
			continue // 第一遍已确认只有文件末尾才有空行
		}
		row, err := s.parseImportRow(layout, record)
		if err != nil {
			// 第一遍已经整份校验过；文件在两遍之间被改动才会走到这里。
			return fmt.Errorf("第 %d 行：%w（导入文件在校验之后被改动？）", reader.Line(), err)
		}
		if len(chunk) >= importChunkRows && row.DataTime.After(chunk[len(chunk)-1].DataTime) {
			if err := flush(); err != nil {
				return err
			}
		}
		chunk = append(chunk, row)
		read++
		if reporter.tick(read) {
			return errStopped
		}
	}
	return flush()
}
