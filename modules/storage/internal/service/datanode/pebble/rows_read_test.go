package pebble

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func openReadStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func tsRow(subject, tag string, at time.Time, close float64) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: subject, Freq: "1m", DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: tag,
		}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: close}}}},
	}
}

func sel(subject string) *pb.TimeSeriesSelector {
	return &pb.TimeSeriesSelector{SpaceId: "s", DatasetId: "d", SubjectId: subject, Freq: "1m"}
}

func selTag(subject, tag string) *pb.TimeSeriesSelector {
	s := sel(subject)
	s.SeriesTag = &tag
	return s
}

// readAll 用游标顺序读完，返回全部行的标识（时间|标的|标签）及页数。
func readAll(t *testing.T, store *Store, req *pb.ReadTimeSeriesRowsReq, pageSize uint32) ([]string, int) {
	t.Helper()
	var ids []string
	pages, cursor := 0, ""
	for {
		clone := proto.Clone(req).(*pb.ReadTimeSeriesRowsReq)
		clone.Page = &pb.Page{Size: pageSize, Cursor: cursor}
		rsp, err := store.ReadTimeSeriesRows(context.Background(), clone)
		require.NoError(t, err)
		pages++
		for _, row := range rsp.GetRows() {
			k := row.GetKey()
			ids = append(ids, k.GetDataTime()+"|"+k.GetSubjectId()+"|"+k.GetSeriesTag())
		}
		cursor = rsp.GetPageResult().GetNextCursor()
		if cursor == "" {
			require.False(t, rsp.GetPageResult().GetHasMore())
			return ids, pages
		}
		require.True(t, rsp.GetPageResult().GetHasMore())
		require.Less(t, pages, 10000, "cursor does not advance")
	}
}

func seedRangeData(t *testing.T, store *Store) (start time.Time, expected []string) {
	t.Helper()
	start = time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	var rows []*pb.RowFieldUpsert
	for minute := 0; minute < 30; minute++ {
		at := start.Add(time.Duration(minute) * time.Minute)
		for _, series := range []struct{ subject, tag string }{{"A", ""}, {"A", "venue:x"}, {"B", ""}, {"C", "venue:x"}} {
			if (minute+len(series.subject)+len(series.tag))%3 == 0 {
				continue // 制造空洞，各序列的时间点不同
			}
			rows = append(rows, tsRow(series.subject, series.tag, at, float64(minute)))
		}
	}
	require.NoError(t, store.UpsertFields(context.Background(), rows))
	for _, row := range rows {
		k := row.GetKey().GetTimeSeries()
		expected = append(expected, outputTime(mustCanonical(k.GetDataTime()))+"|"+k.GetSubjectId()+"|"+k.GetSeriesTag())
	}
	return start, expected
}

func mustCanonical(raw string) string {
	at, _ := time.Parse(time.RFC3339Nano, raw)
	return canonicalTime(at)
}

func sortedByTime(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.Split(out[i], "|"), strings.Split(out[j], "|")
		for k := 0; k < 3; k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
	return out
}

func filterSubjects(ids []string, subjects ...string) []string {
	var out []string
	for _, id := range ids {
		for _, s := range subjects {
			if strings.Split(id, "|")[1] == s {
				out = append(out, id)
			}
		}
	}
	return out
}

func rangeReq(start, end time.Time, selectors ...*pb.TimeSeriesSelector) *pb.ReadTimeSeriesRowsReq {
	return &pb.ReadTimeSeriesRowsReq{
		SpaceId: "s", DatasetId: "d", Selectors: selectors, ColumnNames: []string{"close"},
		TimeRange: &pb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)},
	}
}

