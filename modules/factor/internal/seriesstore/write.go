package seriesstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/cockroachdb/pebble"
)

// WriteRange 用 w.Rows 整段替换标的在 [w.From, w.To) 内的数据，一次写入在一个 Pebble batch 里原子提交。
func (s *Store) WriteRange(ctx context.Context, src Source, w SubjectWrite, opts WriteOptions) (WriteResult, error) {
	res, err := s.WriteBatch(ctx, src, []SubjectWrite{w}, opts)
	if err != nil {
		return WriteResult{}, err
	}
	return res[0], nil
}

// WriteBatch 把多个标的的整段替换合成一个 Pebble batch 提交（同步器每期对本期所有标的合并成一批）。
// 同一批里的标的必须互不相同。字段表、同步位点和失效重算待办随同一批原子提交。
func (s *Store) WriteBatch(ctx context.Context, src Source, writes []SubjectWrite, opts WriteOptions) ([]WriteResult, error) {
	if err := src.check(); err != nil {
		return nil, err
	}
	g, err := gridFor(src.Freq)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, w := range writes {
		if err := checkSegment("subject", w.Subject); err != nil {
			return nil, err
		}
		if _, dup := seen[w.Subject]; dup {
			return nil, fmt.Errorf("%w：同一批里标的 %s 重复", ErrInvalidWrite, w.Subject)
		}
		if err := checkRegion(src, g, w); err != nil {
			return nil, err
		}
		seen[w.Subject] = struct{}{}
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	snap := s.db.NewSnapshot()
	defer snap.Close()

	old, hasOld, err := s.loadSchema(snap, src)
	if err != nil {
		return nil, err
	}
	fields := old
	schemaChanged := false
	for _, w := range writes {
		if w.Rows == nil || (len(w.Rows.Fields) == 0 && w.Rows.Len() == 0) {
			continue
		}
		merged, changed, err := mergeSchema(fields, w.Rows.Fields)
		if err != nil {
			return nil, err
		}
		fields = merged
		schemaChanged = schemaChanged || changed || !hasOld
	}
	if len(fields) == 0 {
		// 没有字段表又没有任何行：没有可写的内容。
		out := make([]WriteResult, len(writes))
		for i, w := range writes {
			out[i] = WriteResult{Subject: w.Subject}
		}
		return out, nil
	}

	batch := s.db.NewBatch()
	defer batch.Close()
	results := make([]WriteResult, 0, len(writes))
	var pending []PendingSpan
	for _, w := range writes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := s.writeSubject(snap, batch, src, g, fields, w, opts)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
		if opts.RecordPending && !res.PrevLatest.IsZero() && !res.Changed.Empty() && !res.Changed.From.After(res.PrevLatest) {
			pending = append(pending, PendingSpan{Subject: w.Subject, From: res.Changed.From, To: res.Changed.To, Reason: opts.PendingReason})
		}
		// Earliest 前移（补更早的历史）单独触发失效重算（设计 5.3），不受 RecordPending 控制：此前靠历史不足得到空值的结果要重算。
		// 记录新补入的区间，引擎按因子回看向后扩展受影响的周期。
		if !res.PrevEarliest.IsZero() && !res.Earliest.IsZero() && res.Earliest.Before(res.PrevEarliest) {
			pending = append(pending, PendingSpan{Subject: w.Subject, From: res.Earliest, To: res.PrevEarliest, Reason: "earliest_moved"})
		}
	}
	if schemaChanged {
		if err := batch.Set(schemaKey(src), marshalSchema(fields, 1), nil); err != nil {
			return nil, err
		}
	}
	if opts.Cursor != nil {
		if err := batch.Set(syncKey(src), opts.Cursor, nil); err != nil {
			return nil, err
		}
	}
	if len(pending) > 0 {
		cur, err := loadPending(snap, src)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(append(cur, pending...))
		if err := batch.Set(pendingKey(src), raw, nil); err != nil {
			return nil, err
		}
	}
	if err := batch.Commit(s.sync); err != nil {
		return nil, fmt.Errorf("提交本地时序存储 %s：%w", src, err)
	}
	if schemaChanged {
		s.smu.Lock()
		s.schemas[src] = fields
		s.smu.Unlock()
	}
	return results, nil
}

