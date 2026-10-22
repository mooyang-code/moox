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
	// Lookback 是本地还没有某个标的时一次性拉取的根数（回看数量 N）。
	Lookback int
	// Subjects 非空时是集合的标的范围：变更跟进只处理范围内的标的，被移出集合的标的不再同步（设计 6.2）。nil 表示不过滤。
	Subjects map[string]struct{}
}

func (s Spec) inScope(subject string) bool {
	if s.Subjects == nil {
		return true
	}
	_, ok := s.Subjects[subject]
	return ok
}

// Names 是数据源字段名。
func (s Spec) Names() []string { return s.names() }

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

	// pulled 记录最近由拉取写入本地的区间及拉取前的变更日志链头位置 H0（设计 5.2「变更跟进的规则」第 1 条）。
	pulledMu sync.Mutex
	pulled   map[seriesstore.Source]map[string][]pulledSpan
}

// pulledSpan 是一次拉取覆盖的区间 [from, to)：拉取读到的行至少反映了序号不超过 h0 的全部变更。
type pulledSpan struct {
	from, to    time.Time
	h0          uint64
	node, store string
}

// maxPulledPerSubject 限制每个标的保留的拉取记录数（跟进正常推进时每次都会清理，这只是防止跟进长期停摆时无限增长）。
const maxPulledPerSubject = 256

func (s *Syncer) notePulled(src seriesstore.Source, subject string, sp pulledSpan) {
	s.pulledMu.Lock()
	defer s.pulledMu.Unlock()
	if s.pulled == nil {
		s.pulled = map[seriesstore.Source]map[string][]pulledSpan{}
	}
	bySubject := s.pulled[src]
	if bySubject == nil {
		bySubject = map[string][]pulledSpan{}
		s.pulled[src] = bySubject
	}
	list := append(bySubject[subject], sp)
	if len(list) > maxPulledPerSubject {
		list = list[len(list)-maxPulledPerSubject:]
	}
	bySubject[subject] = list
}

// coveredByPull 判断序号为 seq 的变更区间 [from, to) 是否已经被一次拉取反映：拉取前的链头位置不小于该变更的序号，
// 并且拉取覆盖了整个区间。位点之后发生的修正（序号大于 H0）不会被判为已反映。
func (s *Syncer) coveredByPull(src seriesstore.Source, subject string, seq uint64, cursor storageio.ChangeCursor, from, to time.Time) bool {
	s.pulledMu.Lock()
	defer s.pulledMu.Unlock()
	for _, sp := range s.pulled[src][subject] {
		if sp.node == cursor.NodeID && sp.store == cursor.StoreID && sp.h0 >= seq && !sp.from.After(from) && !sp.to.Before(to) {
			return true
		}
	}
	return false
}

// prunePulled 清掉已经不可能再用到的拉取记录：同步位点越过 H0 之后读不到序号更小的变更；身份变了的记录作废。
func (s *Syncer) prunePulled(src seriesstore.Source, cursor storageio.ChangeCursor) {
	s.pulledMu.Lock()
	defer s.pulledMu.Unlock()
	bySubject := s.pulled[src]
	for subject, list := range bySubject {
		kept := list[:0]
		for _, sp := range list {
			if sp.node == cursor.NodeID && sp.store == cursor.StoreID && sp.h0 > cursor.Sequence {
				kept = append(kept, sp)
			}
		}
		if len(kept) == 0 {
			delete(bySubject, subject)
		} else {
			bySubject[subject] = kept
		}
	}
}

// HasPulled 报告数据源下某标的是否还留有拉取记录（观测与测试用）。
func (s *Syncer) HasPulled(src seriesstore.Source, subject string) bool {
	s.pulledMu.Lock()
	defer s.pulledMu.Unlock()
	return len(s.pulled[src][subject]) > 0
}

