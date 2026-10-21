// Package seriessync 把 Storage 的近期数据同步到因子机的本地时序存储（设计第 5 节）：
// 每期只拉增量、启动时补停机缺口、按 Storage 的行变更序号跟进修正、断档时由对账重建基线。
// 本地数据在写入时保证连续，所以「同步到哪里了」只看本地 Latest。
package seriessync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// Storage 是同步用到的 Storage 能力（storageio.Client 实现）。
type Storage interface {
	ReadWindow(ctx context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error)
	ReadDatasetChanges(ctx context.Context, spaceID, datasetID string, cursor *storageio.ChangeCursor, fromHead bool, limit int) (storageio.Changes, error)
	EarliestPeriod(ctx context.Context, spaceID, datasetID, freq string, subjects, columns []string) (time.Time, bool, error)
}

// Spec 描述一个数据源怎么同步。
type Spec struct {
	Source seriesstore.Source
	Fields []seriesstore.Field
	// Lookback 是本地还没有某个标的时一次性拉取的根数（集合内各因子的最大回看根数）。
	Lookback int
}

func (s Spec) names() []string {
	out := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		out[i] = f.Name
	}
	return out
}

// Syncer 是同步器。同一个数据源的同步、跟进变更、对账必须由调用方串行执行。
type Syncer struct {
	Store   *seriesstore.Store
	Storage Storage
	// Workers 是并行读取 Storage 的批数；BatchSubjects 是每批标的数（Storage 单次最多 100 个 selector）。
	Workers       int
	BatchSubjects int
	// ChangePageSize 是每次读取变更日志的条数。
	ChangePageSize int
}

func (s *Syncer) workers() int       { return max(s.Workers, 1) }
func (s *Syncer) batchSubjects() int { return min(max(s.BatchSubjects, 1), 100) }
func (s *Syncer) pageSize() int {
	if s.ChangePageSize > 0 {
		return s.ChangePageSize
	}
	return 500
}

// ErrNoBaseline 表示数据源还没有同步位点：要先取基线（Baseline）。
var ErrNoBaseline = errors.New("数据源还没有变更日志的同步位点")

// ---------------------------------------------------------------- 拉取增量

// PullResult 是一次拉取的结果。
type PullResult struct {
	// Written 是有写入的标的及其写入结果。
	Written map[string]seriesstore.WriteResult
	// Failed 是读取或写入失败的标的（本期按失败处理，之后补算）。
	Failed []string
}

