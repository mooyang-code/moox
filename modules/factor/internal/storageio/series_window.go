package storageio

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (c *Client) readSeriesWindow(ctx context.Context, key WindowKey, span *pb.TimeRange, limit int, columns []string) ([]*pb.TimeSeriesRow, []time.Time, bool, time.Time, error) {
	rsp, periods, err := c.querySeriesWindow(ctx, key, span, limit, columns)
	if err != nil {
		return nil, nil, false, time.Time{}, err
	}
	return rsp.GetRows(), periods, false, time.Time{}, nil
}

func (c *Client) querySeriesWindow(ctx context.Context, key WindowKey, span *pb.TimeRange, limit int, columns []string) (*pb.QueryTimeSeriesRowsRsp, []time.Time, error) {
	return c.querySeriesWindowAttempt(ctx, key, span, limit, columns, true)
}

func (c *Client) querySeriesWindowAttempt(ctx context.Context, key WindowKey, span *pb.TimeRange, limit int, columns []string, refresh bool) (*pb.QueryTimeSeriesRowsRsp, []time.Time, error) {
	fail := func(err error) (*pb.QueryTimeSeriesRowsRsp, []time.Time, error) {
		return nil, nil, err
	}
	ids := uniqueSortedStrings(append(append([]string{}, key.SubjectIDs...), key.SubjectID))
	if len(columns) > 0 && len(ids) > pb.SeriesWindowSubjectLimit(limit, len(uniqueSortedStrings(columns))) {
		return fail(nonRetryableRead(fmt.Errorf("series window exceeds projected cell budget")))
	}
	if c.view == nil || key.SpaceID == "" || key.SourceViewID == "" || key.Freq == "" || !key.FilterSourceSeriesTag || key.InputContractVersion == "" || key.ExpectedActiveIndexRevision != 0 || len(ids) == 0 || len(ids) > 512 || limit < 1 || limit > 10000 || len(ids)*limit > 50000 {
		return fail(nonRetryableRead(fmt.Errorf("series window requires bounded explicit series, DataView and contract without global revision")))
	}
	span = boundSeriesWindowSpan(key.Freq, span, limit)
	end, err := time.Parse(time.RFC3339Nano, span.GetEndTime())
	if err != nil {
		return fail(nonRetryableRead(err))
	}
	var start time.Time
	if raw := span.GetStartTime(); raw != "" {
		start, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil || !start.Before(end) {
			return fail(nonRetryableRead(fmt.Errorf("invalid series window start time")))
		}
	}
	selectors := make([]*pb.TimeSeriesSelector, 0, len(ids))
	counts := make(map[string]int, len(ids))
	for _, id := range ids {
		counts[id] = 0
		// SourceDataset is a logical View alias in factor tasks, not the View's
		// primary dataset. Let the contract-fenced service supply that scope.
		selectors = append(selectors, &pb.TimeSeriesSelector{SpaceId: key.SpaceID, SubjectId: id, Freq: key.Freq, SeriesTag: &key.SourceSeriesTag})
	}
	rsp, err := c.view.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: c.viewRequestAuth(), SpaceId: key.SpaceID, ViewId: key.SourceViewID, Selectors: selectors, TimeRange: span, ColumnNames: append([]string(nil), columns...), RowsPerSeries: uint32(limit), TotalMode: pb.TotalMode_NONE, ExpectedInputContractVersion: key.InputContractVersion})
	if err != nil {
		return fail(err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return fail(fmt.Errorf("series window response is missing"))
	}
	if err := classifyViewReadRet("read series window", rsp.GetRetInfo()); err != nil {
		nextContract := strings.TrimSpace(rsp.GetServedInputContractVersion())
		if refresh && nextContract != "" && nextContract != key.InputContractVersion {
			key.InputContractVersion = nextContract
			return c.querySeriesWindowAttempt(ctx, key, span, limit, columns, false)
		}
		return fail(err)
	}
	if rsp.GetServedInputContractVersion() != key.InputContractVersion {
		return fail(fmt.Errorf("served series window index or contract changed"))
	}
	seenRows := make(map[string]map[int64]bool, len(ids))
	seenPeriods := map[int64]bool{}
	var periods []time.Time
	for _, row := range rsp.GetRows() {
		k := row.GetKey()
		count, requested := counts[k.GetSubjectId()]
		at, parseErr := time.Parse(time.RFC3339Nano, k.GetDataTime())
		if !requested || count >= limit || k.GetFreq() != key.Freq || k.GetSeriesTag() != key.SourceSeriesTag || parseErr != nil || !at.Before(end) || (!start.IsZero() && at.Before(start)) {
			return fail(nonRetryableRead(fmt.Errorf("series window returned a row outside its requested scope")))
		}
		if seenRows[k.GetSubjectId()] == nil {
			seenRows[k.GetSubjectId()] = map[int64]bool{}
		}
		if seenRows[k.GetSubjectId()][at.UnixNano()] {
			return fail(nonRetryableRead(fmt.Errorf("series window returned a duplicate row")))
		}
		seenRows[k.GetSubjectId()][at.UnixNano()] = true
		counts[k.GetSubjectId()]++
		if !seenPeriods[at.UnixNano()] {
			seenPeriods[at.UnixNano()] = true
			periods = append(periods, at.UTC())
		}
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i].Before(periods[j]) })
	return rsp, periods, nil
}

func boundSeriesWindowSpan(freq string, span *pb.TimeRange, limit int) *pb.TimeRange {
	if span == nil || span.GetStartTime() != "" || strings.TrimSpace(freq) == "" || limit < 1 {
		return span
	}
	end, err := time.Parse(time.RFC3339Nano, span.GetEndTime())
	if err != nil {
		return span
	}
	period, err := domain.ParseFrequency(freq)
	if err != nil {
		return span
	}
	start := end.UTC().Add(-time.Duration(limit) * period)
	return &pb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: span.GetEndTime()}
}
