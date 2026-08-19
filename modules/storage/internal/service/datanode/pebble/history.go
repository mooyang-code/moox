package pebble

import (
	"bytes"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

var errHistoryIndexNotReady = errors.New("Primary history index is still being materialized")

// ReadTimeSeriesRows scans the physical Primary keyspace. It is deliberately
// implemented below the public PrimaryStore view resolver so a newly created
// View can recover history after its old indexes have been removed.
func (s *Store) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return nil, errors.New("space_id and dataset_id are required")
	}
	selectors := make([]historySelector, 0, len(req.GetSelectors()))
	for _, selector := range req.GetSelectors() {
		if selector == nil || selector.GetSubjectId() == "" || selector.GetFreq() == "" {
			return nil, errors.New("selector subject_id and freq are required")
		}
		if selector.GetSpaceId() != "" && selector.GetSpaceId() != req.GetSpaceId() {
			return nil, errors.New("selector space_id does not match request")
		}
		if selector.GetDatasetId() != "" && selector.GetDatasetId() != req.GetDatasetId() {
			return nil, errors.New("selector dataset_id does not match request")
		}
		selectors = append(selectors, historySelector{subject: selector.GetSubjectId(), freq: selector.GetFreq(), tag: selector.GetSeriesTag(), hasTag: selector.SeriesTag != nil})
	}
	selectorSet := newHistorySelectorSet(selectors)
	var start, end time.Time
	var err error
	if req.GetTimeRange() != nil {
		if raw := strings.TrimSpace(req.GetTimeRange().GetStartTime()); raw != "" {
			start, err = time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return nil, fmt.Errorf("invalid time_range.start_time: %w", err)
			}
			start = start.UTC()
		}
		if raw := strings.TrimSpace(req.GetTimeRange().GetEndTime()); raw != "" {
			end, err = time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return nil, fmt.Errorf("invalid time_range.end_time: %w", err)
			}
			end = end.UTC()
		}
		if !start.IsZero() && !end.IsZero() && !start.Before(end) {
			return nil, errors.New("time_range.start_time must be before end_time")
		}
	}

	var after *pb.RowKey
	if raw := req.GetAfterKey(); len(raw) > 0 {
		after = &pb.RowKey{}
		if err := proto.Unmarshal(raw, after); err != nil {
			return nil, fmt.Errorf("invalid after_key: %w", err)
		}
		if after.GetTimeSeries() == nil || after.GetSpaceId() != req.GetSpaceId() || after.GetDatasetId() != req.GetDatasetId() {
			return nil, errors.New("after_key must be a time-series key in the requested dataset")
		}
		after, err = NormalizeRowKey(after)
		if err != nil {
			return nil, fmt.Errorf("invalid after_key: %w", err)
		}
	}
	pageNo, pageSize := uint32(1), uint32(1000)
	if req.GetPage() != nil {
		if req.GetPage().GetPage() > 0 {
			pageNo = req.GetPage().GetPage()
		}
		if req.GetPage().GetSize() > 0 {
			pageSize = req.GetPage().GetSize()
		}
	}
	if pageSize > 10000 {
		pageSize = 10000
	}
	// Stores created before the dedicated history namespace still contain the
	// complete row identity in their field keys. Materialize that identity once
	// per dataset before serving the first history page so a View reset can
	// recover the retained Primary history instead of only rows written after
	// the namespace was introduced.
	if err := s.ensureHistoryIndex(ctx, req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return nil, err
	}
	pageOffset := uint64(0)
	if pageNo > 1 {
		pageOffset = uint64(pageNo-1) * uint64(pageSize)
		// Page-number pagination is retained only as a small compatibility
		// window. Large offsets require retaining every preceding RowKey in
		// memory; all internal rebuild callers use after_key and stay bounded.
		if pageOffset > 10_000 {
			return nil, errors.New("page offset exceeds history scan limit; use after_key pagination")
		}
	}
	desc := req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC
	// Internal backfills use after_key and keep memory bounded by one page.
	// Legacy page-number callers still work, but only retain the requested
	// window rather than materializing the complete dataset.
	skip := 0
	if after == nil && pageNo > 1 {
		skip = int(pageOffset)
	}
	limit := skip + int(pageSize) + 1
	keysByID := make(map[string]*pb.RowKey, limit)
	var candidates historyCandidateHeap
	candidates.desc = desc
	heap.Init(&candidates)
	better := func(a, b *pb.RowKey) bool {
		if desc {
			return historyKeyLess(b.GetTimeSeries(), a.GetTimeSeries())
		}
		return historyKeyLess(a.GetTimeSeries(), b.GetTimeSeries())
	}
	insertCandidate := func(key *pb.RowKey) {
		if key == nil {
			return
		}
		encoded, err := proto.Marshal(key)
		if err != nil {
			return
		}
		id := string(encoded)
		if _, exists := keysByID[id]; exists {
			return
		}
		if after != nil {
			cmp := historyKeyCompare(key.GetTimeSeries(), after.GetTimeSeries())
			if desc {
				if cmp >= 0 {
					return
				}
			} else if cmp <= 0 {
				return
			}
		}
		keysByID[id] = key
		if candidates.Len() < limit {
			heap.Push(&candidates, key)
			return
		}
		if better(key, candidates.items[0]) {
			removed := heap.Pop(&candidates).(*pb.RowKey)
			if data, err := proto.Marshal(removed); err == nil {
				delete(keysByID, string(data))
			}
			heap.Push(&candidates, key)
		}
	}
	// The dedicated history index is ordered by the same logical tuple used by
	// the API (data_time, subject, frequency, tag). This makes cursor paging a
	// single seekable scan rather than a repeated scan of every field/attribute
	// key in the current time bucket.
	prefix := historyDatasetPrefix(req.GetSpaceId(), req.GetDatasetId())
	lower := append([]byte(nil), prefix...)
	upper := nextPrefix(prefix)
	if !start.IsZero() {
		lower = historyTimeBound(prefix, start)
	}
	if !end.IsZero() {
		upper = historyTimeBound(prefix, end)
	}
	if after != nil {
		afterKey, keyErr := encodeHistoryKey(after, s.bucketDuration)
		if keyErr != nil {
			return nil, fmt.Errorf("invalid after_key: %w", keyErr)
		}
		if desc {
			if upper == nil || bytes.Compare(afterKey, upper) < 0 {
				upper = afterKey
			}
		} else if bytes.Compare(afterKey, lower) >= 0 {
			lower = nextPrefix(afterKey)
		}
	}
	if upper != nil && lower != nil && bytes.Compare(lower, upper) >= 0 {
		return &pb.ReadTimeSeriesRowsRsp{Rows: nil, PageResult: &pb.PageResult{Page: pageNo, Size: pageSize, HasMore: false}}, nil
	}
	iter, iterErr := s.db.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if iterErr != nil {
		return nil, iterErr
	}
	valid := iter.First()
	advance := iter.Next
	if desc {
		valid = iter.Last()
		advance = iter.Prev
	}
	for ; valid; valid = advance() {
		if err := ctx.Err(); err != nil {
			_ = iter.Close()
			return nil, err
		}
		key, ok := parseHistoryKey(iter.Key())
		if !ok || !historyMatches(key, selectorSet, start, end) {
			continue
		}
		insertCandidate(key)
		if candidates.Len() >= limit {
			break
		}
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, err
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	ordered := append([]*pb.RowKey(nil), candidates.items...)
	sort.Slice(ordered, func(i, j int) bool {
		if desc {
			return historyKeyLess(ordered[j].GetTimeSeries(), ordered[i].GetTimeSeries())
		}
		return historyKeyLess(ordered[i].GetTimeSeries(), ordered[j].GetTimeSeries())
	})
	startAt := 0
	if after == nil {
		startAt = skip
	}
	if startAt > len(ordered) {
		startAt = len(ordered)
	}
	endAt := startAt + int(pageSize)
	if endAt > len(ordered) {
		endAt = len(ordered)
	}
	selected := ordered[startAt:endAt]
	hasMore := len(ordered) > endAt
	var rows []*pb.RowFieldValues
	existingSet := make(map[string]struct{}, len(selected))
	if len(req.GetColumnNames()) == 0 {
		rows = make([]*pb.RowFieldValues, 0, len(selected))
		for _, key := range selected {
			rows = append(rows, &pb.RowFieldValues{Key: key})
			data, marshalErr := proto.Marshal(key)
			if marshalErr == nil {
				existingSet[string(data)] = struct{}{}
			}
		}
	} else {
		// ReadFieldsWithPresence caps key*field pairs at 100,000. A public
		// history request may ask for a full page and a wide View schema, so
		// split the enrichment read without changing the logical page.
		fieldIDs := req.GetColumnNames()
		chunkSize := len(selected)
		if len(fieldIDs) > 0 && chunkSize > 100000/len(fieldIDs) {
			chunkSize = 100000 / len(fieldIDs)
			if chunkSize == 0 {
				chunkSize = 1
			}
		}
		rows = make([]*pb.RowFieldValues, 0, len(selected))
		for start := 0; start < len(selected); start += chunkSize {
			end := start + chunkSize
			if end > len(selected) {
				end = len(selected)
			}
			partRows, existing, readErr := s.ReadFieldsWithPresence(ctx, selected[start:end], fieldIDs, nil)
			if readErr != nil {
				return nil, readErr
			}
			rows = append(rows, partRows...)
			for _, key := range existing {
				data, marshalErr := proto.Marshal(key)
				if marshalErr == nil {
					existingSet[string(data)] = struct{}{}
				}
			}
		}
	}
	result := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		data, marshalErr := proto.Marshal(row.GetKey())
		if marshalErr != nil {
			return nil, marshalErr
		}
		if _, ok := existingSet[string(data)]; !ok {
			continue
		}
		key := row.GetKey().GetTimeSeries()
		result = append(result, &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SpaceId: row.GetKey().GetSpaceId(), DatasetId: row.GetKey().GetDatasetId(), SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag()}, Fields: row.GetFields()})
	}
	return &pb.ReadTimeSeriesRowsRsp{Rows: result, PageResult: &pb.PageResult{Page: pageNo, Size: pageSize, HasMore: hasMore}}, nil
}