// ClearPulled 清掉数据源的拉取记录：本地数据被拉取之外的途径覆盖（导入、对账）之后，不能再用「拉取已反映」跳过变更。
func (s *Syncer) ClearPulled(src seriesstore.Source) { s.clearPulled(src) }

func (s *Syncer) clearPulled(src seriesstore.Source) {
	s.pulledMu.Lock()
	defer s.pulledMu.Unlock()
	delete(s.pulled, src)
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
	staged, err := s.PullStaged(ctx, spec, subjects, end)
	if err != nil {
		return res, err
	}
	res.Failed = staged.Failed
	if len(staged.Writes) > 0 {
		written, err := staged.Commit(ctx, staged.Writes)
		if err != nil {
			return res, err
		}
		res.Written = written
	}
	return res, nil
}

// StagedPull 是已经从 Storage 取回、还没写进本地存储的一次拉取：调用方可以先拿 Writes 里的行更新内存窗口、触发计算，
// 再提交写盘（设计 5.2，决定 25 实时尾部并行落盘）。
type StagedPull struct {
	// Writes 是要写入本地的行（按标的排序）。Failed 是读取失败的标的（本期按失败处理，之后补算）。
	Writes []seriesstore.SubjectWrite
	Failed []string

	syncer *Syncer
	spec   Spec
	end    time.Time
	h0     *storageio.ChangeCursor
}

// Commit 把 writes（Writes 的全部或一部分）写进本地存储，同一个 batch 提交，并记下这些区间已被这次拉取反映。
func (sp *StagedPull) Commit(ctx context.Context, writes []seriesstore.SubjectWrite) (map[string]seriesstore.WriteResult, error) {
	out := map[string]seriesstore.WriteResult{}
	if len(writes) == 0 {
		return out, nil
	}
	results, err := sp.syncer.Store.WriteBatch(ctx, sp.spec.Source, writes, seriesstore.WriteOptions{FillGaps: true})
	if err != nil {
		return nil, fmt.Errorf("%w: 写入本地时序存储 %s：%v", storageio.ErrInfra, sp.spec.Source, err)
	}
	for _, r := range results {
		out[r.Subject] = r
	}
	if sp.h0 != nil {
		for _, w := range writes {
			sp.syncer.notePulled(sp.spec.Source, w.Subject, pulledSpan{from: w.From, to: sp.end, h0: sp.h0.Sequence, node: sp.h0.NodeID, store: sp.h0.StoreID})
		}
	}
	return out, nil
}

