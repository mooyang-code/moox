// Package runner 是导入服务的编排层（设计第 7 节）：展开文件、按格式解析、做通用校验（字段齐全、UTC、按频率对齐、
// 连续性）、按时间边界分批、写到一个或多个写入目标，并记录断点。写入目标（Storage、因子本地存储）由调用方提供。
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/seriesformat"
)

// Chunk 是写给目标的一批行：Batch 的时间落在 [From, To) 内，按时间升序，同一个时间点的所有序列都在同一批里。
type Chunk struct {
	Space, Dataset, Freq, Subject string
	From, To                      time.Time
	Fields                        []seriesformat.Field
	Batch                         *seriesformat.Batch
	// AllowMissing 允许缺的周期写成缺失行（目标自行决定怎么写）。
	AllowMissing bool
	// RecordPending 要求目标把改变的区间记入失效重算待办（带 --recalc 的导入）。
	RecordPending bool
}

// Target 是一个写入目标。同一批重复写入必须幂等（整段替换）。
type Target interface {
	Name() string
	Write(ctx context.Context, chunk Chunk) error
}

// Finisher 是可选的目标能力：所有文件写完之后调用一次（例如 Storage 目标追加同步点、等 View 追平）。
type Finisher interface {
	Finish(ctx context.Context) error
}

// Checkpoints 保存断点：(任务, 文件, 目标) 已经写完的批数。
type Checkpoints interface {
	Get(job, file, target string) (batches, rows int64, done bool, err error)
	Save(job, file, target string, batches, rows int64, done bool, errText string) error
}

// Meta 是调用方提供的数据集信息。
type Meta struct {
	// Fields 是数据集的全部字段（不含时间列与 series_tag）。
	Fields []seriesformat.Field
	// SubjectInScope 判断标的是否在数据集的标签范围内；nil 表示不检查。
	SubjectInScope func(ctx context.Context, subject string) (bool, error)
	// LocalBounds 返回标的在因子机本地的最早与最新时间；nil 表示不知道。文件完全早于本地已有数据（补更早的历史）时，
	// 批要从后往前写：每一批都与已有数据（或刚写的那一批）相接，本地数据才始终连续。
	LocalBounds func(ctx context.Context, subject string) (earliest, latest time.Time, ok bool, err error)
}

// Request 是一次导入。
type Request struct {
	Space, Dataset, Freq string
	// Paths 是要导入的文件，或者包含文件的目录（目录下按格式后缀取全部文件）。
	Paths []string
	// Subject 为空时用文件名（去掉扩展名）作为标的 ID。
	Subject string
	Format  string
	// TimeColumn 是时间列名，默认 data_time；SeriesTag 统一指定 series_tag。
	TimeColumn string
	SeriesTag  string
	// BatchRows 是每批最多行数（不会拆开同一个时间点）。
	BatchRows    int
	AllowMissing bool
	Recalc       bool
	DryRun       bool
	// JobID 是断点标识；为空时按请求内容与文件特征生成稳定的 ID，重跑同一请求自动续传。
	JobID string
	// Step 是频率对应的周期时长。
	Step time.Duration
}

// FileSummary 是一个文件的结果。
type FileSummary struct {
	File    string `json:"file"`
	Subject string `json:"subject"`
	Rows    int    `json:"rows"`
	First   string `json:"first,omitempty"`
	Last    string `json:"last,omitempty"`
	// MissingPeriods 是文件序列内部缺的周期总数；Missing 是其中前若干个。
	MissingPeriods int      `json:"missing_periods,omitempty"`
	Missing        []string `json:"missing,omitempty"`
	// Batches 是每个目标写完的批数。
	Batches map[string]int64 `json:"batches,omitempty"`
	// Skipped 是每个目标因为断点而跳过的批数。
	Skipped map[string]int64 `json:"skipped_batches,omitempty"`
}

// Summary 是一次导入的结果。
type Summary struct {
	JobID  string        `json:"job_id"`
	Status string        `json:"status"`
	Files  []FileSummary `json:"files"`
	Rows   int           `json:"rows"`
}