// tagPlan 是一个 series_tag 在一次写入里的改动：要从 [removeLo, removeHi) 删掉的旧行、要插入的行和新的边界。
type tagPlan struct {
	tag                  string
	removeLo, removeHi   int64
	ins                  *Columns
	newE, newL           int64
	hasBounds            bool
	changedLo, changedHi int64
	written, filled      int
	prevL                int64
	hadData              bool
}

func (s *Store) writeSubject(snap kvReader, batch *pebble.Batch, src Source, g grid, fields []Field, w SubjectWrite, opts WriteOptions) (WriteResult, error) {
	res := WriteResult{Subject: w.Subject}
	from, to := w.From.UTC().Unix(), w.To.UTC().Unix()
	rows := NewColumns(fields, 0)
	if w.Rows != nil && w.Rows.Len() > 0 {
		if err := validateRows(w.Rows); err != nil {
			return res, err
		}
		rows = conform(w.Rows, fields).sortRows()
	}
	byTag := map[string][]int{}
	for i := 0; i < rows.Len(); i++ {
		t := rows.Times[i]
		if t < from || t >= to || !g.aligned(t) {
			return res, fmt.Errorf("%w：标的 %s 的行时间 %s 不在 [%s, %s) 内或未按 %s 对齐", ErrInvalidWrite, w.Subject,
				time.Unix(t, 0).UTC().Format(time.RFC3339), w.From.UTC().Format(time.RFC3339), w.To.UTC().Format(time.RFC3339), src.Freq)
		}
		idx := byTag[rows.Tags[i]]
		if n := len(idx); n > 0 && rows.Times[idx[n-1]] == t {
			return res, fmt.Errorf("%w：标的 %s 在 %s 有重复行", ErrInvalidWrite, w.Subject, time.Unix(t, 0).UTC().Format(time.RFC3339))
		}
		byTag[rows.Tags[i]] = append(idx, i)
	}
	existing, err := subjectTags(snap, src, w.Subject)
	if err != nil {
		return res, err
	}
	bounds := map[string]tagBounds{}
	tags := map[string]struct{}{}
	for _, tb := range existing {
		bounds[tb.tag] = tb
		tags[tb.tag] = struct{}{}
	}
	for tag := range byTag {
		tags[tag] = struct{}{}
	}
	ordered := make([]string, 0, len(tags))
	for tag := range tags {
		ordered = append(ordered, tag)
	}
	sort.Strings(ordered)

	var plans []*tagPlan
	var missing []time.Time
	for _, tag := range ordered {
		plan, miss, err := planTag(snap, src, g, fields, w.Subject, tag, rows, byTag[tag], bounds[tag], from, to, opts.FillGaps)
		if err != nil {
			return res, err
		}
		missing = append(missing, miss...)
		plans = append(plans, plan)
	}
	if len(missing) > 0 {
		sort.Slice(missing, func(i, j int) bool { return missing[i].Before(missing[j]) })
		return res, &GapError{Subject: w.Subject, Missing: missing}
	}

	removed, err := applyPlans(snap, batch, src, g, fields, w.Subject, plans)
	if err != nil {
		return res, err
	}
	res.Removed = removed
	var prevE int64
	for _, tb := range existing {
		if tb.exists && (prevE == 0 || tb.e < prevE) {
			prevE = tb.e
		}
	}
	if prevE != 0 {
		res.PrevEarliest = time.Unix(prevE, 0).UTC()
	}
	var allE, allL int64
	haveBounds := false
	cLo, cHi := int64(1<<62), int64(-1<<62)
	for _, p := range plans {
		res.Written += p.written
		res.Filled += p.filled
		key := boundsKey(src, w.Subject, p.tag)
		if p.hasBounds {
			if err := batch.Set(key, encodeBounds(p.newE, p.newL), nil); err != nil {
				return res, err
			}
			if !haveBounds {
				allE, allL, haveBounds = p.newE, p.newL, true
			} else {
				allE, allL = min(allE, p.newE), max(allL, p.newL)
			}
		} else if err := batch.Delete(key, nil); err != nil {
			return res, err
		}
		if p.hadData && (res.PrevLatest.IsZero() || p.prevL > res.PrevLatest.Unix()) {
			res.PrevLatest = time.Unix(p.prevL, 0).UTC()
		}
		if p.changedLo < p.changedHi {
			cLo, cHi = min(cLo, p.changedLo), max(cHi, p.changedHi)
		}
	}
	if haveBounds {
		res.Earliest, res.Latest = time.Unix(allE, 0).UTC(), time.Unix(allL, 0).UTC()
	}
	if cLo < cHi {
		res.Changed = Span{From: time.Unix(cLo, 0).UTC(), To: time.Unix(cHi, 0).UTC()}
	}
	return res, nil
}