func TestRangeReadWithoutSelectorsPagesThroughTheWholeDataset(t *testing.T) {
	store := openReadStore(t)
	start, all := seedRangeData(t, store)
	end := start.Add(30 * time.Minute)
	want := sortedByTime(all)
	for _, size := range []uint32{1, 7, 1000} {
		got, _ := readAll(t, store, rangeReq(start, end), size)
		require.Equal(t, want, got, "page size %d", size)
	}
	// 区间左闭右开。
	got, _ := readAll(t, store, rangeReq(start.Add(5*time.Minute), start.Add(10*time.Minute)), 4)
	for _, id := range got {
		at := strings.Split(id, "|")[0]
		require.GreaterOrEqual(t, at, start.Add(5*time.Minute).Format(time.RFC3339Nano))
		require.Less(t, at, start.Add(10*time.Minute).Format(time.RFC3339Nano))
	}
	require.NotEmpty(t, got)
}

func TestRangeReadWithSelectorsMatchesFilteredFullScan(t *testing.T) {
	store := openReadStore(t)
	start, all := seedRangeData(t, store)
	end := start.Add(30 * time.Minute)
	cases := map[string][]*pb.TimeSeriesSelector{
		"one subject all tags": {sel("A")},
		"two subjects":         {sel("A"), sel("C")},
		"exact tag":            {selTag("A", "venue:x")},
		"default tag only":     {selTag("A", "")},
		"overlapping":          {sel("A"), selTag("A", "venue:x"), sel("B")},
	}
	for name, selectors := range cases {
		t.Run(name, func(t *testing.T) {
			full, _ := readAll(t, store, rangeReq(start, end), 1000)
			var want []string
			for _, id := range full {
				parts := strings.Split(id, "|")
				for _, s := range selectors {
					if parts[1] == s.GetSubjectId() && (s.SeriesTag == nil || s.GetSeriesTag() == parts[2]) {
						want = append(want, id)
						break
					}
				}
			}
			require.NotEmpty(t, want)
			for _, size := range []uint32{1, 3, 1000} {
				got, _ := readAll(t, store, rangeReq(start, end, selectors...), size)
				require.Equal(t, want, got, "page size %d", size)
			}
		})
	}
	_ = all
}

func TestRangeReadRejectsInvalidShapes(t *testing.T) {
	store := openReadStore(t)
	ctx := context.Background()
	base := func() *pb.ReadTimeSeriesRowsReq {
		return &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "d", ColumnNames: []string{"close"}, TimeRange: &pb.TimeRange{StartTime: "2026-08-16T00:00:00Z", EndTime: "2026-08-16T01:00:00Z"}}
	}
	cases := map[string]func(*pb.ReadTimeSeriesRowsReq){
		"no columns":    func(r *pb.ReadTimeSeriesRowsReq) { r.ColumnNames = nil },
		"both shapes":   func(r *pb.ReadTimeSeriesRowsReq) { r.LatestPerSeries = 3 },
		"neither shape": func(r *pb.ReadTimeSeriesRowsReq) { r.TimeRange = nil },
		"bad cursor":    func(r *pb.ReadTimeSeriesRowsReq) { r.Page = &pb.Page{Cursor: "%%%"} },
		"inverted range": func(r *pb.ReadTimeSeriesRowsReq) {
			r.TimeRange.StartTime, r.TimeRange.EndTime = r.TimeRange.EndTime, r.TimeRange.StartTime
		},
		"selector no subj": func(r *pb.ReadTimeSeriesRowsReq) { r.Selectors = []*pb.TimeSeriesSelector{{Freq: "1m"}} },
	}
	for name, mutate := range cases {
		req := base()
		mutate(req)
		_, err := store.ReadTimeSeriesRows(ctx, req)
		require.Error(t, err, name)
	}
}

func seedLatestData(t *testing.T, store *Store, bars int) time.Time {
	t.Helper()
	start := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	var rows []*pb.RowFieldUpsert
	for i := 0; i < bars; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		for _, series := range []struct{ subject, tag string }{{"A", ""}, {"A", "venue:b"}, {"B", ""}, {"C", "venue:a"}, {"C", "venue:b"}, {"D", ""}} {
			rows = append(rows, tsRow(series.subject, series.tag, at, float64(i)))
		}
	}
	require.NoError(t, store.UpsertFields(context.Background(), rows))
	return start
}

