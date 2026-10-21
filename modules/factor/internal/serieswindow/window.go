// Package serieswindow 是因子引擎内存里的列式滑动窗口（设计第 4 节）：每个数据源一份，按序列（标的 × series_tag）
// 保存最近 Lookback 根的数值字段，本地数据连续，所以序列的时间轴由起点和步长隐含，不存时间数组，也没有缺行。
//
// 每个序列的数组容量 = Lookback + 余量：追加只写余量、最旧的一根自然移出可见范围；余量用尽时复制成新一代数组再切换，
// 旧一代留给仍在读取的租约，由 GC 回收。读租约（Acquire）只是在加锁时抓取各序列的切片头，之后无锁读取，
// 追加与换代都不会改动租约可见范围内的数据。
package serieswindow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
)

// FilledColumn 是表达式里可以引用的虚拟列：该行是填充行时为 1，否则为 0（设计 3.4）。
const FilledColumn = "is_filled"

var (
	// ErrDiscontinuous 表示追加的行与窗口末尾不连续，调用方应当重新载入该标的。
	ErrDiscontinuous = errors.New("追加的行与窗口末尾不连续")
	// ErrSchemaChanged 表示数据集字段变了，需要重新载入整个窗口。
	ErrSchemaChanged = errors.New("数据集字段已变化，窗口需要重新载入")
)

// Config 是一个数据源窗口的参数。
type Config struct {
	Source seriesstore.Source
	// Lookback 是窗口保留的根数（集合内各因子的最大回看根数）。
	Lookback int
	// Headroom 是数组余量的根数；0 取 max(Lookback/4, 16)。
	Headroom int
}

type seriesKey struct{ subject, tag string }

type series struct {
	cols   [][]float64
	filled []bool
	start  int   // 第一个可见行在数组里的下标
	n      int   // 可见行数
	end    int64 // 最后一个可见行的时间（UTC 秒）
}

// Window 是一个数据源的内存窗口。
type Window struct {
	cfg    Config
	step   int64
	cap    int
	fields []string // 窗口里的数值字段，顺序固定
	index  map[string]int

	mu        sync.RWMutex
	series    map[seriesKey]*series
	bySubject map[string][]string // 标的 -> 排好序的 tag
}

// New 创建窗口。fields 是要保留的数值字段（INT、BOOL 转成 float64）。
func New(cfg Config, stepSeconds int64, fields []string) (*Window, error) {
	if cfg.Lookback < 1 {
		return nil, fmt.Errorf("窗口回看根数必须为正数，得到 %d", cfg.Lookback)
	}
	if stepSeconds <= 0 {
		return nil, errors.New("窗口步长必须为正数")
	}
	if cfg.Headroom <= 0 {
		cfg.Headroom = max(cfg.Lookback/4, 16)
	}
	w := &Window{cfg: cfg, step: stepSeconds, cap: cfg.Lookback + cfg.Headroom, fields: append([]string(nil), fields...), index: map[string]int{},
		series: map[seriesKey]*series{}, bySubject: map[string][]string{}}
	for i, name := range fields {
		if _, dup := w.index[name]; dup {
			return nil, fmt.Errorf("窗口字段 %q 重复", name)
		}
		w.index[name] = i
	}
	return w, nil
}

// NumericFields 从数据集字段表里取出能放进窗口的数值字段名。
func NumericFields(fields []seriesstore.Field) []string {
	var out []string
	for _, f := range fields {
		switch f.Type {
		case seriesstore.TypeFloat64, seriesstore.TypeInt64, seriesstore.TypeBool:
			out = append(out, f.Name)
		}
	}
	return out
}

// Source 返回窗口对应的数据源。
func (w *Window) Source() seriesstore.Source { return w.cfg.Source }

// Lookback 返回窗口保留的根数。
func (w *Window) Lookback() int { return w.cfg.Lookback }

// Fields 返回窗口保留的数值字段。
func (w *Window) Fields() []string { return append([]string(nil), w.fields...) }

// BytesPerSeries 估算一个序列占用的内存：数值列 + 填充标记，按数组容量计。
func BytesPerSeries(lookback, headroom, numericFields int) int64 {
	if headroom <= 0 {
		headroom = max(lookback/4, 16)
	}
	return int64(lookback+headroom) * int64(numericFields*8+1)
}

// Stats 是窗口的实际占用。
type Stats struct {
	Series int
	Rows   int
	Bytes  int64
}