// checkRegion 检查写入区间：起点早于终点，且都按频率对齐。
func checkRegion(src Source, g grid, w SubjectWrite) error {
	from, to := w.From.UTC(), w.To.UTC()
	if !from.Before(to) || from.Nanosecond() != 0 || to.Nanosecond() != 0 || !g.aligned(from.Unix()) || !g.aligned(to.Unix()) {
		return fmt.Errorf("%w：标的 %s 的区间 [%s, %s) 必须按 %s 对齐且起点早于终点", ErrInvalidWrite, w.Subject, from.Format(time.RFC3339), to.Format(time.RFC3339), src.Freq)
	}
	return nil
}

func validateRows(c *Columns) error {
	n := c.Len()
	if len(c.Tags) != n || len(c.Filled) != n || len(c.Cols) != len(c.Fields) {
		return fmt.Errorf("%w：行任务的各列长度不一致", ErrInvalidWrite)
	}
	for i, f := range c.Fields {
		col := c.Cols[i]
		if len(col.Null) != n {
			return fmt.Errorf("%w：字段 %s 的空值标记长度不一致", ErrInvalidWrite, f.Name)
		}
		var got int
		switch f.Type {
		case TypeFloat64:
			got = len(col.F)
		case TypeInt64, TypeBool:
			got = len(col.I)
		case TypeString:
			got = len(col.S)
		default:
			return fmt.Errorf("%w：字段 %s 的类型不合法", ErrInvalidWrite, f.Name)
		}
		if got != n {
			return fmt.Errorf("%w：字段 %s 的数据长度 %d 与行数 %d 不一致", ErrInvalidWrite, f.Name, got, n)
		}
	}
	return nil
}