// Pull 把标的从本地 Latest 之后的数据拉到 end（不含）；本地没有的标的一次性拉 Lookback 根。
// 缺的周期在写入时按字段填充规则写成缺失行。一次拉取的所有标的在同一个本地 batch 里提交。
func (s *Syncer) Pull(ctx context.Context, spec Spec, subjects []string, end time.Time) (PullResult, error) {
	res := PullResult{Written: map[string]seriesstore.WriteResult{}}
	step, err := seriesstore.Step(spec.Source.Freq)
	if err != nil {
		return res, err
	}
	end = end.UTC()
	byFrom := map[time.Time][]string{}
	for _, subject := range subjects {
		from := end.Add(-time.Duration(max(spec.Lookback, 1)) * step)
		b, ok, err := s.Store.Bounds(spec.Source, subject)
		if err != nil {
			return res, err
		}
		if ok {
			from = b.Latest.Add(step)
		}
		if !from.Before(end) {
			continue
		}
		byFrom[from] = append(byFrom[from], subject)
	}
	type job struct {
		from     time.Time
		subjects []string
	}
	var jobs []job
	froms := make([]time.Time, 0, len(byFrom))
	for from := range byFrom {
		froms = append(froms, from)
	}
	sort.Slice(froms, func(i, j int) bool { return froms[i].Before(froms[j]) })
	for _, from := range froms {
		group := byFrom[from]
		sort.Strings(group)
		for i := 0; i < len(group); i += s.batchSubjects() {
			jobs = append(jobs, job{from: from, subjects: group[i:min(i+s.batchSubjects(), len(group))]})
		}
	}

	type fetched struct {
		subject string
		from    time.Time
		rows    *seriesstore.Columns
		err     error
	}
	results := make(chan fetched, len(subjects))
	work := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < min(s.workers(), max(len(jobs), 1)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range work {
				frames, err := s.Storage.ReadWindow(ctx, storageio.ReadRequest{
					SpaceID: spec.Source.SpaceID, DatasetID: spec.Source.DatasetID, Freq: spec.Source.Freq,
					Subjects: j.subjects, Start: j.from, End: end, Columns: spec.names(),
				})
				for _, subject := range j.subjects {
					if err != nil {
						results <- fetched{subject: subject, err: err}
						continue
					}
					cols, convErr := FrameToColumns(spec.Fields, frames[subject])
					results <- fetched{subject: subject, from: j.from, rows: cols, err: convErr}
				}
			}
		}()
	}
	go func() {
		defer close(work)
		for _, j := range jobs {
			select {
			case work <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	var writes []seriesstore.SubjectWrite
	failed := map[string]struct{}{}
	for r := range results {
		switch {
		case r.err != nil:
			failed[r.subject] = struct{}{}
		case r.rows.Len() > 0:
			last := r.rows.Times[r.rows.Len()-1]
			writes = append(writes, seriesstore.SubjectWrite{Subject: r.subject, From: r.from, To: time.Unix(last, 0).UTC().Add(step), Rows: r.rows})
		}
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if len(writes) > 0 {
		sort.Slice(writes, func(i, j int) bool { return writes[i].Subject < writes[j].Subject })
		results, err := s.Store.WriteBatch(ctx, spec.Source, writes, seriesstore.WriteOptions{FillGaps: true})
		if err != nil {
			return res, fmt.Errorf("%w: 写入本地时序存储 %s：%v", storageio.ErrInfra, spec.Source, err)
		}
		for _, r := range results {
			res.Written[r.Subject] = r
		}
	}
	for subject := range failed {
		res.Failed = append(res.Failed, subject)
	}
	sort.Strings(res.Failed)
	return res, nil
}

// ---------------------------------------------------------------- 变更日志

func encodeCursor(c storageio.ChangeCursor) []byte {
	return []byte(c.NodeID + "\x00" + c.StoreID + "\x00" + strconv.FormatUint(c.Sequence, 10))
}

func decodeCursor(raw []byte) (storageio.ChangeCursor, error) {
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 {
		return storageio.ChangeCursor{}, errors.New("同步位点已损坏")
	}
	seq, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return storageio.ChangeCursor{}, errors.New("同步位点已损坏")
	}
	return storageio.ChangeCursor{NodeID: parts[0], StoreID: parts[1], Sequence: seq}, nil
}

// Cursor 返回数据源当前的同步位点。
func (s *Syncer) Cursor(src seriesstore.Source) (storageio.ChangeCursor, bool, error) {
	raw, ok, err := s.Store.SyncCursor(src)
	if err != nil || !ok {
		return storageio.ChangeCursor{}, false, err
	}
	c, err := decodeCursor(raw)
	return c, err == nil, err
}

// Baseline 取 Storage 变更日志头部的位点作为同步基线。必须先取位点、再读数据：之后从这个位点继续读变更，
// 取位点与读数据之间发生的写入不会被漏掉。
func (s *Syncer) Baseline(ctx context.Context, src seriesstore.Source) (storageio.ChangeCursor, error) {
	head, err := s.Storage.ReadDatasetChanges(ctx, src.SpaceID, src.DatasetID, nil, true, 0)
	if err != nil {
		return storageio.ChangeCursor{}, err
	}
	if err := s.Store.SetSyncCursor(src, encodeCursor(head.Next)); err != nil {
		return storageio.ChangeCursor{}, err
	}
	return head.Next, nil
}

// FollowResult 是一次跟进变更的结果。
type FollowResult struct {
	// Applied 是实际改变了本地数据的标的及其写入结果（Changed 是改变的区间）。
	Applied map[string]seriesstore.WriteResult
	// Gap 为真表示同步位点早于日志保留起点或身份对不上：本数据源要走对账。
	Gap bool
}

// Follow 从同步位点之后按序读取 Storage 的行变更，对本地已有的区间重拉并整段替换；位点随写入原子推进。
// 变更落在本地最新之后的是新数据，由 Pull 补；落在本地没有的标的上的忽略。内容与本地相同的变更不写入、不记失效重算。
func (s *Syncer) Follow(ctx context.Context, spec Spec) (FollowResult, error) {
	res := FollowResult{Applied: map[string]seriesstore.WriteResult{}}
	cursor, ok, err := s.Cursor(spec.Source)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ErrNoBaseline
	}
	for {
		page, err := s.Storage.ReadDatasetChanges(ctx, spec.Source.SpaceID, spec.Source.DatasetID, &cursor, false, s.pageSize())
		if err != nil {
			return res, err
		}
		if page.Gap {
			res.Gap = true
			return res, nil
		}
		applied, err := s.applyChanges(ctx, spec, page, encodeCursor(page.Next))
		if err != nil {
			return res, err
		}
		for subject, r := range applied {
			if prev, seen := res.Applied[subject]; seen {
				r.Changed = unionSpan(prev.Changed, r.Changed)
			}
			res.Applied[subject] = r
		}
		cursor = page.Next
		if !page.HasMore {
			return res, nil
		}
	}
}

func unionSpan(a, b seriesstore.Span) seriesstore.Span {
	if a.Empty() {
		return b
	}
	if b.Empty() {
		return a
	}
	out := a
	if b.From.Before(out.From) {
		out.From = b.From
	}
	if b.To.After(out.To) {
		out.To = b.To
	}
	return out
}

// applyChanges 处理一页变更：按标的合并区间，重拉 Storage、与本地比对，不同的才整段替换；
// 位点与这一页的写入同一个 batch 提交，没有写入时单独推进位点。
func (s *Syncer) applyChanges(ctx context.Context, spec Spec, page storageio.Changes, next []byte) (map[string]seriesstore.WriteResult, error) {
	step, err := seriesstore.Step(spec.Source.Freq)
	if err != nil {
		return nil, err
	}
	spans := map[string][]seriesstore.Span{}
	for _, change := range page.Changes {
		if change.DatasetID != spec.Source.DatasetID {
			continue
		}
		for _, r := range change.Ranges {
			if r.Freq != spec.Source.Freq {
				continue
			}
			spans[r.Subject] = append(spans[r.Subject], seriesstore.Span{From: r.Start, To: r.End.Add(step)})
		}
	}
	var writes []seriesstore.SubjectWrite
	subjects := make([]string, 0, len(spans))
	for subject := range spans {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	for _, subject := range subjects {
		b, ok, err := s.Store.Bounds(spec.Source, subject)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // 本地没有这个标的：用到时一次性拉取
		}
		localEnd := b.Latest.Add(step)
		for _, span := range mergeSpans(spans[subject]) {
			lo, hi := span.From, span.To
			if lo.Before(b.Earliest) {
				lo = b.Earliest
			}
			if hi.After(localEnd) {
				hi = localEnd // 本地最新之后的是新数据，由 Pull 补
			}
			if !lo.Before(hi) {
				continue
			}
			write, changed, err := s.refetch(ctx, spec, subject, lo, hi)
			if err != nil {
				return nil, err
			}
			if changed {
				writes = append(writes, write)
			}
		}
	}
	out := map[string]seriesstore.WriteResult{}
	opts := seriesstore.WriteOptions{FillGaps: true, Cursor: next, RecordPending: true, PendingReason: "storage_change"}
	if len(writes) == 0 {
		return out, s.Store.SetSyncCursor(spec.Source, next)
	}
	// 同一个标的的多个不相交区间合成一次写入会让区间变大；这里逐段提交，位点随最后一批。
	for i, w := range writes {
		o := opts
		if i != len(writes)-1 {
			o.Cursor = nil
		}
		results, err := s.Store.WriteBatch(ctx, spec.Source, []seriesstore.SubjectWrite{w}, o)
		if err != nil {
			return nil, fmt.Errorf("%w: 应用 Storage 变更到本地 %s/%s：%v", storageio.ErrInfra, spec.Source, w.Subject, err)
		}
		r := results[0]
		if prev, seen := out[w.Subject]; seen {
			r.Changed = unionSpan(prev.Changed, r.Changed)
		}
		out[w.Subject] = r
	}
	return out, nil
}

func mergeSpans(in []seriesstore.Span) []seriesstore.Span {
	sort.Slice(in, func(i, j int) bool { return in[i].From.Before(in[j].From) })
	var out []seriesstore.Span
	for _, sp := range in {
		if n := len(out); n > 0 && !sp.From.After(out[n-1].To) {
			if sp.To.After(out[n-1].To) {
				out[n-1].To = sp.To
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

// refetch 重拉 [lo, hi) 并与本地比对；不同才返回要写入的整段替换。
func (s *Syncer) refetch(ctx context.Context, spec Spec, subject string, lo, hi time.Time) (seriesstore.SubjectWrite, bool, error) {
	frames, err := s.Storage.ReadWindow(ctx, storageio.ReadRequest{
		SpaceID: spec.Source.SpaceID, DatasetID: spec.Source.DatasetID, Freq: spec.Source.Freq,
		Subjects: []string{subject}, Start: lo, End: hi, Columns: spec.names(),
	})
	if err != nil {
		return seriesstore.SubjectWrite{}, false, err
	}
	remote, err := FrameToColumns(spec.Fields, frames[subject])
	if err != nil {
		return seriesstore.SubjectWrite{}, false, err
	}
	local, err := s.Store.ReadRange(ctx, spec.Source, []string{subject}, lo, hi)
	if err != nil {
		return seriesstore.SubjectWrite{}, false, err
	}
	if sameRealRows(local[subject], remote) {
		return seriesstore.SubjectWrite{}, false, nil
	}
	return seriesstore.SubjectWrite{Subject: subject, From: lo, To: hi, Rows: remote}, true, nil
}

// sameRealRows 比较本地的真实行（不含填充行）与 Storage 的行是否完全相同。
func sameRealRows(local, remote *seriesstore.Columns) bool {
	var l []int
	if local != nil {
		for i := 0; i < local.Len(); i++ {
			if !local.Filled[i] {
				l = append(l, i)
			}
		}
	}
	if len(l) != remote.Len() {
		return false
	}
	for r, i := range l {
		if local.Times[i] != remote.Times[r] || local.Tags[i] != remote.Tags[r] {
			return false
		}
		for f := range remote.Fields {
			j := local.FieldIndex(remote.Fields[f].Name)
			if j < 0 {
				return false
			}
			a, b := local.ValueAt(j, i), remote.ValueAt(f, r)
			if a != b && !(a.F != a.F && b.F != b.F) {
				return false
			}
		}
	}
	return true
}

// ---------------------------------------------------------------- 对账

// ReconcileResult 是对账的结果。
type ReconcileResult struct {
	Subjects int
	// Replaced 是内容与 Storage 不同、被整段替换的标的及其改变的区间。
	Replaced map[string]seriesstore.WriteResult
	// Followed 是对账期间 Storage 又发生的变更里应用到本地的部分。
	Followed map[string]seriesstore.WriteResult
}

// Reconcile 重建同步基线（设计 5.2「对账命令」）：先取日志当前位点 C0；对「本地数据范围 ∩ Storage 仍保留范围」逐标的重拉并比对，
// 不同的整段替换；全部完成后把位点设为 C0、补读 C0 之后的变更并应用，再发布新基线。Storage 保留期之外的本地数据不动，
// 空结果不当作删除。subjects 为空表示本地有数据的全部标的。progress 可为 nil。
func (s *Syncer) Reconcile(ctx context.Context, spec Spec, subjects []string, progress func(done, total int)) (ReconcileResult, error) {
	res := ReconcileResult{Replaced: map[string]seriesstore.WriteResult{}}
	step, err := seriesstore.Step(spec.Source.Freq)
	if err != nil {
		return res, err
	}
	head, err := s.Storage.ReadDatasetChanges(ctx, spec.Source.SpaceID, spec.Source.DatasetID, nil, true, 0)
	if err != nil {
		return res, err
	}
	if len(subjects) == 0 {
		if subjects, err = s.Store.Subjects(spec.Source); err != nil {
			return res, err
		}
	}
	column := "close"
	if len(spec.Fields) > 0 {
		column = spec.Fields[0].Name
	}
	for i, subject := range subjects {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		b, ok, err := s.Store.Bounds(spec.Source, subject)
		if err != nil {
			return res, err
		}
		if ok {
			earliest, found, err := s.Storage.EarliestPeriod(ctx, spec.Source.SpaceID, spec.Source.DatasetID, spec.Source.Freq, []string{subject}, []string{column})
			if err != nil {
				return res, err
			}
			if found {
				lo := b.Earliest
				if earliest.After(lo) {
					lo = earliest
				}
				hi := b.Latest.Add(step)
				if lo.Before(hi) {
					write, changed, err := s.refetch(ctx, spec, subject, lo, hi)
					if err != nil {
						return res, err
					}
					if changed {
						r, err := s.Store.WriteBatch(ctx, spec.Source, []seriesstore.SubjectWrite{write}, seriesstore.WriteOptions{FillGaps: true, RecordPending: true, PendingReason: "reconcile"})
						if err != nil {
							return res, fmt.Errorf("%w: 对账写入本地 %s/%s：%v", storageio.ErrInfra, spec.Source, subject, err)
						}
						res.Replaced[subject] = r[0]
					}
				}
			}
		}
		res.Subjects++
		if progress != nil {
			progress(i+1, len(subjects))
		}
	}
	if err := s.Store.SetSyncCursor(spec.Source, encodeCursor(head.Next)); err != nil {
		return res, err
	}
	follow, err := s.Follow(ctx, spec)
	if err != nil {
		return res, err
	}
	if follow.Gap {
		return res, errors.New("对账期间变更日志再次断档，请重新执行对账")
	}
	res.Followed = follow.Applied
	return res, nil
}
