package pebble

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// 读取上限。区间读取和最近 N 根的页大小不同：区间读取按时间归并输出，最近 N 根按序列整段取。
const (
	maxReadRowsKeys       = 10000
	maxSelectors          = 100
	maxCountSelectors     = 2000
	defaultRangePageSize  = 1000
	maxRangePageSize      = 2000
	defaultLatestPageSize = 20000
	maxLatestPageSize     = 20000
	maxLatestPageValues   = 200000
	maxLatestPerSeries    = 10000
)

// seriesSelector 选中一个标的的一个频率；tag 为空且 hasTag 为 false 表示该标的的全部序列。
type seriesSelector struct {
	subject, freq, tag string
	hasTag             bool
}

type seriesID struct{ subject, freq, tag string }

func (a seriesID) less(b seriesID) bool {
	if a.subject != b.subject {
		return a.subject < b.subject
	}
	if a.freq != b.freq {
		return a.freq < b.freq
	}
	return a.tag < b.tag
}

func parseSelectors(req []*pb.TimeSeriesSelector, spaceID, datasetID string, limit int) ([]seriesSelector, error) {
	if len(req) > limit {
		return nil, invalidf("selectors exceed the limit of %d", limit)
	}
	out := make([]seriesSelector, 0, len(req))
	for _, selector := range req {
		if selector == nil || selector.GetSubjectId() == "" || selector.GetFreq() == "" {
			return nil, invalid("selector subject_id and freq are required")
		}
		if selector.GetSpaceId() != "" && selector.GetSpaceId() != spaceID {
			return nil, invalid("selector space_id does not match request")
		}
		if selector.GetDatasetId() != "" && selector.GetDatasetId() != datasetID {
			return nil, invalid("selector dataset_id does not match request")
		}
		out = append(out, seriesSelector{subject: selector.GetSubjectId(), freq: selector.GetFreq(), tag: selector.GetSeriesTag(), hasTag: selector.SeriesTag != nil})
	}
	return out, nil
}

func parseTimeRange(tr *pb.TimeRange) (start, end time.Time, err error) {
	if tr == nil {
		return start, end, nil
	}
	if raw := strings.TrimSpace(tr.GetStartTime()); raw != "" {
		if start, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return start, end, invalidf("invalid time_range.start_time: %w", err)
		}
		start = start.UTC()
	}
	if raw := strings.TrimSpace(tr.GetEndTime()); raw != "" {
		if end, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return start, end, invalidf("invalid time_range.end_time: %w", err)
		}
		end = end.UTC()
	}
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		return start, end, invalid("time_range.start_time must be before end_time")
	}
	return start, end, nil
}

func encodeRowsCursor(cursor *pb.RowsCursor) (string, error) {
	raw, err := proto.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeRowsCursor(raw string) (*pb.RowsCursor, error) {
	if raw == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalid("invalid page cursor")
	}
	cursor := &pb.RowsCursor{}
	if err := proto.Unmarshal(data, cursor); err != nil {
		return nil, invalid("invalid page cursor")
	}
	return cursor, nil
}

