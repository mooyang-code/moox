package klinecheck

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// countingRows 在 fakeRows 之上提供按标的计数，并记录哪些标的真的被读了行。
type countingRows struct {
	*fakeRows
	countCalls int
	readSubs   map[string]int
	countErr   error
}

func newCountingRows(missing map[string]map[int64]bool) *countingRows {
	return &countingRows{fakeRows: &fakeRows{missing: missing}, readSubs: map[string]int{}}
}

func (c *countingRows) CountTimeSeriesRows(_ context.Context, req *storagepb.CountTimeSeriesRowsReq, _ ...client.Option) (*storagepb.CountTimeSeriesRowsRsp, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.countCalls++
	if c.countErr != nil {
		return nil, c.countErr
	}
	start, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetStartTime())
	end, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetEndTime())
	rsp := &storagepb.CountTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}
	for _, selector := range req.GetSelectors() {
		rows := 0
		for t := start; t.Before(end); t = t.Add(time.Minute) {
			if !c.missing[selector.GetSubjectId()][t.Unix()] {
				rows++
			}
		}
		if rows > 0 {
			rsp.Counts = append(rsp.Counts, &storagepb.SubjectRowCount{SubjectId: selector.GetSubjectId(), Rows: uint32(rows)})
		}
	}
	return rsp, nil
}

func (c *countingRows) ReadTimeSeriesRows(ctx context.Context, req *storagepb.ReadTimeSeriesRowsReq, opts ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	c.mu.Lock()
	for _, selector := range req.GetSelectors() {
		c.readSubs[selector.GetSubjectId()]++
	}
	c.mu.Unlock()
	return c.fakeRows.ReadTimeSeriesRows(ctx, req, opts...)
}

func (c *countingRows) readSubjects() []string {
	out := make([]string, 0, len(c.readSubs))
	for subject := range c.readSubs {
		out = append(out, subject)
	}
	sort.Strings(out)
	return out
}

// 先比对根数：只有根数对不上的标的读行，结果与逐行比对一致。
func TestCheckCountsFirstAndReadsOnlySubjectsWithMissingBars(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT", "ETH-USDT", "SOL-USDT")}
	missing := map[string]map[int64]bool{}
	markMissingIn(missing, "ETH-USDT", base.Add(3*time.Minute), base.Add(5*time.Minute))
	request := Request{SpaceID: "crypto", From: base, To: base.Add(20 * time.Minute)}
	now := func() time.Time { return base.Add(time.Hour) }

	counted := newCountingRows(missing)
	result, err := (&Checker{Plans: fakePlans{records}, Rows: counted, Now: now}).Check(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, []string{"ETH-USDT"}, counted.readSubjects(), "根数齐全的标的不读行")
	require.Equal(t, 1, counted.countCalls, "三个标的合成一次计数")

	rows := &fakeRows{missing: missing}
	expected, err := (&Checker{Plans: fakePlans{records}, Rows: rows, Now: now}).Check(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, expected, result, "与逐行比对的结果一致")
	require.Equal(t, 2, result.Missing)
	require.Equal(t, 60, result.Expected)
}

// 已确认的行情源缺失计入“应有 - 缺失”的比对：只缺这些 K 线的标的同样不读行。
func TestCheckCountsSubtractConfirmedAbsences(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT", "ETH-USDT")}
	missing := map[string]map[int64]bool{}
	markMissingIn(missing, "BTC-USDT", base.Add(2*time.Minute), base.Add(3*time.Minute))
	markMissingIn(missing, "ETH-USDT", base.Add(2*time.Minute), base.Add(4*time.Minute))
	repo := openBackfillStore(t)
	require.NoError(t, repo.AddAbsences(context.Background(), []store.SourceAbsence{
		{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", SubjectID: "BTC-USDT", From: base.Add(2 * time.Minute), To: base.Add(3 * time.Minute), Bars: 1},
		{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", SubjectID: "ETH-USDT", From: base.Add(2 * time.Minute), To: base.Add(3 * time.Minute), Bars: 1},
	}))
	counted := newCountingRows(missing)
	result, err := (&Checker{Plans: fakePlans{records}, Rows: counted, Absences: repo, Now: func() time.Time { return base.Add(time.Hour) }}).Check(context.Background(),
		Request{SpaceID: "crypto", From: base, To: base.Add(10 * time.Minute)})
	require.NoError(t, err)
	require.Equal(t, []string{"ETH-USDT"}, counted.readSubjects(), "BTC 只缺已确认的那根，不用读行")
	require.Equal(t, 2, result.Absent)
	require.Equal(t, 1, result.Missing)
	require.Len(t, result.Gaps, 1)
	require.True(t, result.Gaps[0].From.Equal(base.Add(3*time.Minute)))
}

// 计数失败（例如 Storage 还没升级）时退回逐行比对，检查照常完成。
func TestCheckFallsBackToRowReadsWhenCountingFails(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT", "ETH-USDT")}
	missing := map[string]map[int64]bool{}
	markMissingIn(missing, "ETH-USDT", base.Add(3*time.Minute), base.Add(4*time.Minute))
	counted := newCountingRows(missing)
	counted.countErr = errors.New("unknown method CountTimeSeriesRows")
	result, err := (&Checker{Plans: fakePlans{records}, Rows: counted, Now: func() time.Time { return base.Add(time.Hour) }}).Check(context.Background(),
		Request{SpaceID: "crypto", From: base, To: base.Add(10 * time.Minute)})
	require.NoError(t, err)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, counted.readSubjects())
	require.Equal(t, 1, result.Missing)

	// 深度检查要校验字段，不走计数。
	deep := newCountingRows(missing)
	_, err = (&Checker{Plans: fakePlans{records}, Rows: deep, Now: func() time.Time { return base.Add(time.Hour) }}).Check(context.Background(),
		Request{SpaceID: "crypto", From: base, To: base.Add(10 * time.Minute), Deep: true})
	require.NoError(t, err)
	require.Zero(t, deep.countCalls)
}

// 历史扫描先计数：一根都没有的标的（没有历史）与根数齐全的标的都不读行，结果与逐行比对一致。
func TestCheckHistoryCountsFirst(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	horizon := base.Add(-historyScanBars * time.Minute)
	missing := map[string]map[int64]bool{}
	hole := base.Add(-30 * time.Minute)
	markMissingIn(missing, "A-USDT", hole, hole.Add(3*time.Minute))
	markMissingIn(missing, "B-USDT", horizon, base.Add(-100*time.Minute))
	markMissingIn(missing, "C-USDT", horizon, base)
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "A-USDT", "B-USDT", "C-USDT", "G-USDT")}
	now := func() time.Time { return base.Add(2 * time.Hour) }

	counted := newCountingRows(missing)
	result, err := (&Checker{Plans: fakePlans{records}, Rows: counted, Now: now}).CheckHistory(context.Background(), "crypto", "ds", "1m")
	require.NoError(t, err)
	require.Equal(t, []string{"A-USDT", "B-USDT"}, counted.readSubjects(), "C 没有历史、G 齐全，都不读行")

	expected, err := (&Checker{Plans: fakePlans{records}, Rows: &fakeRows{missing: missing}, Now: now}).CheckHistory(context.Background(), "crypto", "ds", "1m")
	require.NoError(t, err)
	require.Equal(t, expected, result)
	require.Equal(t, 3, result.WithHistory)
	require.Len(t, result.Gaps, 1)
}

func markMissingIn(missing map[string]map[int64]bool, subject string, from, to time.Time) {
	if missing[subject] == nil {
		missing[subject] = map[int64]bool{}
	}
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		missing[subject][t.Unix()] = true
	}
}