// PullStaged 做 Pull 的取数部分，不写本地存储。
func (s *Syncer) PullStaged(ctx context.Context, spec Spec, subjects []string, end time.Time) (*StagedPull, error) {
	staged := &StagedPull{syncer: s, spec: spec}
	step, err := seriesstore.Step(spec.Source.Freq)
	if err != nil {
		return nil, err
	}
	end = end.UTC()
	byFrom := map[time.Time][]string{}
	for _, subject := range subjects {
		from := end.Add(-time.Duration(max(spec.Lookback, 1)) * step)
		b, ok, err := s.Store.Bounds(spec.Source, subject)
		if err != nil {
			return nil, err
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
	// 拉取前先取变更日志的链头位置 H0：之后读到的行至少反映了序号不超过 H0 的全部变更，跟进据此跳过已反映的变更。
	// 取不到时不记录，跟进按旧办法比对重拉，不影响正确性。
	var h0 *storageio.ChangeCursor
	if len(jobs) > 0 {
		if head, err := s.Storage.ReadDatasetChanges(ctx, spec.Source.SpaceID, spec.Source.DatasetID, nil, true, 0); err == nil {
			c := head.Next
			h0 = &c
		}
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
		return nil, err
	}
	sort.Slice(writes, func(i, j int) bool { return writes[i].Subject < writes[j].Subject })
	staged.Writes = writes
	staged.end = end
	staged.h0 = h0
	for subject := range failed {
		staged.Failed = append(staged.Failed, subject)
	}
	sort.Strings(staged.Failed)
	return staged, nil
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
	s.clearPulled(src)
	return head.Next, nil
}

// FollowResult 是一次跟进变更的结果。
type FollowResult struct {
	// Applied 是实际改变了本地数据的标的及其写入结果（Changed 是改变的区间）。
	Applied map[string]seriesstore.WriteResult
	// Gap 为真表示同步位点早于日志保留起点或身份对不上：本数据源要走对账。
	Gap bool
	// Stats 是本次跟进的统计，用来观察跟进的开销。
	Stats FollowStats
}

// FollowStats 是一次跟进的统计。
type FollowStats struct {
	// Pages 是读取的变更日志页数；Changes 是读到的变更条数。
	Pages, Changes int
	// Skipped 是因为已被拉取反映而跳过的（标的，区间）数；OutOfScope 是因标的已不在集合内而忽略的数。
	Skipped, OutOfScope int
	// Refetched 是需要重拉比对的（标的，区间）数；Replaced 是比对后内容不同、整段替换的数。
	Refetched, Replaced int
	// Requests 是向 Storage 发出的请求数（含变更日志页与窗口读取）。
	Requests int
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
		res.Stats.Requests++
		if err != nil {
			return res, err
		}
		if page.Gap {
			res.Gap = true
			s.clearPulled(spec.Source)
			return res, nil
		}
		res.Stats.Pages++
		res.Stats.Changes += len(page.Changes)
		applied, err := s.applyChanges(ctx, spec, page, encodeCursor(page.Next), &res.Stats)
		if err != nil {
			return res, err
		}
		s.prunePulled(spec.Source, page.Next)
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

// applyChanges 处理一页变更：先跳过已被拉取反映的、标的已不在集合内的，其余按标的合并区间，批量重拉 Storage、与本地比对，
// 不同的才整段替换；位点与这一页的写入同一个 batch 提交，没有写入时单独推进位点。
func (s *Syncer) applyChanges(ctx context.Context, spec Spec, page storageio.Changes, next []byte, stats *FollowStats) (map[string]seriesstore.WriteResult, error) {
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
			if !spec.inScope(r.Subject) {
				stats.OutOfScope++
				continue
			}
			span := seriesstore.Span{From: r.Start, To: r.End.Add(step)}
			if s.coveredByPull(spec.Source, r.Subject, change.Sequence, page.Next, span.From, span.To) {
				stats.Skipped++
				continue
			}
			spans[r.Subject] = append(spans[r.Subject], span)
		}
	}
	subjects := make([]string, 0, len(spans))
	for subject := range spans {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	var jobs []refetchJob
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
			jobs = append(jobs, refetchJob{subject: subject, lo: lo, hi: hi})
		}
	}
	stats.Refetched += len(jobs)
	writes, err := s.refetchBatch(ctx, spec, jobs, stats)
	if err != nil {
		return nil, err
	}
	stats.Replaced += len(writes)
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

// refetchJob 是一个需要重拉比对的（标的，区间 [lo, hi)）。
type refetchJob struct {
	subject string
	lo, hi  time.Time
}

// refetchBatch 批量重拉并与本地比对（设计 5.2「变更跟进的规则」第 2 条）：区间相同的标的合并成一批，每批最多 batchSubjects 个标的一次请求，
// 批之间按 workers 并行。返回内容与本地不同、要整段替换的写入。
// 保留期之外的数据 Storage 已经清理，本地的历史不能被「Storage 没有」改写成填充行，所以只重拉 Storage 现存的区间。
func (s *Syncer) refetchBatch(ctx context.Context, spec Spec, jobs []refetchJob, stats *FollowStats) ([]seriesstore.SubjectWrite, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	type key struct{ lo, hi time.Time }
	groups := map[key][]string{}
	var keys []key
	for _, j := range jobs {
		k := key{j.lo, j.hi}
		if _, seen := groups[k]; !seen {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], j.subject)
	}
	sort.Slice(keys, func(i, j int) bool {
		if !keys[i].lo.Equal(keys[j].lo) {
			return keys[i].lo.Before(keys[j].lo)
		}
		return keys[i].hi.Before(keys[j].hi)
	})
	type task struct {
		k        key
		subjects []string
		frames   map[string]*storageio.Frame
		err      error
	}
	var tasks []*task
	for _, k := range keys {
		group := groups[k]
		sort.Strings(group)
		for i := 0; i < len(group); i += s.batchSubjects() {
			tasks = append(tasks, &task{k: k, subjects: group[i:min(i+s.batchSubjects(), len(group))]})
		}
	}
	stats.Requests += len(tasks)
	work := make(chan *task)
	var wg sync.WaitGroup
	for i := 0; i < min(s.workers(), len(tasks)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				t.frames, t.err = s.Storage.ReadWindow(ctx, storageio.ReadRequest{
					SpaceID: spec.Source.SpaceID, DatasetID: spec.Source.DatasetID, Freq: spec.Source.Freq,
					Subjects: t.subjects, Start: t.k.lo, End: t.k.hi, Columns: spec.names(),
				})
			}
		}()
	}
	for _, t := range tasks {
		work <- t
	}
	close(work)
	wg.Wait()
	var writes []seriesstore.SubjectWrite
	for _, t := range tasks {
		if t.err != nil {
			return nil, t.err
		}
		for _, subject := range t.subjects {
			remote, err := FrameToColumns(spec.Fields, t.frames[subject])
			if err != nil {
				return nil, err
			}
			lo := t.k.lo
			// Storage 的行不是从区间起点开始（或区间内一行都没有）：可能是被修正删掉了，也可能是保留期已经清理了。
			// 只有这种少见的情形才单独查一次 Storage 的最早时间，来区分；正常的比对不需要。
			if remote.Len() == 0 || time.Unix(remote.Times[0], 0).UTC().After(lo) {
				stats.Requests++
				earliest, has, err := s.Storage.EarliestPeriod(ctx, spec.Source.SpaceID, spec.Source.DatasetID, spec.Source.Freq, []string{subject}, spec.names())
				if err != nil {
					return nil, err
				}
				if !has {
					continue // Storage 完全没有这个标的的数据：本地不动
				}
				if earliest.After(lo) {
					lo = earliest
				}
				if !lo.Before(t.k.hi) {
					continue
				}
			}
			local, err := s.Store.ReadRange(ctx, spec.Source, []string{subject}, lo, t.k.hi)
			if err != nil {
				return nil, err
			}
			if sameRealRows(local[subject], remote) {
				continue
			}
			writes = append(writes, seriesstore.SubjectWrite{Subject: subject, From: lo, To: t.k.hi, Rows: remote})
		}
	}
	sort.SliceStable(writes, func(i, j int) bool {
		if writes[i].Subject != writes[j].Subject {
			return writes[i].Subject < writes[j].Subject
		}
		return writes[i].From.Before(writes[j].From)
	})
	return writes, nil
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
	// 保留期之外的数据 Storage 已经清理：本地的历史（导入的、早先同步的）不能被「Storage 没有」改写成填充行。
	// 只重拉 Storage 现存的区间；Storage 完全没有这个标的的数据时，本地不动。
	earliest, has, err := s.Storage.EarliestPeriod(ctx, spec.Source.SpaceID, spec.Source.DatasetID, spec.Source.Freq, []string{subject}, spec.names())
	if err != nil {
		return seriesstore.SubjectWrite{}, false, err
	}
	if !has {
		return seriesstore.SubjectWrite{}, false, nil
	}
	if earliest.After(lo) {
		lo = earliest
	}
	if !lo.Before(hi) {
		return seriesstore.SubjectWrite{}, false, nil
	}
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