// ReadTimeSeriesRows 读取时序事实，有两种形态：时间区间（左闭右开）或每个序列最近 N 根。
// 不带 selector 表示数据集的全部标的。分页用游标，只能顺序向后翻页。
// 整个请求使用同一个快照：先在索引上取出这一页的键，再读这些键的字段值。
func (s *Store) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return nil, invalid("space_id and dataset_id are required")
	}
	if len(req.GetColumnNames()) == 0 {
		return nil, invalid("column_names are required")
	}
	selectors, err := parseSelectors(req.GetSelectors(), req.GetSpaceId(), req.GetDatasetId(), maxSelectors)
	if err != nil {
		return nil, err
	}
	latest := req.GetLatestPerSeries()
	if (latest > 0) == (req.GetTimeRange() != nil) {
		return nil, invalid("provide exactly one of time_range or latest_per_series")
	}
	if latest > maxLatestPerSeries {
		return nil, invalidf("latest_per_series must be at most %d", maxLatestPerSeries)
	}
	cursor, err := decodeRowsCursor(req.GetPage().GetCursor())
	if err != nil {
		return nil, err
	}
	snapshot := s.db.NewSnapshot()
	defer snapshot.Close()
	var (
		keys       []*pb.RowKey
		nextCursor *pb.RowsCursor
		pageSize   uint32
	)
	if latest > 0 {
		pageSize = clampPageSize(req.GetPage().GetSize(), defaultLatestPageSize, maxLatestPageSize)
		if values := uint32(maxLatestPageValues / len(req.GetColumnNames())); values < pageSize {
			pageSize = max(values, 1)
		}
		keys, nextCursor, err = s.readLatestPerSeries(ctx, snapshot, req.GetSpaceId(), req.GetDatasetId(), selectors, int(latest), int(pageSize), cursor)
	} else {
		pageSize = clampPageSize(req.GetPage().GetSize(), defaultRangePageSize, maxRangePageSize)
		start, end, rangeErr := parseTimeRange(req.GetTimeRange())
		if rangeErr != nil {
			return nil, rangeErr
		}
		if len(selectors) == 0 {
			keys, nextCursor, err = s.readRangeByTime(ctx, snapshot, req.GetSpaceId(), req.GetDatasetId(), start, end, int(pageSize), cursor)
		} else {
			keys, nextCursor, err = s.readRangeBySeries(ctx, snapshot, req.GetSpaceId(), req.GetDatasetId(), selectors, start, end, int(pageSize), cursor)
		}
	}
	if err != nil {
		return nil, err
	}
	values := make([]*pb.RowFieldValues, 0, len(keys))
	for from := 0; from < len(keys); from += maxReadRowsKeys {
		to := min(from+maxReadRowsKeys, len(keys))
		part, readErr := s.readRowsAt(ctx, snapshot, keys[from:to], req.GetColumnNames(), nil)
		if readErr != nil {
			return nil, readErr
		}
		values = append(values, part...)
	}
	rows := make([]*pb.TimeSeriesRow, 0, len(values))
	for _, row := range values {
		key := row.GetKey().GetTimeSeries()
		rows = append(rows, &pb.TimeSeriesRow{
			Key:    &pb.TimeSeriesKey{SpaceId: row.GetKey().GetSpaceId(), DatasetId: row.GetKey().GetDatasetId(), SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: outputTime(key.GetDataTime()), SeriesTag: key.GetSeriesTag()},
			Fields: row.GetFields(),
		})
	}
	result := &pb.PageResult{Size: pageSize}
	if nextCursor != nil {
		encoded, encodeErr := encodeRowsCursor(nextCursor)
		if encodeErr != nil {
			return nil, encodeErr
		}
		result.HasMore, result.NextCursor = true, encoded
	}
	return &pb.ReadTimeSeriesRowsRsp{Rows: rows, PageResult: result}, nil
}

func clampPageSize(size, def, limit uint32) uint32 {
	if size == 0 {
		return def
	}
	return min(size, limit)
}