// planTag 计算一个 series_tag 在这次写入里的改动（设计 3.3 与 3.4）。
//
// 本地数据在 [E, L] 上连续。写入 [from, to) 之后：
//   - 左段 [E, min(L, from-1步)] 保持不动；右段 [max(E, to), L] 保持不动，只有紧跟 to 的连续填充行要按新的前值重新生成；
//   - 新行与缺的周期（间隙、区间内缺的）按前一行生成填充行，需要 FillGaps，重新生成原有的填充行不需要。
func planTag(r kvReader, src Source, g grid, fields []Field, subject, tag string, rows *Columns, idx []int, tb tagBounds, from, to int64, fillGaps bool) (*tagPlan, []time.Time, error) {
	step := g.step
	p := &tagPlan{tag: tag, removeLo: from, removeHi: to, hadData: tb.exists, ins: NewColumns(fields, len(idx))}
	if tb.exists {
		p.prevL = tb.l
	}
	leftExists := tb.exists && tb.e <= min(tb.l, from-step)
	leftEnd := int64(0)
	if leftExists {
		leftEnd = min(tb.l, from-step)
	}
	rightExists, rs := false, int64(0)
	if tb.exists {
		if s0 := max(tb.e, to); s0 <= tb.l {
			rightExists, rs = true, s0
		}
	}
	hiRemove := to
	if rightExists {
		run, err := filledRun(r, src, g, fields, subject, tag, rs, tb.l)
		if err != nil {
			return nil, nil, err
		}
		hiRemove = rs + int64(run)*step
		p.removeHi = hiRemove
	}

	var prev []Value
	if leftExists {
		row, err := readRowAt(r, src, g, fields, subject, tag, leftEnd)
		if err != nil {
			return nil, nil, err
		}
		if row == nil {
			return nil, nil, fmt.Errorf("%s/%s 的数据在 %s 处应当存在却读不到（本地数据不连续）", src, subject, time.Unix(leftEnd, 0).UTC().Format(time.RFC3339))
		}
		prev = row
	}

	var insLo, insHi int64
	haveIns := false
	switch {
	case leftExists:
		insLo = leftEnd + step
	case len(idx) > 0:
		insLo = rows.Times[idx[0]]
	case rightExists:
		insLo = rs
	default:
		insLo = to
	}
	switch {
	case rightExists:
		insHi = hiRemove - step
	case len(idx) > 0:
		insHi = rows.Times[idx[len(idx)-1]]
	default:
		insHi = insLo - step
	}
	var missing []time.Time
	k := 0
	for t := insLo; t <= insHi; t += step {
		if k < len(idx) && rows.Times[idx[k]] == t {
			p.ins.copyRow(rows, idx[k])
			prev = rowVector(rows, idx[k])
			p.written++
			k++
			haveIns = true
			continue
		}
		if prev == nil {
			continue // 没有前一行可以填充：序列从第一根真实行开始
		}
		regenerated := rightExists && t >= rs
		if !regenerated && !fillGaps {
			missing = append(missing, time.Unix(t, 0).UTC())
		}
		prev = fillRow(p.ins, fields, t, tag, prev)
		p.filled++
		haveIns = true
	}

	rightKept := rightExists && hiRemove <= tb.l
	switch {
	case leftExists:
		p.newE, p.hasBounds = tb.e, true
	case p.ins.Len() > 0:
		p.newE, p.hasBounds = p.ins.Times[0], true
	case rightKept:
		p.newE, p.hasBounds = hiRemove, true
	}
	switch {
	case rightKept:
		p.newL = tb.l
	case p.ins.Len() > 0:
		p.newL = p.ins.Times[p.ins.Len()-1]
	case leftExists:
		p.newL = leftEnd
	default:
		p.hasBounds = false
	}
	p.changedLo, p.changedHi = from, p.removeHi
	if haveIns {
		p.changedLo = min(p.changedLo, insLo)
		p.changedHi = max(p.changedHi, insHi+step)
	}
	return p, missing, nil
}

// rowVector 取一行的全部值。
func rowVector(c *Columns, row int) []Value {
	out := make([]Value, len(c.Fields))
	for i := range out {
		out[i] = c.ValueAt(i, row)
	}
	return out
}

// fillRow 按各字段的填充规则，用上一行 prev 生成 t 处的填充行追加到 dst，返回这一行的值（作为再下一行的前值）。
func fillRow(dst *Columns, fields []Field, t int64, tag string, prev []Value) []Value {
	closeIdx := -1
	for i, f := range fields {
		if f.Name == CloseField {
			closeIdx = i
			break
		}
	}
	values := make([]Value, len(fields))
	for i, f := range fields {
		switch f.Fill {
		case FillZero:
			switch f.Type {
			case TypeFloat64, TypeInt64, TypeBool:
				values[i] = Value{}
			default:
				values[i] = NullValue
			}
		case FillPrevClose:
			if closeIdx >= 0 {
				values[i] = convertValue(prev[closeIdx], fields[closeIdx].Type, f.Type)
			} else {
				values[i] = prev[i]
			}
		default:
			values[i] = prev[i]
		}
	}
	dst.appendRaw(t, tag, true)
	for i, v := range values {
		dst.appendValue(i, v)
	}
	return values
}