// Run 执行一次导入。有任何文件不合法时，在写入任何内容之前就返回错误（先校验全部文件，再写）。
func Run(ctx context.Context, req Request, meta Meta, targets []Target, cps Checkpoints) (Summary, error) {
	if err := req.validate(); err != nil {
		return Summary{}, err
	}
	if len(meta.Fields) == 0 {
		return Summary{}, errors.New("数据集没有可导入的字段")
	}
	if !req.DryRun && len(targets) == 0 {
		return Summary{}, errors.New("没有写入目标")
	}
	decoder, err := seriesformat.Lookup(req.Format)
	if err != nil {
		return Summary{}, err
	}
	files, err := expand(req.Paths, req.Format)
	if err != nil {
		return Summary{}, err
	}
	job := req.JobID
	if job == "" {
		job = stableJobID(req, files)
	}
	summary := Summary{JobID: job, Status: "imported"}

	// 第一遍：只校验，不写。
	type plan struct {
		file, subject string
		report        seriesformat.Report
		reverse       bool
	}
	var plans []plan
	for _, file := range files {
		subject := req.Subject
		if subject == "" {
			subject = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		}
		if meta.SubjectInScope != nil {
			ok, err := meta.SubjectInScope(ctx, subject)
			if err != nil {
				return summary, fmt.Errorf("检查标的 %s 的标签范围：%w", subject, err)
			}
			if !ok {
				return summary, fmt.Errorf("标的 %s 不在数据集的标签范围内（不会自动绑定，请先登记标的）", subject)
			}
		}
		report, err := scan(ctx, decoder, file, req, meta)
		if err != nil {
			return summary, fmt.Errorf("%s：%w", file, err)
		}
		if report.Rows == 0 {
			return summary, fmt.Errorf("%s：文件里没有数据行", file)
		}
		if report.MissingCount > 0 && !req.AllowMissing {
			return summary, fmt.Errorf("%s：序列内部缺 %d 个周期，例如 %s（交易所本身缺数据时加 --allow-missing，缺的周期会写成带标记的填充行）", file, report.MissingCount, formatTimes(report.Missing, 3))
		}
		fs := FileSummary{File: file, Subject: subject, Rows: report.Rows, First: report.First.Format(time.RFC3339), Last: report.Last.Format(time.RFC3339), MissingPeriods: report.MissingCount}
		for _, m := range report.Missing {
			fs.Missing = append(fs.Missing, m.Format(time.RFC3339))
		}
		summary.Files = append(summary.Files, fs)
		summary.Rows += report.Rows
		reverse := false
		if meta.LocalBounds != nil {
			earliest, _, ok, err := meta.LocalBounds(ctx, subject)
			if err != nil {
				return summary, fmt.Errorf("读取标的 %s 的本地边界：%w", subject, err)
			}
			reverse = ok && report.Last.Before(earliest)
		}
		plans = append(plans, plan{file: file, subject: subject, report: report, reverse: reverse})
	}
	if req.DryRun {
		summary.Status = "dry_run"
		return summary, nil
	}

	// 第二遍：逐文件、逐目标写入，每批写完记断点。
	for i, p := range plans {
		fs := &summary.Files[i]
		fs.Batches, fs.Skipped = map[string]int64{}, map[string]int64{}
		for _, target := range targets {
			if err := writeFile(ctx, decoder, p.file, p.subject, p.reverse, req, meta, target, cps, job, fs); err != nil {
				summary.Status = "failed"
				if cps != nil {
					_ = cps.Save(job, p.file, target.Name(), fs.Batches[target.Name()]+fs.Skipped[target.Name()], 0, false, err.Error())
				}
				return summary, fmt.Errorf("%s 写入目标 %s：%w", p.file, target.Name(), err)
			}
		}
	}
	for _, target := range targets {
		if fin, ok := target.(Finisher); ok {
			if err := fin.Finish(ctx); err != nil {
				summary.Status = "failed"
				return summary, fmt.Errorf("目标 %s 收尾：%w", target.Name(), err)
			}
		}
	}
	return summary, nil
}

func (r Request) validate() error {
	switch {
	case r.Space == "" || r.Dataset == "" || r.Freq == "":
		return errors.New("space、dataset、freq 都是必填项")
	case len(r.Paths) == 0:
		return errors.New("没有指定要导入的文件或目录")
	case r.Step <= 0:
		return errors.New("缺少频率对应的周期时长")
	case r.Format == "":
		return errors.New("缺少数据格式")
	}
	return nil
}

func (r Request) batchRows() int {
	if r.BatchRows <= 0 {
		return 10000
	}
	return r.BatchRows
}

func (r Request) options(meta Meta) seriesformat.Options {
	return seriesformat.Options{Fields: meta.Fields, TimeColumn: r.TimeColumn, SeriesTag: r.SeriesTag}
}