// outputTime 把索引里的固定宽度时间转成 RFC3339Nano（没有小数秒时不带小数）。
func outputTime(canonical string) string {
	at, err := time.Parse(time.RFC3339Nano, canonical)
	if err != nil {
		return canonical
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func canonicalTime(at time.Time) string { return at.UTC().Format(canonicalTimeLayout) }

// ---- 区间读取：不带 selector，走时间索引 ----

func (s *Store) readRangeByTime(ctx context.Context, snap pebbleReader, spaceID, datasetID string, start, end time.Time, size int, cursor *pb.RowsCursor) ([]*pb.RowKey, *pb.RowsCursor, error) {
	prefix := historyDatasetPrefix(spaceID, datasetID)
	lower, upper := append([]byte(nil), prefix...), nextPrefix(prefix)
	if !start.IsZero() {
		lower = historyTimeBound(prefix, start)
	}
	if !end.IsZero() {
		upper = historyTimeBound(prefix, end)
	}
	if cursor.GetLast() != nil {
		after := &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: cursor.GetLast()}}
		afterKey, err := encodeHistoryKey(after, s.bucketDuration)
		if err != nil {
			return nil, nil, invalidf("invalid page cursor: %w", err)
		}
		if next := nextPrefix(afterKey); bytes.Compare(next, lower) > 0 {
			lower = next
		}
	}
	if bytes.Compare(lower, upper) >= 0 {
		return nil, nil, nil
	}
	iter, err := snap.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()
	keys := make([]*pb.RowKey, 0, size)
	hasMore := false
	for valid, n := iter.First(), 0; valid; valid, n = iter.Next(), n+1 {
		if n&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		key, ok := parseHistoryKey(iter.Key())
		if !ok {
			continue
		}
		if len(keys) == size {
			hasMore = true
			break
		}
		keys = append(keys, key)
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	if !hasMore || len(keys) == 0 {
		return keys, nil, nil
	}
	return keys, &pb.RowsCursor{Last: keys[len(keys)-1].GetTimeSeries()}, nil
}

// ---- 区间读取：带 selector，走序列索引，各序列按时间归并 ----

type seriesCursor struct {
	id     seriesID
	prefix []byte
}

type rangeCandidate struct {
	series int
	raw    []byte
	key    *pb.RowKey
}

type rangeHeap []rangeCandidate

func (h rangeHeap) Len() int { return len(h) }
func (h rangeHeap) Less(i, j int) bool {
	return historyKeyLess(h[i].key.GetTimeSeries(), h[j].key.GetTimeSeries())
}
func (h rangeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *rangeHeap) Push(v any)   { *h = append(*h, v.(rangeCandidate)) }
func (h *rangeHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func seriesDatasetPrefix(spaceID, datasetID string) []byte {
	prefix := []byte{seriesHistoryNamespace, timeSeriesKind}
	prefix = appendRawPart(prefix, []byte(spaceID))
	return appendRawPart(prefix, []byte(datasetID))
}

func seriesKeyPrefix(spaceID, datasetID string, id seriesID) []byte {
	return seriesHistoryPrefix(spaceID, datasetID, seriesSelector{subject: id.subject, freq: id.freq, tag: id.tag, hasTag: true})
}

// enumerateSeries 列出一个 selector 选中的全部序列（selector 不带 series_tag 时逐个定位该标的各序列的起点）。
func enumerateSeries(iter *cpebble.Iterator, spaceID, datasetID string, selector seriesSelector) ([]seriesCursor, error) {
	prefix := seriesHistoryPrefix(spaceID, datasetID, selector)
	if selector.hasTag {
		return []seriesCursor{{id: seriesID{selector.subject, selector.freq, selector.tag}, prefix: prefix}}, nil
	}
	var out []seriesCursor
	for valid := iter.SeekGE(prefix); valid && bytes.HasPrefix(iter.Key(), prefix); {
		key, ok := parseSeriesHistoryKey(iter.Key())
		if !ok {
			valid = iter.Next()
			continue
		}
		row := key.GetTimeSeries()
		id := seriesID{row.GetSubjectId(), row.GetFreq(), row.GetSeriesTag()}
		tagPrefix := seriesKeyPrefix(spaceID, datasetID, id)
		out = append(out, seriesCursor{id: id, prefix: tagPrefix})
		valid = iter.SeekGE(nextPrefix(tagPrefix))
	}
	return out, iter.Error()
}

func (s *Store) readRangeBySeries(ctx context.Context, snap pebbleReader, spaceID, datasetID string, selectors []seriesSelector, start, end time.Time, size int, cursor *pb.RowsCursor) ([]*pb.RowKey, *pb.RowsCursor, error) {
	base := seriesDatasetPrefix(spaceID, datasetID)
	iter, err := snap.NewIter(&cpebble.IterOptions{LowerBound: base, UpperBound: nextPrefix(base)})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()
	seen := map[seriesID]bool{}
	var series []seriesCursor
	for _, selector := range selectors {
		found, err := enumerateSeries(iter, spaceID, datasetID, selector)
		if err != nil {
			return nil, nil, err
		}
		for _, item := range found {
			if !seen[item.id] {
				seen[item.id] = true
				series = append(series, item)
			}
		}
	}
	startTime := ""
	if !start.IsZero() {
		startTime = canonicalTime(start)
	}
	endTime := ""
	if !end.IsZero() {
		endTime = canonicalTime(end)
	}
	last := cursor.GetLast()
	if last != nil && last.GetDataTime() > startTime {
		startTime = last.GetDataTime()
	}
	// fetch 返回序列在 seek 位置之后（含）的第一个键；超出区间终点或序列范围则 ok 为 false。
	var steps int
	fetch := func(index int, seek []byte) (rangeCandidate, bool, error) {
		for valid := iter.SeekGE(seek); valid && bytes.HasPrefix(iter.Key(), series[index].prefix); valid = iter.Next() {
			if steps++; steps&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return rangeCandidate{}, false, err
				}
			}
			key, ok := parseSeriesHistoryKey(iter.Key())
			if !ok {
				continue
			}
			row := key.GetTimeSeries()
			if endTime != "" && row.GetDataTime() >= endTime {
				return rangeCandidate{}, false, nil
			}
			if last != nil && historyKeyCompare(row, last) <= 0 {
				continue
			}
			return rangeCandidate{series: index, raw: append([]byte(nil), iter.Key()...), key: key}, true, nil
		}
		return rangeCandidate{}, false, iter.Error()
	}
	var candidates rangeHeap
	for index := range series {
		seek := series[index].prefix
		if startTime != "" {
			seek = appendRawPart(append([]byte(nil), seek...), []byte(startTime))
		}
		candidate, ok, err := fetch(index, seek)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	heap.Init(&candidates)
	keys := make([]*pb.RowKey, 0, size)
	for len(keys) < size && candidates.Len() > 0 {
		top := heap.Pop(&candidates).(rangeCandidate)
		keys = append(keys, top.key)
		next, ok, err := fetch(top.series, nextPrefix(top.raw))
		if err != nil {
			return nil, nil, err
		}
		if ok {
			heap.Push(&candidates, next)
		}
	}
	if candidates.Len() == 0 || len(keys) == 0 {
		return keys, nil, nil
	}
	return keys, &pb.RowsCursor{Last: keys[len(keys)-1].GetTimeSeries()}, nil
}

// ---- 每个序列最近 N 根 ----

// latestReader 按序列顺序（subject_id、freq、series_tag 升序）逐个序列、从新到旧取最近 N 根。
// 序列范围来自 selector；不带 selector 时是整个数据集。
type latestReader struct {
	iter      *cpebble.Iterator
	spaceID   string
	datasetID string
	prefixes  [][]byte
	per       int

	ri         int // 当前序列范围
	seekFrom   []byte
	cur        *seriesCursor
	returned   int
	before     string // 当前序列已返回的最早一根的 data_time；空表示从序列末尾开始
	positioned bool
}

func newLatestReader(iter *cpebble.Iterator, spaceID, datasetID string, selectors []seriesSelector, per int) *latestReader {
	r := &latestReader{iter: iter, spaceID: spaceID, datasetID: datasetID, per: per}
	if len(selectors) == 0 {
		r.prefixes = [][]byte{seriesDatasetPrefix(spaceID, datasetID)}
		return r
	}
	all := make([][]byte, 0, len(selectors))
	for _, selector := range selectors {
		all = append(all, seriesHistoryPrefix(spaceID, datasetID, selector))
	}
	sort.Slice(all, func(i, j int) bool { return bytes.Compare(all[i], all[j]) < 0 })
	for _, prefix := range all {
		if n := len(r.prefixes); n > 0 && bytes.HasPrefix(prefix, r.prefixes[n-1]) {
			continue // 被前一个范围覆盖（含重复）
		}
		r.prefixes = append(r.prefixes, prefix)
	}
	return r
}

// resume 把读取位置恢复到游标记录的位置。
func (r *latestReader) resume(cursor *pb.RowsCursor) error {
	if cursor == nil {
		return nil
	}
	id := seriesID{cursor.GetSubjectId(), cursor.GetFreq(), cursor.GetSeriesTag()}
	if id.subject == "" || id.freq == "" {
		return invalid("invalid page cursor")
	}
	prefix := seriesKeyPrefix(r.spaceID, r.datasetID, id)
	for index, candidate := range r.prefixes {
		if bytes.HasPrefix(prefix, candidate) {
			r.ri = index
			r.cur = &seriesCursor{id: id, prefix: prefix}
			r.returned = int(cursor.GetReturned())
			r.before = cursor.GetResumeBefore()
			r.seekFrom = nextPrefix(prefix)
			return nil
		}
	}
	return invalid("page cursor does not match the request")
}

func (r *latestReader) state() *pb.RowsCursor {
	if r.cur == nil {
		return nil
	}
	return &pb.RowsCursor{SubjectId: r.cur.id.subject, Freq: r.cur.id.freq, SeriesTag: r.cur.id.tag, ResumeBefore: r.before, Returned: uint32(r.returned)}
}

// next 返回下一个键；读完时返回 nil。
func (r *latestReader) next() (*pb.RowKey, error) {
	for {
		if r.cur == nil {
			if r.ri >= len(r.prefixes) {
				return nil, nil
			}
			seek := r.seekFrom
			if seek == nil {
				seek = r.prefixes[r.ri]
			}
			if !r.iter.SeekGE(seek) || !bytes.HasPrefix(r.iter.Key(), r.prefixes[r.ri]) {
				if err := r.iter.Error(); err != nil {
					return nil, err
				}
				r.ri++
				r.seekFrom = nil
				continue
			}
			key, ok := parseSeriesHistoryKey(r.iter.Key())
			if !ok {
				return nil, invalid("corrupt series index key")
			}
			row := key.GetTimeSeries()
			id := seriesID{row.GetSubjectId(), row.GetFreq(), row.GetSeriesTag()}
			prefix := seriesKeyPrefix(r.spaceID, r.datasetID, id)
			r.cur, r.returned, r.before, r.positioned = &seriesCursor{id: id, prefix: prefix}, 0, "", false
			r.seekFrom = nextPrefix(prefix)
		}
		if r.returned >= r.per {
			r.cur, r.positioned = nil, false
			continue
		}
		var valid bool
		switch {
		case r.positioned:
			valid = r.iter.Prev()
		case r.before == "":
			valid = r.iter.SeekLT(nextPrefix(r.cur.prefix))
		default:
			valid = r.iter.SeekLT(appendRawPart(append([]byte(nil), r.cur.prefix...), []byte(r.before)))
		}
		if !valid || !bytes.HasPrefix(r.iter.Key(), r.cur.prefix) {
			if err := r.iter.Error(); err != nil {
				return nil, err
			}
			r.cur, r.positioned = nil, false
			continue
		}
		key, ok := parseSeriesHistoryKey(r.iter.Key())
		if !ok {
			return nil, invalid("corrupt series index key")
		}
		r.positioned = true
		r.returned++
		r.before = key.GetTimeSeries().GetDataTime()
		return key, nil
	}
}

func (s *Store) readLatestPerSeries(ctx context.Context, snap pebbleReader, spaceID, datasetID string, selectors []seriesSelector, per, size int, cursor *pb.RowsCursor) ([]*pb.RowKey, *pb.RowsCursor, error) {
	base := seriesDatasetPrefix(spaceID, datasetID)
	iter, err := snap.NewIter(&cpebble.IterOptions{LowerBound: base, UpperBound: nextPrefix(base)})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()
	reader := newLatestReader(iter, spaceID, datasetID, selectors, per)
	if err := reader.resume(cursor); err != nil {
		return nil, nil, err
	}
	keys := make([]*pb.RowKey, 0, min(size, 1024))
	for len(keys) < size {
		if len(keys)&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		key, err := reader.next()
		if err != nil {
			return nil, nil, err
		}
		if key == nil {
			return keys, nil, nil
		}
		keys = append(keys, key)
	}
	next := reader.state()
	// 预读一个键判断是否还有下一页；游标取预读之前的位置。
	peek, err := reader.next()
	if err != nil {
		return nil, nil, err
	}
	if peek == nil {
		return keys, nil, nil
	}
	return keys, next, nil
}

// ---- 计数 ----

// CountTimeSeriesRows 按标的统计区间内已存在的时间点数：同一时间点有多个 series_tag 只计一次。
// 只在序列索引上扫描键，不读字段值。
func (s *Store) CountTimeSeriesRows(ctx context.Context, req *pb.CountTimeSeriesRowsReq) ([]*pb.SubjectRowCount, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" || len(req.GetSelectors()) == 0 {
		return nil, invalid("space_id, dataset_id and selectors are required")
	}
	selectors, err := parseSelectors(req.GetSelectors(), req.GetSpaceId(), req.GetDatasetId(), maxCountSelectors)
	if err != nil {
		return nil, err
	}
	start, end, err := parseTimeRange(req.GetTimeRange())
	if err != nil {
		return nil, err
	}
	snapshot := s.db.NewSnapshot()
	defer snapshot.Close()
	base := seriesDatasetPrefix(req.GetSpaceId(), req.GetDatasetId())
	iter, err := snapshot.NewIter(&cpebble.IterOptions{LowerBound: base, UpperBound: nextPrefix(base)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	bySubject := map[string][]seriesCursor{}
	seen := map[seriesID]bool{}
	for _, selector := range selectors {
		found, err := enumerateSeries(iter, req.GetSpaceId(), req.GetDatasetId(), selector)
		if err != nil {
			return nil, err
		}
		for _, item := range found {
			if !seen[item.id] {
				seen[item.id] = true
				bySubject[item.id.subject] = append(bySubject[item.id.subject], item)
			}
		}
	}
	subjects := make([]string, 0, len(bySubject))
	for subject := range bySubject {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	out := make([]*pb.SubjectRowCount, 0, len(subjects))
	for _, subject := range subjects {
		list := bySubject[subject]
		var count uint32
		var times map[string]struct{}
		if len(list) > 1 {
			times = map[string]struct{}{}
		}
		for _, item := range list {
			lower, upper := append([]byte(nil), item.prefix...), nextPrefix(item.prefix)
			if !start.IsZero() {
				lower = appendRawPart(append([]byte(nil), item.prefix...), []byte(canonicalTime(start)))
			}
			if !end.IsZero() {
				upper = appendRawPart(append([]byte(nil), item.prefix...), []byte(canonicalTime(end)))
			}
			if bytes.Compare(lower, upper) >= 0 {
				continue
			}
			rangeIter, err := snapshot.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
			if err != nil {
				return nil, err
			}
			for valid, n := rangeIter.First(), 0; valid; valid, n = rangeIter.Next(), n+1 {
				if n&1023 == 0 {
					if err := ctx.Err(); err != nil {
						_ = rangeIter.Close()
						return nil, err
					}
				}
				if times == nil {
					count++
					continue
				}
				if key, ok := parseSeriesHistoryKey(rangeIter.Key()); ok {
					times[key.GetTimeSeries().GetDataTime()] = struct{}{}
				}
			}
			err = rangeIter.Error()
			_ = rangeIter.Close()
			if err != nil {
				return nil, err
			}
		}
		if times != nil {
			count = uint32(len(times))
		}
		if count > 0 {
			out = append(out, &pb.SubjectRowCount{SubjectId: subject, Rows: count})
		}
	}
	return out, nil
}

// ---- 精确读取 ----

// ReadRows 按精确键读取，只返回存在的行（有任意字段或属性）：存在但请求的列都没有值时返回只有键的行，不存在的键不返回。
// 返回顺序与请求一致，记录型键没有版本时解析为最大版本。整个请求使用同一个快照，成组字段由一次提交原子写入，不会读到混合值。
func (s *Store) ReadRows(ctx context.Context, keys []*pb.RowKey, columns, attributeKeys []string) ([]*pb.RowFieldValues, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	if len(keys) == 0 {
		return nil, invalid("keys are required")
	}
	if len(columns) == 0 && len(attributeKeys) == 0 {
		return nil, invalid("column_names or attribute_keys are required")
	}
	if len(keys) > maxReadRowsKeys {
		return nil, invalidf("read request exceeds the limit of %d keys", maxReadRowsKeys)
	}
	snapshot := s.db.NewSnapshot()
	defer snapshot.Close()
	return s.readRowsAt(ctx, snapshot, keys, columns, attributeKeys)
}

type rowRead struct {
	index  int
	key    *pb.RowKey
	fields []byte // 字段命名空间的行前缀
	attrs  []byte // 属性命名空间的行前缀
}

func (s *Store) readRowsAt(ctx context.Context, snap pebbleReader, keys []*pb.RowKey, columns, attributeKeys []string) ([]*pb.RowFieldValues, error) {
	reads := make([]rowRead, len(keys))
	for index, key := range keys {
		resolved := key
		if rec := key.GetRecord(); rec != nil && rec.GetVersion() == "" {
			var err error
			if resolved, err = s.resolveMaxRecordVersion(snap, key); err != nil {
				return nil, err
			}
		}
		fieldPrefix, err := encodeNamespacePrefix(fieldNamespace, resolved, s.bucketDuration)
		if err != nil {
			return nil, err
		}
		attrPrefix, err := encodeNamespacePrefix(attributeNamespace, resolved, s.bucketDuration)
		if err != nil {
			return nil, err
		}
		reads[index] = rowRead{index: index, key: resolved, fields: fieldPrefix, attrs: attrPrefix}
	}
	// 按物理编码排序后顺序读取，同一行的字段在物理上连续。
	order := make([]*rowRead, len(reads))
	for i := range reads {
		order[i] = &reads[i]
	}
	sort.SliceStable(order, func(i, j int) bool { return bytes.Compare(order[i].fields, order[j].fields) < 0 })
	wantField := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		wantField[column] = struct{}{}
	}
	wantAttr := make(map[string]struct{}, len(attributeKeys))
	for _, name := range attributeKeys {
		wantAttr[name] = struct{}{}
	}
	fieldIter, err := snap.NewIter(nil)
	if err != nil {
		return nil, err
	}
	defer fieldIter.Close()
	var attrIter *cpebble.Iterator
	defer func() {
		if attrIter != nil {
			_ = attrIter.Close()
		}
	}()
	result := make([]*pb.RowFieldValues, len(keys))
	for n, item := range order {
		if n&255 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		row := &pb.RowFieldValues{Key: item.key}
		present := false
		for valid := fieldIter.SeekGE(item.fields); valid && bytes.HasPrefix(fieldIter.Key(), item.fields); valid = fieldIter.Next() {
			present = true
			id, _, err := decodePart(fieldIter.Key()[len(item.fields):])
			if err != nil {
				return nil, err
			}
			if _, ok := wantField[id]; !ok {
				continue
			}
			value := &pb.TypedValue{}
			if err := proto.Unmarshal(fieldIter.Value(), value); err != nil {
				return nil, err
			}
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: id, Value: value})
		}
		if err := fieldIter.Error(); err != nil {
			return nil, err
		}
		if len(wantAttr) > 0 || !present {
			if attrIter == nil {
				if attrIter, err = snap.NewIter(nil); err != nil {
					return nil, err
				}
			}
			for valid := attrIter.SeekGE(item.attrs); valid && bytes.HasPrefix(attrIter.Key(), item.attrs); valid = attrIter.Next() {
				present = true
				if len(wantAttr) == 0 {
					break
				}
				name, _, err := decodePart(attrIter.Key()[len(item.attrs):])
				if err != nil {
					return nil, err
				}
				if _, ok := wantAttr[name]; !ok {
					continue
				}
				value := &pb.TypedValue{}
				if err := proto.Unmarshal(attrIter.Value(), value); err != nil {
					return nil, err
				}
				if row.Attributes == nil {
					row.Attributes = make(map[string]*pb.TypedValue)
				}
				row.Attributes[name] = value
			}
			if err := attrIter.Error(); err != nil {
				return nil, err
			}
		}
		if present {
			result[item.index] = row
		}
	}
	out := result[:0]
	for _, row := range result {
		if row != nil {
			out = append(out, row)
		}
	}
	return out, nil
}