// Resync 对账指定标的在本地数据范围内与 Storage 保留期的重叠区间（不动同步位点）：标的重新加入集合时，
// 它被移出期间 Storage 里的修正没有被跟进，本地保有的历史可能是旧值。内容不同的整段替换并记失效重算。
func (s *Syncer) Resync(ctx context.Context, spec Spec, subjects []string) (map[string]seriesstore.WriteResult, error) {
	step, err := seriesstore.Step(spec.Source.Freq)
	if err != nil {
		return nil, err
	}
	out := map[string]seriesstore.WriteResult{}
	for _, subject := range subjects {
		b, ok, err := s.Store.Bounds(spec.Source, subject)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		write, changed, err := s.refetch(ctx, spec, subject, b.Earliest, b.Latest.Add(step))
		if err != nil {
			return out, err
		}
		if !changed {
			continue
		}
		r, err := s.Store.WriteBatch(ctx, spec.Source, []seriesstore.SubjectWrite{write}, seriesstore.WriteOptions{FillGaps: true, RecordPending: true, PendingReason: "resync"})
		if err != nil {
			return out, fmt.Errorf("%w: 重新对账写入本地 %s/%s：%v", storageio.ErrInfra, spec.Source, subject, err)
		}
		out[subject] = r[0]
	}
	return out, nil
}