func latestReq(per uint32, selectors ...*pb.TimeSeriesSelector) *pb.ReadTimeSeriesRowsReq {
	return &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "d", Selectors: selectors, ColumnNames: []string{"close"}, LatestPerSeries: per}
}

func TestLatestPerSeriesWithoutSelectorsCoversEverySeriesAcrossPages(t *testing.T) {
	store := openReadStore(t)
	start := seedLatestData(t, store, 40)
	const per = 5
	want := map[string]int{"A|": per, "A|venue:b": per, "B|": per, "C|venue:a": per, "C|venue:b": per, "D|": per}
	for _, size := range []uint32{1, 4, 5, 6, 7, 1000} {
		ids, _ := readAll(t, store, latestReq(per), size)
		counts := map[string]int{}
		lastSeries, lastTime := "", ""
		seenSeries := map[string]bool{}
		for _, id := range ids {
			parts := strings.Split(id, "|")
			series := parts[1] + "|" + parts[2]
			counts[series]++
			if series != lastSeries {
				require.False(t, seenSeries[series], "page size %d: series %s split into two runs", size, series)
				seenSeries[series] = true
				lastSeries, lastTime = series, ""
			}
			if lastTime != "" {
				require.Less(t, parts[0], lastTime, "page size %d: not newest first", size)
			}
			lastTime = parts[0]
			newest := start.Add(39 * time.Minute)
			require.GreaterOrEqual(t, parts[0], newest.Add(-per*time.Minute).Format(time.RFC3339Nano))
		}
		require.Equal(t, want, counts, "page size %d", size)
	}
	// 标的按 subject_id 升序分组。
	ids, _ := readAll(t, store, latestReq(per), 1000)
	var subjects []string
	for _, id := range ids {
		s := strings.Split(id, "|")[1]
		if len(subjects) == 0 || subjects[len(subjects)-1] != s {
			subjects = append(subjects, s)
		}
	}
	require.Equal(t, []string{"A", "B", "C", "D"}, subjects)
}

func TestLatestPerSeriesWithSelectorsOnlyReadsSelectedSubjects(t *testing.T) {
	store := openReadStore(t)
	seedLatestData(t, store, 12)
	for _, size := range []uint32{1, 3, 100} {
		ids, _ := readAll(t, store, latestReq(4, sel("C"), sel("A"), selTag("A", "venue:b")), size)
		counts := map[string]int{}
		for _, id := range ids {
			parts := strings.Split(id, "|")
			counts[parts[1]+"|"+parts[2]]++
		}
		require.Equal(t, map[string]int{"A|": 4, "A|venue:b": 4, "C|venue:a": 4, "C|venue:b": 4}, counts, "page size %d", size)
	}
}

func TestLatestPerSeriesPageLimitedByValueCount(t *testing.T) {
	store := openReadStore(t)
	seedLatestData(t, store, 10)
	cols := make([]string, 0, 100001)
	for i := 0; i < 100001; i++ {
		cols = append(cols, fmt.Sprintf("c%d", i))
	}
	// 列数超过 20 万值上限的一半：每页最多 1 行。
	req := latestReq(3)
	req.ColumnNames = cols
	rsp, err := store.ReadTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.LessOrEqual(t, len(rsp.GetRows()), 1)
	require.True(t, rsp.GetPageResult().GetHasMore())
}

func TestLatestPerSeriesCursorFromAnotherRequestIsRejected(t *testing.T) {
	store := openReadStore(t)
	seedLatestData(t, store, 12)
	req := latestReq(3, sel("A"))
	req.Page = &pb.Page{Size: 1}
	rsp, err := store.ReadTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	cursor := rsp.GetPageResult().GetNextCursor()
	require.NotEmpty(t, cursor)
	other := latestReq(3, sel("B"))
	other.Page = &pb.Page{Size: 1, Cursor: cursor}
	_, err = store.ReadTimeSeriesRows(context.Background(), other)
	require.Error(t, err)
}