// ---- 键辅助 ----

func historyDatasetPrefix(spaceID, datasetID string) []byte {
	prefix := []byte{historyNamespace, timeSeriesKind}
	prefix = appendRawPart(prefix, []byte(spaceID))
	return appendRawPart(prefix, []byte(datasetID))
}

func seriesHistoryPrefix(spaceID, datasetID string, selector seriesSelector) []byte {
	prefix := seriesDatasetPrefix(spaceID, datasetID)
	prefix = appendRawPart(prefix, []byte(selector.subject))
	prefix = appendRawPart(prefix, []byte(selector.freq))
	if selector.hasTag {
		prefix = appendRawPart(prefix, []byte(selector.tag))
	}
	return prefix
}

func historyTimeBound(prefix []byte, at time.Time) []byte {
	bound := append([]byte(nil), prefix...)
	return appendRawPart(bound, []byte(canonicalTime(at)))
}

func historyKeyLess(a, b *pb.TimeSeriesRowKey) bool {
	if a.GetDataTime() != b.GetDataTime() {
		return a.GetDataTime() < b.GetDataTime()
	}
	if a.GetSubjectId() != b.GetSubjectId() {
		return a.GetSubjectId() < b.GetSubjectId()
	}
	if a.GetFreq() != b.GetFreq() {
		return a.GetFreq() < b.GetFreq()
	}
	return a.GetSeriesTag() < b.GetSeriesTag()
}