// convertValue 把数值在 float64 与 int64 之间转换；类型不同又无法转换时返回 NULL。
func convertValue(v Value, from, to FieldType) Value {
	if v.Null || from == to {
		return v
	}
	switch {
	case from == TypeFloat64 && (to == TypeInt64):
		return Value{I: int64(v.F)}
	case from == TypeInt64 && to == TypeFloat64:
		return Value{F: float64(v.I)}
	}
	return NullValue
}

// readRowAt 读标的某个 series_tag 在 t 处的一行的全部值，没有返回 nil。
func readRowAt(r kvReader, src Source, g grid, fields []Field, subject, tag string, t int64) ([]Value, error) {
	blk, err := readBlockAt(r, src, subject, g.blockStart(t), fields)
	if err != nil || blk == nil {
		return nil, err
	}
	for i := 0; i < blk.Len(); i++ {
		if blk.Times[i] == t && blk.Tags[i] == tag {
			return rowVector(blk, i), nil
		}
	}
	return nil, nil
}

// filledRun 数出从 rs 起连续的填充行个数（最多到 l）。
func filledRun(r kvReader, src Source, g grid, fields []Field, subject, tag string, rs, l int64) (int, error) {
	run := 0
	t := rs
	for bs := g.blockStart(rs); t <= l; bs = g.nextBlock(bs) {
		blk, err := readBlockAt(r, src, subject, bs, fields)
		if err != nil {
			return 0, err
		}
		if blk == nil {
			return run, nil
		}
		for i := 0; i < blk.Len() && t <= l; i++ {
			if blk.Tags[i] != tag || blk.Times[i] < t {
				continue
			}
			if blk.Times[i] != t || !blk.Filled[i] {
				return run, nil
			}
			run++
			t += g.step
		}
	}
	return run, nil
}

// applyPlans 重写受影响的块：删掉各 series_tag 要移除的旧行、放进要插入的行，返回被替换掉的旧行数。
func applyPlans(r kvReader, batch *pebble.Batch, src Source, g grid, fields []Field, subject string, plans []*tagPlan) (int, error) {
	cLo, cHi := int64(1<<62), int64(-1<<62)
	for _, p := range plans {
		if p.changedLo < p.changedHi {
			cLo, cHi = min(cLo, p.changedLo), max(cHi, p.changedHi)
		}
	}
	if cLo >= cHi {
		return 0, nil
	}
	byTag := make(map[string]*tagPlan, len(plans))
	for _, p := range plans {
		byTag[p.tag] = p
	}
	removed := 0
	for bs := g.blockStart(cLo); bs < cHi; bs = g.nextBlock(bs) {
		blk, err := readBlockAt(r, src, subject, bs, fields)
		if err != nil {
			return 0, err
		}
		next := g.nextBlock(bs)
		out := NewColumns(fields, 0)
		if blk != nil {
			for i := 0; i < blk.Len(); i++ {
				if p := byTag[blk.Tags[i]]; p != nil && blk.Times[i] >= p.removeLo && blk.Times[i] < p.removeHi {
					removed++
					continue
				}
				out.copyRow(blk, i)
			}
		}
		inserted := false
		for _, p := range plans {
			lo := sort.Search(p.ins.Len(), func(i int) bool { return p.ins.Times[i] >= bs })
			for i := lo; i < p.ins.Len() && p.ins.Times[i] < next; i++ {
				out.copyRow(p.ins, i)
				inserted = true
			}
		}
		key := blockKey(src, subject, bs)
		switch {
		case out.Len() == 0 && blk != nil:
			if err := batch.Delete(key, nil); err != nil {
				return 0, err
			}
		case out.Len() > 0 && (inserted || blk != nil):
			if err := batch.Set(key, encodeBlock(out.sortRows()), nil); err != nil {
				return 0, err
			}
		}
	}
	return removed, nil
}