// Stats 返回窗口当前的序列数、行数与占用字节数。
func (w *Window) Stats() Stats {
	w.mu.RLock()
	defer w.mu.RUnlock()
	st := Stats{Series: len(w.series)}
	for _, s := range w.series {
		st.Rows += s.n
		st.Bytes += int64(cap(s.filled)) * int64(len(w.fields)*8+1)
	}
	return st
}

// Subjects 返回窗口里有数据的标的（已排序）。
func (w *Window) Subjects() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]string, 0, len(w.bySubject))
	for subject := range w.bySubject {
		out = append(out, subject)
	}
	sort.Strings(out)
	return out
}

// Has 报告窗口里是否有该标的。
func (w *Window) Has(subject string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.bySubject[subject]
	return ok
}

// Latest 返回标的在窗口里最新一根的时间（各序列取最大）。
func (w *Window) Latest(subject string) (time.Time, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	tags, ok := w.bySubject[subject]
	if !ok {
		return time.Time{}, false
	}
	var end int64
	for _, tag := range tags {
		end = max(end, w.series[seriesKey{subject, tag}].end)
	}
	return time.Unix(end, 0).UTC(), true
}

// ---------------------------------------------------------------- 载入

// Load 从本地存储载入标的各序列最近 Lookback 根；已有的序列整体替换。没有本地数据的标的不在窗口里。
func (w *Window) Load(ctx context.Context, store *seriesstore.Store, subjects []string) error {
	workers := min(max(runtime.GOMAXPROCS(0), 1), 8)
	type result struct {
		subject string
		built   map[string]*series
		err     error
	}
	jobs := make(chan string)
	results := make(chan result, len(subjects))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for subject := range jobs {
				built, err := w.build(ctx, store, subject)
				results <- result{subject: subject, built: built, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, subject := range subjects {
			select {
			case jobs <- subject:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	var firstErr error
	var done []result
	for r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		done = append(done, r)
	}
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range done {
		w.replaceSubjectLocked(r.subject, r.built)
	}
	return nil
}

// Reload 重新载入一个标的（修正覆盖了窗口里的区间之后）。
func (w *Window) Reload(ctx context.Context, store *seriesstore.Store, subject string) error {
	return w.Load(ctx, store, []string{subject})
}

// build 读出标的最近 Lookback 根并按序列放进数组。
func (w *Window) build(ctx context.Context, store *seriesstore.Store, subject string) (map[string]*series, error) {
	b, ok, err := store.Bounds(w.cfg.Source, subject)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	from := b.Latest.Add(-time.Duration(int64(w.cfg.Lookback-1)*w.step) * time.Second)
	if from.Before(b.Earliest) {
		from = b.Earliest
	}
	got, err := store.ReadRange(ctx, w.cfg.Source, []string{subject}, from, b.Latest.Add(time.Second))
	if err != nil {
		return nil, err
	}
	cols := got[subject]
	if cols == nil {
		return nil, nil
	}
	return w.fromColumns(subject, cols)
}

// fromColumns 把一批（按时间、标签排序的）行拆成各序列的数组。
func (w *Window) fromColumns(subject string, cols *seriesstore.Columns) (map[string]*series, error) {
	byTag := map[string][]int{}
	for i := 0; i < cols.Len(); i++ {
		byTag[cols.Tags[i]] = append(byTag[cols.Tags[i]], i)
	}
	out := make(map[string]*series, len(byTag))
	fieldIdx := make([]int, len(w.fields))
	for i, name := range w.fields {
		fieldIdx[i] = cols.FieldIndex(name)
	}
	for tag, rows := range byTag {
		if len(rows) > w.cfg.Lookback {
			rows = rows[len(rows)-w.cfg.Lookback:]
		}
		s := &series{cols: make([][]float64, len(w.fields)), filled: make([]bool, w.cap)}
		for i := range s.cols {
			s.cols[i] = make([]float64, w.cap)
		}
		for r, row := range rows {
			if r > 0 && cols.Times[row]-cols.Times[rows[r-1]] != w.step {
				return nil, fmt.Errorf("%s/%s 的本地数据在 %s 之后不连续", w.cfg.Source, subject, time.Unix(cols.Times[rows[r-1]], 0).UTC().Format(time.RFC3339))
			}
			w.putRow(s, r, cols, row, fieldIdx)
		}
		s.start, s.n, s.end = 0, len(rows), cols.Times[rows[len(rows)-1]]
		out[tag] = s
	}
	return out, nil
}

// putRow 把 cols 的第 row 行写到 s 的数组下标 at。
func (w *Window) putRow(s *series, at int, cols *seriesstore.Columns, row int, fieldIdx []int) {
	for i, fi := range fieldIdx {
		v := math.NaN()
		if fi >= 0 && !cols.Cols[fi].Null[row] {
			switch cols.Fields[fi].Type {
			case seriesstore.TypeFloat64:
				v = cols.Cols[fi].F[row]
			case seriesstore.TypeInt64, seriesstore.TypeBool:
				v = float64(cols.Cols[fi].I[row])
			}
		}
		s.cols[i][at] = v
	}
	s.filled[at] = cols.Filled[row]
}

func (w *Window) replaceSubjectLocked(subject string, built map[string]*series) {
	for _, tag := range w.bySubject[subject] {
		delete(w.series, seriesKey{subject, tag})
	}
	delete(w.bySubject, subject)
	if len(built) == 0 {
		return
	}
	tags := make([]string, 0, len(built))
	for tag, s := range built {
		w.series[seriesKey{subject, tag}] = s
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	w.bySubject[subject] = tags
}

// Drop 从窗口移除标的。
func (w *Window) Drop(subject string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.replaceSubjectLocked(subject, nil)
}

// ---------------------------------------------------------------- 追加

// Append 把紧接着窗口末尾的新行追加进标的各序列（同步器写盘之后调用）。rows 按（时间、标签）排序、各序列连续。
// 序列还不在窗口里、或第一行与末尾不连续时返回 ErrDiscontinuous，调用方改用 Reload。
func (w *Window) Append(subject string, rows *seriesstore.Columns) error {
	if rows.Len() == 0 {
		return nil
	}
	for _, name := range w.fields {
		if rows.FieldIndex(name) < 0 {
			return ErrSchemaChanged
		}
	}
	for _, f := range rows.Fields {
		numeric := f.Type == seriesstore.TypeFloat64 || f.Type == seriesstore.TypeInt64 || f.Type == seriesstore.TypeBool
		if _, known := w.index[f.Name]; numeric && !known {
			return ErrSchemaChanged
		}
	}
	fieldIdx := make([]int, len(w.fields))
	for i, name := range w.fields {
		fieldIdx[i] = rows.FieldIndex(name)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// 先整体检查连续性，再写：失败时窗口保持原样。
	last := map[string]int64{}
	for i := 0; i < rows.Len(); i++ {
		tag := rows.Tags[i]
		s := w.series[seriesKey{subject, tag}]
		if s == nil {
			return ErrDiscontinuous
		}
		prev, seen := last[tag]
		if !seen {
			prev = s.end
		}
		if rows.Times[i] != prev+w.step {
			return ErrDiscontinuous
		}
		last[tag] = rows.Times[i]
	}
	for i := 0; i < rows.Len(); i++ {
		s := w.series[seriesKey{subject, rows.Tags[i]}]
		if s.start+s.n == w.cap {
			s = w.compactLocked(subject, rows.Tags[i], s)
		}
		w.putRow(s, s.start+s.n, rows, i, fieldIdx)
		if s.n < w.cfg.Lookback {
			s.n++
		} else {
			s.start++
		}
		s.end = rows.Times[i]
	}
	return nil
}

// compactLocked 余量用尽：把可见的行复制到新一代数组的开头并切换，旧一代留给仍在读取的租约。
func (w *Window) compactLocked(subject, tag string, old *series) *series {
	fresh := &series{cols: make([][]float64, len(old.cols)), filled: make([]bool, w.cap), n: old.n, end: old.end}
	for i := range old.cols {
		fresh.cols[i] = make([]float64, w.cap)
		copy(fresh.cols[i], old.cols[i][old.start:old.start+old.n])
	}
	copy(fresh.filled, old.filled[old.start:old.start+old.n])
	w.series[seriesKey{subject, tag}] = fresh
	return fresh
}

// ---------------------------------------------------------------- 读租约

// SeriesView 是一个序列在租约时刻的只读视图；切片指向窗口的数组，窗口之后的追加与换代不会改动其中的数据。
type SeriesView struct {
	Subject, Tag string
	Start        int64 // 第一个可见行的时间（UTC 秒）
	N            int
	Cols         [][]float64 // 与 View.Fields 一一对应，长度都是 N；null 是 NaN
	Filled       []bool
}

// End 返回最后一个可见行的时间。
func (s SeriesView) End(step int64) int64 { return s.Start + int64(s.N-1)*step }

// View 是一次读租约抓到的列式数据：各标的的序列（已按标签排序）。
type View struct {
	Step      int64
	Fields    []string
	fieldIdx  map[string]int
	bySubject map[string][]SeriesView
}

// Acquire 抓取标的各序列截至 endSec（含）的视图。窗口里没有的标的不在视图里。
func (w *Window) Acquire(subjects []string, endSec int64) *View {
	v := &View{Step: w.step, Fields: w.fields, fieldIdx: w.index, bySubject: make(map[string][]SeriesView, len(subjects))}
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, subject := range subjects {
		for _, tag := range w.bySubject[subject] {
			s := w.series[seriesKey{subject, tag}]
			n := s.n
			if s.end > endSec {
				n -= int((s.end - endSec) / w.step)
			}
			if n <= 0 {
				continue
			}
			sv := SeriesView{Subject: subject, Tag: tag, Start: s.end - int64(s.n-1)*w.step, N: n, Filled: s.filled[s.start : s.start+n]}
			sv.Cols = make([][]float64, len(s.cols))
			for i := range s.cols {
				sv.Cols[i] = s.cols[i][s.start : s.start+n]
			}
			v.bySubject[subject] = append(v.bySubject[subject], sv)
		}
	}
	return v
}

// FromColumns 用一次本地读取的结果构造视图（补算、重算、回放预热读磁盘区间时用）；每个标的的行按（时间、标签）排序，
// 各序列必须连续。fields 是要放进视图的数值字段。
func FromColumns(stepSeconds int64, fields []string, cols map[string]*seriesstore.Columns) (*View, error) {
	w := &Window{step: stepSeconds, fields: fields, index: map[string]int{}, cfg: Config{Lookback: math.MaxInt32}}
	for i, name := range fields {
		w.index[name] = i
	}
	v := &View{Step: stepSeconds, Fields: fields, fieldIdx: w.index, bySubject: make(map[string][]SeriesView, len(cols))}
	fieldIdx := func(c *seriesstore.Columns) []int {
		idx := make([]int, len(fields))
		for i, name := range fields {
			idx[i] = c.FieldIndex(name)
		}
		return idx
	}
	for subject, c := range cols {
		byTag := map[string][]int{}
		for i := 0; i < c.Len(); i++ {
			byTag[c.Tags[i]] = append(byTag[c.Tags[i]], i)
		}
		tags := make([]string, 0, len(byTag))
		for tag := range byTag {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		idx := fieldIdx(c)
		for _, tag := range tags {
			rows := byTag[tag]
			s := &series{cols: make([][]float64, len(fields)), filled: make([]bool, len(rows))}
			for i := range s.cols {
				s.cols[i] = make([]float64, len(rows))
			}
			for r, row := range rows {
				if r > 0 && c.Times[row]-c.Times[rows[r-1]] != stepSeconds {
					return nil, fmt.Errorf("标的 %s 序列 %q 的本地数据在 %s 之后不连续", subject, tag, time.Unix(c.Times[rows[r-1]], 0).UTC().Format(time.RFC3339))
				}
				w.putRow(s, r, c, row, idx)
			}
			sv := SeriesView{Subject: subject, Tag: tag, Start: c.Times[rows[0]], N: len(rows), Filled: s.filled, Cols: s.cols}
			v.bySubject[subject] = append(v.bySubject[subject], sv)
		}
	}
	return v, nil
}

// Subjects 返回视图里的标的（已排序）。
func (v *View) Subjects() []string {
	out := make([]string, 0, len(v.bySubject))
	for subject := range v.bySubject {
		out = append(out, subject)
	}
	sort.Strings(out)
	return out
}

// Series 返回标的的各序列（按标签排序）。
func (v *View) Series(subject string) []SeriesView { return v.bySubject[subject] }

// Len 是视图里的序列总数。
func (v *View) Len() int {
	n := 0
	for _, s := range v.bySubject {
		n += len(s)
	}
	return n
}

// FieldIndex 返回数值字段在 SeriesView.Cols 里的下标。
func (v *View) FieldIndex(name string) (int, bool) {
	i, ok := v.fieldIdx[name]
	return i, ok
}

// Latest 返回标的各序列最新一根的时间（取最大）。
func (v *View) Latest(subject string) (int64, bool) {
	series := v.bySubject[subject]
	if len(series) == 0 {
		return 0, false
	}
	end := series[0].End(v.Step)
	for _, s := range series[1:] {
		end = max(end, s.End(v.Step))
	}
	return end, true
}