func historyKeyCompare(a, b *pb.TimeSeriesRowKey) int {
	if historyKeyLess(a, b) {
		return -1
	}
	if historyKeyLess(b, a) {
		return 1
	}
	return 0
}

func parseHistoryKey(key []byte) (*pb.RowKey, bool) {
	if len(key) < 2 || key[0] != historyNamespace || key[1] != timeSeriesKind {
		return nil, false
	}
	parts, ok := decodeParts(key[2:], 6)
	if !ok {
		return nil, false
	}
	return &pb.RowKey{SpaceId: parts[0], DatasetId: parts[1], Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{DataTime: parts[2], SubjectId: parts[3], Freq: parts[4], SeriesTag: parts[5]}}}, true
}

func parseSeriesHistoryKey(key []byte) (*pb.RowKey, bool) {
	if len(key) < 2 || key[0] != seriesHistoryNamespace || key[1] != timeSeriesKind {
		return nil, false
	}
	parts, ok := decodeParts(key[2:], 6)
	if !ok {
		return nil, false
	}
	return &pb.RowKey{SpaceId: parts[0], DatasetId: parts[1], Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: parts[2], Freq: parts[3], SeriesTag: parts[4], DataTime: parts[5]}}}, true
}

func decodeParts(rest []byte, count int) ([]string, bool) {
	parts := make([]string, 0, count)
	for i := 0; i < count; i++ {
		part, next, err := decodePart(rest)
		if err != nil {
			return nil, false
		}
		parts = append(parts, part)
		rest = next
	}
	return parts, true
}