func (s *Store) ensureHistoryIndex(ctx context.Context, spaceID, datasetID string) error {
	cacheKey := spaceID + "\x00" + datasetID
	s.historyMu.Lock()
	if s.historyBackfilled == nil {
		s.historyBackfilled = make(map[string]bool)
	}
	if s.historyBackfillStarted == nil {
		s.historyBackfillStarted = make(map[string]bool)
	}
	if s.historyBackfilled[cacheKey] {
		s.historyMu.Unlock()
		return nil
	}
	if s.historyBackfillStarted[cacheKey] {
		s.historyMu.Unlock()
		return errHistoryIndexNotReady
	}
	s.historyBackfillStarted[cacheKey] = true
	s.historyMu.Unlock()
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		err := s.backfillHistoryIndexSync(ctx, spaceID, datasetID)
		s.historyMu.Lock()
		if err == nil {
			s.historyBackfilled[cacheKey] = true
		}
		s.historyMu.Unlock()
		return err
	}
	// Legacy stores may contain millions of field keys. Do not spend the
	// request's short RPC deadline rebuilding the derived history namespace;
	// start one bounded-by-dataset background pass and let the next reconcile
	// observe the completed markers.
	log.Printf("DataNode history index backfill started space=%s dataset=%s", spaceID, datasetID)
	s.maintenanceMu.Lock()
	if s.closing {
		s.maintenanceMu.Unlock()
		return errors.New("DataNode store is closing")
	}
	s.historyWG.Add(1)
	s.maintenanceMu.Unlock()
	go func() {
		defer s.historyWG.Done()
		s.backfillHistoryIndex(cacheKey, spaceID, datasetID)
	}()
	// Never serve a partial derived index. The caller will retry after the
	// background pass marks the dataset complete; returning success here would
	// let a View rebuild activate an incomplete B index.
	return errHistoryIndexNotReady
}