// Backfill 把标的在本地最早一根之前的历史从 Storage 补到最多 bars 根（以本地最新一根往前数；Storage 保留期内有多少补多少）。
// 补上的区间与本地最早一根相接，写入时连续；Earliest 前移会自动登记失效重算。没有本地数据的标的不处理（由同步一次性拉取）。
// 用于 Storage 保留期内的历史（回放、较长回看的因子）；保留期之外的历史用导入。返回有写入的标的。
func (s *Syncer) Backfill(ctx context.Context, spec Spec, subjects []string, bars int, progress func(done, total int)) (map[string]seriesstore.WriteResult, error) {
	step, err := seriesstore.Step(spec.Source.Freq)
	if err != nil {
		return nil, err
	}
	if bars < 1 {
		return nil, errors.New("bars 必须为正数")
	}
	column := "close"
	if len(spec.Fields) > 0 {
		column = spec.Fields[0].Name
	}
	written := map[string]seriesstore.WriteResult{}
	for i, subject := range subjects {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		b, ok, err := s.Store.Bounds(spec.Source, subject)
		if err != nil {
			return written, err
		}
		if ok {
			lo := b.Latest.Add(-time.Duration(bars-1) * step)
			hi := b.Earliest
			if lo.Before(hi) {
				first, found, err := s.Storage.EarliestPeriod(ctx, spec.Source.SpaceID, spec.Source.DatasetID, spec.Source.Freq, []string{subject}, []string{column})
				if err != nil {
					return written, err
				}
				if found && first.After(lo) {
					lo = first
				}
				if found && lo.Before(hi) {
					frames, err := s.Storage.ReadWindow(ctx, storageio.ReadRequest{
						SpaceID: spec.Source.SpaceID, DatasetID: spec.Source.DatasetID, Freq: spec.Source.Freq,
						Subjects: []string{subject}, Start: lo, End: hi, Columns: spec.names(),
					})
					if err != nil {
						return written, err
					}
					rows, err := FrameToColumns(spec.Fields, frames[subject])
					if err != nil {
						return written, err
					}
					if rows.Len() > 0 {
						r, err := s.Store.WriteBatch(ctx, spec.Source, []seriesstore.SubjectWrite{{Subject: subject, From: lo, To: hi, Rows: rows}}, seriesstore.WriteOptions{FillGaps: true})
						if err != nil {
							return written, fmt.Errorf("%w: 补历史写入本地 %s/%s：%v", storageio.ErrInfra, spec.Source, subject, err)
						}
						written[subject] = r[0]
					}
				}
			}
		}
		if progress != nil {
			progress(i+1, len(subjects))
		}
	}
	return written, nil
}