// scan 流式读完一个文件做通用校验，返回连续性报告。
func scan(ctx context.Context, d seriesformat.Decoder, file string, req Request, meta Meta) (seriesformat.Report, error) {
	f, err := os.Open(file)
	if err != nil {
		return seriesformat.Report{}, err
	}
	defer f.Close()
	r, err := d.Open(f, req.options(meta))
	if err != nil {
		return seriesformat.Report{}, err
	}
	defer r.Close()
	checker := seriesformat.NewChecker(req.Step)
	for {
		if err := ctx.Err(); err != nil {
			return seriesformat.Report{}, err
		}
		batch, err := r.Next(req.batchRows())
		if batch != nil {
			if cerr := checker.Check(batch); cerr != nil {
				return seriesformat.Report{}, cerr
			}
		}
		if errors.Is(err, io.EOF) {
			return checker.Finish(), nil
		}
		if err != nil {
			return seriesformat.Report{}, err
		}
	}
}

// writeFile 把一个文件写到一个目标：按时间边界分批，已经有断点的批跳过。
func writeFile(ctx context.Context, d seriesformat.Decoder, file, subject string, reverse bool, req Request, meta Meta, target Target, cps Checkpoints, job string, fs *FileSummary) error {
	var doneBatches, doneRows int64
	if cps != nil {
		b, rows, done, err := cps.Get(job, file, target.Name())
		if err != nil {
			return err
		}
		if done {
			// 整个文件已经写完：重跑直接跳过。
			fs.Skipped[target.Name()] = b
			return nil
		}
		doneBatches, doneRows = b, rows
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := d.Open(f, req.options(meta))
	if err != nil {
		return err
	}
	defer r.Close()
	var batchNo int64
	var rowsSoFar int64
	write := func(b *seriesformat.Batch) error {
		batchNo++
		rowsSoFar += int64(b.Len())
		if batchNo <= doneBatches {
			fs.Skipped[target.Name()]++
			return nil
		}
		chunk := Chunk{
			Space: req.Space, Dataset: req.Dataset, Freq: req.Freq, Subject: subject,
			From: b.Times[0], To: b.Times[b.Len()-1].Add(req.Step), Fields: meta.Fields, Batch: b,
			AllowMissing: req.AllowMissing, RecordPending: req.Recalc,
		}
		if err := target.Write(ctx, chunk); err != nil {
			return err
		}
		fs.Batches[target.Name()]++
		if cps != nil {
			return cps.Save(job, file, target.Name(), batchNo, rowsSoFar, false, "")
		}
		return nil
	}
	_ = doneRows
	rb := newRebatcher(r, req.batchRows())
	// 方向要在续传时保持一致：倒序写到一半时本地最早时间已经前移，重新判断会变成正序，按断点跳过前几批就会漏掉最早的批。
	// 所以第一次决定倒序时在断点里留一个方向标记，续传时以它为准。
	const dirSuffix = "#reverse"
	if cps != nil {
		if marked, _, _, err := cps.Get(job, file, target.Name()+dirSuffix); err != nil {
			return err
		} else if marked > 0 {
			reverse = true
		} else if reverse {
			if err := cps.Save(job, file, target.Name()+dirSuffix, 1, 0, false, ""); err != nil {
				return err
			}
		}
	}
	if reverse {
		// 补更早的历史：先读出全部批，再从最晚的一批往回写。
		var batches []*seriesformat.Batch
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			b, err := rb.next()
			if b != nil && b.Len() > 0 {
				batches = append(batches, b)
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
		}
		for i := len(batches) - 1; i >= 0; i-- {
			if err := write(batches[i]); err != nil {
				return err
			}
		}
		if cps != nil {
			return cps.Save(job, file, target.Name(), batchNo, rowsSoFar, true, "")
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := rb.next()
		if b != nil && b.Len() > 0 {
			if werr := write(b); werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if cps != nil {
		return cps.Save(job, file, target.Name(), batchNo, rowsSoFar, true, "")
	}
	return nil
}

// rebatcher 按时间边界切批：一批至少 max 行时才结束，并且不拆开同一个时间点的行
// （整段替换按 [起点, 终点) 区间删除所有序列，拆开会让后一批删掉前一批刚写的行）。
type rebatcher struct {
	r     seriesformat.BatchReader
	max   int
	carry *seriesformat.Batch
	eof   bool
}

func newRebatcher(r seriesformat.BatchReader, max int) *rebatcher { return &rebatcher{r: r, max: max} }

func (b *rebatcher) next() (*seriesformat.Batch, error) {
	cur := b.carry
	b.carry = nil
	for {
		// 读到多于 max 行才能确定切点：切点是 max 行之后第一个时间发生变化的位置。
		for !b.eof && (cur == nil || cur.Len() <= b.max) {
			got, err := b.r.Next(b.max)
			if got != nil && got.Len() > 0 {
				cur = appendBatch(cur, got)
			}
			if errors.Is(err, io.EOF) {
				b.eof = true
			} else if err != nil {
				return nil, err
			}
		}
		if cur == nil || cur.Len() == 0 {
			return nil, io.EOF
		}
		if cur.Len() <= b.max {
			return cur, io.EOF // 已经读完，剩下的就是最后一批
		}
		cut := b.max
		for cut < cur.Len() && cur.Times[cut].Equal(cur.Times[cut-1]) {
			cut++
		}
		if cut < cur.Len() {
			head, tail := splitBatch(cur, cut)
			b.carry = tail
			return head, nil
		}
		if b.eof {
			return cur, io.EOF
		}
		// max 行之后还是同一个时间点：继续读，直到时间变化。
		extra, err := b.r.Next(b.max)
		if extra != nil && extra.Len() > 0 {
			cur = appendBatch(cur, extra)
		}
		if errors.Is(err, io.EOF) {
			b.eof = true
		} else if err != nil {
			return nil, err
		}
	}
}

func appendBatch(dst, src *seriesformat.Batch) *seriesformat.Batch {
	if dst == nil {
		return src
	}
	dst.Times = append(dst.Times, src.Times...)
	dst.Tags = append(dst.Tags, src.Tags...)
	for i := range dst.Cols {
		dst.Cols[i].Null = append(dst.Cols[i].Null, src.Cols[i].Null...)
		dst.Cols[i].F = append(dst.Cols[i].F, src.Cols[i].F...)
		dst.Cols[i].I = append(dst.Cols[i].I, src.Cols[i].I...)
		dst.Cols[i].S = append(dst.Cols[i].S, src.Cols[i].S...)
	}
	return dst
}

func splitBatch(b *seriesformat.Batch, at int) (*seriesformat.Batch, *seriesformat.Batch) {
	head := &seriesformat.Batch{Times: b.Times[:at:at], Tags: b.Tags[:at:at], Cols: make([]seriesformat.Column, len(b.Cols))}
	tail := &seriesformat.Batch{Times: append([]time.Time(nil), b.Times[at:]...), Tags: append([]string(nil), b.Tags[at:]...), Cols: make([]seriesformat.Column, len(b.Cols))}
	for i, c := range b.Cols {
		head.Cols[i] = seriesformat.Column{Null: c.Null[:at:at]}
		tail.Cols[i] = seriesformat.Column{Null: append([]bool(nil), c.Null[at:]...)}
		if len(c.F) > 0 {
			head.Cols[i].F, tail.Cols[i].F = c.F[:at:at], append([]float64(nil), c.F[at:]...)
		}
		if len(c.I) > 0 {
			head.Cols[i].I, tail.Cols[i].I = c.I[:at:at], append([]int64(nil), c.I[at:]...)
		}
		if len(c.S) > 0 {
			head.Cols[i].S, tail.Cols[i].S = c.S[:at:at], append([]string(nil), c.S[at:]...)
		}
	}
	return head, tail
}

// expand 把文件与目录展开成排好序的文件列表。
func expand(paths []string, format string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(strings.TrimPrefix(filepath.Ext(e.Name()), "."), format) {
				out = append(out, filepath.Join(p, e.Name()))
			}
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, errors.New("没有找到可导入的文件")
	}
	return out, nil
}

func stableJobID(req Request, files []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%d|%t|%t;", req.Space, req.Dataset, req.Freq, req.Subject, req.TimeColumn, req.SeriesTag, req.batchRows(), req.AllowMissing, req.Recalc)
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s|%d|%d;", f, info.Size(), info.ModTime().UnixNano())
	}
	return "import-" + hex.EncodeToString(h.Sum(nil)[:8])
}

func formatTimes(ts []time.Time, n int) string {
	var parts []string
	for i, t := range ts {
		if i >= n {
			break
		}
		parts = append(parts, t.Format(time.RFC3339))
	}
	return strings.Join(parts, "、")
}