func (s *Store) backfillHistoryIndex(cacheKey, spaceID, datasetID string) {
	// System metrics is a low-priority derived index. Defer its first full
	// materialization so business datasets (notably K-line) can finish their
	// initial View rebuild without competing with the largest metrics scan.
	if spaceID == "moox_system" && datasetID == "moox_service_metrics" {
		timer := time.NewTimer(10 * time.Minute)
		defer timer.Stop()
		ctx := s.historyContext()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
	}
	// A legacy dataset may contain millions of field keys. Running one full
	// history materialization per dataset in parallel monopolizes Pebble's
	// compaction/read budget and starves live PrimaryStore requests. Serialize
	// these derived-index passes; the caller remains asynchronous and the next
	// reconcile observes each dataset as it completes.
	s.historyBackfillMu.Lock()
	defer s.historyBackfillMu.Unlock()
	err := s.backfillHistoryIndexSync(s.historyContext(), spaceID, datasetID)
	s.historyMu.Lock()
	if err == nil {
		s.historyBackfilled[cacheKey] = true
	}
	// Keep a failed pass marked as started. A later process restart is the
	// explicit retry boundary and avoids repeatedly rescanning a damaged store.
	s.historyMu.Unlock()
	if err != nil {
		log.Printf("DataNode history index backfill failed space=%s dataset=%s: %v", spaceID, datasetID, err)
		return
	}
	log.Printf("DataNode history index backfill completed space=%s dataset=%s", spaceID, datasetID)
}

func (s *Store) historyContext() context.Context {
	if s.historyCtx != nil {
		return s.historyCtx
	}
	return context.Background()
}