func TestCountTimeSeriesRowsCountsDistinctTimePointsPerSubject(t *testing.T) {
	store := openReadStore(t)
	start, all := seedRangeData(t, store)
	end := start.Add(30 * time.Minute)
	full, _ := readAll(t, store, rangeReq(start, end), 1000)
	distinct := map[string]map[string]bool{}
	for _, id := range full {
		parts := strings.Split(id, "|")
		if distinct[parts[1]] == nil {
			distinct[parts[1]] = map[string]bool{}
		}
		distinct[parts[1]][parts[0]] = true
	}
	_ = all
	counts, err := store.CountTimeSeriesRows(context.Background(), &pb.CountTimeSeriesRowsReq{
		SpaceId: "s", DatasetId: "d", Selectors: []*pb.TimeSeriesSelector{sel("A"), sel("B"), sel("C"), sel("Z")},
		TimeRange: &pb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)},
	})
	require.NoError(t, err)
	got := map[string]uint32{}
	for _, c := range counts {
		got[c.GetSubjectId()] = c.GetRows()
	}
	require.Equal(t, map[string]uint32{"A": uint32(len(distinct["A"])), "B": uint32(len(distinct["B"])), "C": uint32(len(distinct["C"]))}, got)
	// A 有两个序列，同一时间点只计一次，所以少于两个序列的行数之和。
	require.Less(t, got["A"], uint32(len(filterSubjects(full, "A"))))
}

func TestReadRowsSkipsMissingKeysAndKeepsRequestOrder(t *testing.T) {
	store := openReadStore(t)
	at := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{
		tsRow("A", "", at, 1), tsRow("B", "", at, 2),
		{Key: tsRow("C", "", at, 0).Key, Attributes: map[string]*pb.TypedValue{"source": {Value: &pb.TypedValue_StringValue{StringValue: "x"}}}},
	}))
	key := func(subject string) *pb.RowKey { return tsRow(subject, "", at, 0).Key }
	rows, err := store.ReadRows(context.Background(), []*pb.RowKey{key("B"), key("missing"), key("A"), key("C"), key("A")}, []string{"close"}, []string{"source"})
	require.NoError(t, err)
	var order []string
	for _, row := range rows {
		order = append(order, row.GetKey().GetTimeSeries().GetSubjectId())
	}
	require.Equal(t, []string{"B", "A", "C", "A"}, order)
	require.Len(t, rows[0].GetFields(), 1)
	require.Equal(t, 2.0, rows[0].GetFields()[0].GetValue().GetDoubleValue())
	require.Empty(t, rows[2].GetFields(), "属性行没有请求的字段")
	require.Equal(t, "x", rows[2].GetAttributes()["source"].GetStringValue())
	require.Empty(t, rows[0].GetAttributes()["source"])
}

func TestReadRowsHandlesMoreThanTenThousandRowsOverTheKeyLimit(t *testing.T) {
	store := openReadStore(t)
	_, err := store.ReadRows(context.Background(), make([]*pb.RowKey, maxReadRowsKeys+1), []string{"close"}, nil)
	require.Error(t, err)
}

func TestRangeReadReturnsMoreThanTenThousandRowsWithFewColumns(t *testing.T) {
	store := openReadStore(t)
	start := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	var rows []*pb.RowFieldUpsert
	for i := 0; i < 2500; i++ {
		for _, subject := range []string{"A", "B", "C", "D", "E"} {
			rows = append(rows, tsRow(subject, "", start.Add(time.Duration(i)*time.Minute), float64(i)))
		}
	}
	require.NoError(t, store.UpsertFields(context.Background(), rows))
	// 一次最近 N 根返回 12500 行（超过 1 万个键），只请求一列。
	rsp, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		SpaceId: "s", DatasetId: "d", ColumnNames: []string{"close"}, LatestPerSeries: 2500, Page: &pb.Page{Size: 20000},
	})
	require.NoError(t, err)
	require.Len(t, rsp.GetRows(), 12500)
	require.Empty(t, rsp.GetPageResult().GetNextCursor())
}