func (s *Store) backfillHistoryIndexSync(ctx context.Context, spaceID, datasetID string) error {

	prefix := []byte{fieldNamespace, timeSeriesKind}
	prefix = appendRawPart(prefix, []byte(spaceID))
	prefix = appendRawPart(prefix, []byte(datasetID))
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: nextPrefix(prefix)})
	if err != nil {
		return err
	}
	defer iter.Close()
	batch := s.db.NewBatch()
	defer batch.Close()
	pending := 0
	lastHistoryKey := ""
	commit := func() error {
		if pending == 0 {
			return nil
		}
		if err := batch.Commit(s.writeOptions); err != nil {
			return err
		}
		batch.Close()
		batch = s.db.NewBatch()
		pending = 0
		// History markers are derived data. Yield briefly between batches so a
		// live PrimaryStore point read is not starved by Pebble write/compaction
		// work while a large legacy dataset is being materialized.
		time.Sleep(50 * time.Millisecond)
		return nil
	}
	for valid := iter.First(); valid; valid = iter.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, ok := parseTimeSeriesFieldKey(iter.Key())
		if !ok {
			continue
		}
		historyKey, err := encodeHistoryKey(row, s.bucketDuration)
		if err != nil {
			continue
		}
		if string(historyKey) == lastHistoryKey {
			continue
		}
		if err := batch.Set(historyKey, nil, s.writeOptions); err != nil {
			return err
		}
		lastHistoryKey = string(historyKey)
		pending++
		if pending >= 4096 {
			if err := commit(); err != nil {
				return err
			}
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	return nil
}

func historyDatasetPrefix(spaceID, datasetID string) []byte {
	prefix := []byte{historyNamespace, timeSeriesKind}
	prefix = appendRawPart(prefix, []byte(spaceID))
	return appendRawPart(prefix, []byte(datasetID))
}

func historyTimeBound(prefix []byte, at time.Time) []byte {
	bound := append([]byte(nil), prefix...)
	return appendRawPart(bound, []byte(at.UTC().Format(canonicalTimeLayout)))
}

type historySelector struct {
	subject, freq, tag string
	hasTag             bool
}

// historyCandidateHeap keeps only the logical page boundary while the
// time-ordered history index is scanned. Ascending scans use a max-heap;
// descending scans use a min-heap.
type historyCandidateHeap struct {
	items []*pb.RowKey
	desc  bool
}

func (h historyCandidateHeap) Len() int { return len(h.items) }
func (h historyCandidateHeap) Less(i, j int) bool {
	if h.desc {
		return historyKeyLess(h.items[i].GetTimeSeries(), h.items[j].GetTimeSeries())
	}
	return historyKeyLess(h.items[j].GetTimeSeries(), h.items[i].GetTimeSeries())
}
func (h historyCandidateHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *historyCandidateHeap) Push(value any) {
	h.items = append(h.items, value.(*pb.RowKey))
}
func (h *historyCandidateHeap) Pop() any {
	last := len(h.items) - 1
	value := h.items[last]
	h.items = h.items[:last]
	return value
}

type historySelectorSet struct {
	wildcard map[string]struct{}
	exact    map[string]struct{}
}

func newHistorySelectorSet(selectors []historySelector) historySelectorSet {
	set := historySelectorSet{wildcard: make(map[string]struct{}), exact: make(map[string]struct{})}
	for _, selector := range selectors {
		base := selector.subject + "\x00" + selector.freq
		if selector.hasTag {
			set.exact[base+"\x00"+selector.tag] = struct{}{}
		} else {
			set.wildcard[base] = struct{}{}
		}
	}
	return set
}

func historyMatches(key *pb.RowKey, selectors historySelectorSet, start, end time.Time) bool {
	row := key.GetTimeSeries()
	if row == nil {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, row.GetDataTime())
	if err != nil {
		return false
	}
	at = at.UTC()
	if !start.IsZero() && at.Before(start) || !end.IsZero() && !at.Before(end) {
		return false
	}
	if len(selectors.wildcard) == 0 && len(selectors.exact) == 0 {
		return true
	}
	base := row.GetSubjectId() + "\x00" + row.GetFreq()
	if _, ok := selectors.wildcard[base]; ok {
		return true
	}
	_, ok := selectors.exact[base+"\x00"+row.GetSeriesTag()]
	return ok
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
	rest := key[2:]
	parts := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		part, next, err := decodePart(rest)
		if err != nil {
			return nil, false
		}
		parts = append(parts, part)
		rest = next
	}
	if len(parts) != 6 {
		return nil, false
	}
	return &pb.RowKey{SpaceId: parts[0], DatasetId: parts[1], Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{DataTime: parts[2], SubjectId: parts[3], Freq: parts[4], SeriesTag: parts[5]}}}, true
}
