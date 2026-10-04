package storageio

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
)

const readPageSize = 2000

const earliestPeriodSelectorBatchSize = 128

func (c *Client) EarliestPeriod(ctx context.Context, spaceID, datasetID, freq string, subjects, columns []string) (time.Time, bool, error) {
	if err := c.primaryReady("read earliest dataset period"); err != nil {
		return time.Time{}, false, err
	}
	spaceID, datasetID, freq = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID), strings.TrimSpace(freq)
	if spaceID == "" || datasetID == "" || freq == "" {
		return time.Time{}, false, fmt.Errorf("space_id, dataset_id and freq are required")
	}
	var err error
	subjects, err = normalizeReadList("subjects", subjects, true)
	if err != nil {
		return time.Time{}, false, err
	}
	columns, err = normalizeReadList("columns", columns, true)
	if err != nil {
		return time.Time{}, false, err
	}
	end := time.Now().UTC()
	var earliest time.Time
	for offset := 0; offset < len(subjects); offset += earliestPeriodSelectorBatchSize {
		limit := offset + earliestPeriodSelectorBatchSize
		if limit > len(subjects) {
			limit = len(subjects)
		}
		selectors := make([]*storagepb.TimeSeriesSelector, 0, limit-offset)
		for _, subject := range subjects[offset:limit] {
			selectors = append(selectors, &storagepb.TimeSeriesSelector{
				SpaceId: spaceID, DatasetId: datasetID, SubjectId: subject, Freq: freq,
			})
		}
		rsp, callErr := c.primary.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			AuthInfo: c.auth, SpaceId: spaceID, DatasetId: datasetID, Selectors: selectors,
			TimeRange: &storagepb.TimeRange{StartTime: "0001-01-01T00:00:00Z", EndTime: end.Format(time.RFC3339Nano)},
			Order:     storagepb.SortOrder_SORT_ORDER_ASC, ColumnNames: columns,
			Page: &commonpb.Page{Page: 1, Size: 1},
		})
		if callErr != nil {
			return time.Time{}, false, rpcError("read earliest dataset period", callErr)
		}
		if rsp == nil {
			return time.Time{}, false, fmt.Errorf("%w: read earliest dataset period returned an empty response", ErrInfra)
		}
		if err := readResponseError("read earliest dataset period", rsp.GetRetInfo()); err != nil {
			return time.Time{}, false, err
		}
		if len(rsp.GetRows()) == 0 {
			continue
		}
		row := rsp.GetRows()[0]
		if row == nil || row.GetKey() == nil || row.GetKey().GetSpaceId() != spaceID ||
			row.GetKey().GetDatasetId() != datasetID || row.GetKey().GetFreq() != freq {
			return time.Time{}, false, fmt.Errorf("%w: earliest dataset period returned an invalid row identity", ErrInfra)
		}
		period, parseErr := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if parseErr != nil {
			return time.Time{}, false, fmt.Errorf("parse earliest dataset period %q: %w", row.GetKey().GetDataTime(), parseErr)
		}
		if earliest.IsZero() || period.Before(earliest) {
			earliest = period.UTC()
		}
	}
	return earliest, !earliest.IsZero(), nil
}

func (c *Client) ReadWindow(ctx context.Context, req ReadRequest) (map[string]*Frame, error) {
	if err := c.primaryReady("read time-series window"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SpaceID) == "" || strings.TrimSpace(req.DatasetID) == "" || strings.TrimSpace(req.Freq) == "" {
		return nil, fmt.Errorf("space_id, dataset_id and freq are required")
	}
	if req.Start.IsZero() || req.End.IsZero() || !req.Start.Before(req.End) {
		return nil, fmt.Errorf("read window start must be before end")
	}
	subjects, err := normalizeReadList("subjects", req.Subjects, true)
	if err != nil {
		return nil, err
	}
	columns, err := normalizeReadList("columns", req.Columns, false)
	if err != nil {
		return nil, err
	}
	frames := make(map[string]*Frame, len(subjects))
	for _, subject := range subjects {
		frames[subject] = &Frame{
			SubjectID: subject,
			Columns:   append([]string{"data_time", "series_tag"}, columns...),
			Rows:      make([][]any, 0),
		}
	}
	if len(subjects) == 0 {
		return frames, nil
	}

	selectors := make([]*storagepb.TimeSeriesSelector, 0, len(subjects))
	for _, subject := range subjects {
		selectors = append(selectors, &storagepb.TimeSeriesSelector{
			SpaceId: req.SpaceID, DatasetId: req.DatasetID, SubjectId: subject, Freq: req.Freq,
		})
	}
	timeRange := &storagepb.TimeRange{
		StartTime: req.Start.UTC().Format(time.RFC3339Nano),
		EndTime:   req.End.UTC().Format(time.RFC3339Nano),
	}
	var afterKey []byte
	for page := 1; ; page++ {
		rsp, callErr := c.primary.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			AuthInfo: c.auth, SpaceId: req.SpaceID, DatasetId: req.DatasetID,
			Selectors: selectors, TimeRange: timeRange, Order: storagepb.SortOrder_SORT_ORDER_ASC,
			ColumnNames: columns, Page: &commonpb.Page{Page: 1, Size: readPageSize},
			AfterKey: append([]byte(nil), afterKey...),
		})
		if callErr != nil {
			return nil, rpcError("read time-series window", callErr)
		}
		if rsp == nil {
			return nil, fmt.Errorf("%w: read time-series window returned an empty response", ErrInfra)
		}
		if retErr := readResponseError("read time-series window", rsp.GetRetInfo()); retErr != nil {
			return nil, retErr
		}
		rows := rsp.GetRows()
		for _, row := range rows {
			if row == nil || row.GetKey() == nil {
				return nil, fmt.Errorf("read time-series window returned a row without identity")
			}
			frame, ok := frames[row.GetKey().GetSubjectId()]
			if !ok {
				return nil, fmt.Errorf("read time-series window returned unexpected subject %q", row.GetKey().GetSubjectId())
			}
			dataTime, parseErr := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
			if parseErr != nil {
				return nil, fmt.Errorf("parse row data_time %q: %w", row.GetKey().GetDataTime(), parseErr)
			}
			values, decodeErr := decodeFields(row.GetFields(), columns)
			if decodeErr != nil {
				return nil, decodeErr
			}
			frame.Rows = append(frame.Rows, append([]any{dataTime.UTC(), row.GetKey().GetSeriesTag()}, values...))
		}
		pageResult := rsp.GetPageResult()
		if pageResult == nil || !pageResult.GetHasMore() {
			break
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("%w: Storage reported another page without returning rows", ErrInfra)
		}
		last := rows[len(rows)-1]
		key, keyErr := proto.Marshal(&storagepb.RowKey{
			SpaceId: req.SpaceID, DatasetId: req.DatasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: last.GetKey().GetSubjectId(), Freq: last.GetKey().GetFreq(),
				DataTime: last.GetKey().GetDataTime(), SeriesTag: last.GetKey().GetSeriesTag(),
			}},
		})
		if keyErr != nil {
			return nil, fmt.Errorf("encode time-series page cursor: %w", keyErr)
		}
		if string(key) == string(afterKey) {
			return nil, fmt.Errorf("%w: Storage repeated the time-series page cursor", ErrInfra)
		}
		afterKey = key
		if page > 100000 {
			return nil, fmt.Errorf("%w: Storage pagination exceeded the page limit", ErrInfra)
		}
	}
	for _, frame := range frames {
		sort.Slice(frame.Rows, func(i, j int) bool {
			left, _ := frame.Rows[i][0].(time.Time)
			right, _ := frame.Rows[j][0].(time.Time)
			if !left.Equal(right) {
				return left.Before(right)
			}
			return frame.Rows[i][1].(string) < frame.Rows[j][1].(string)
		})
	}
	return frames, nil
}

func readResponseError(action string, ret *commonpb.RetInfo) error {
	if err := responseError(action, ret); err != nil {
		if ret != nil && (ret.GetCode() == commonpb.ErrorCode_VIEW_NOT_READY || ret.GetCode() == commonpb.ErrorCode_CONFLICT) {
			return fmt.Errorf("%w: %w", ErrInfra, err)
		}
		return err
	}
	return nil
}

func normalizeReadList(name string, values []string, requireOne bool) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s contains an empty value", name)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if requireOne && len(out) == 0 {
		return nil, fmt.Errorf("%s are required", name)
	}
	sort.Strings(out)
	return out, nil
}

func decodeFields(fields []*storagepb.FieldValue, columns []string) ([]any, error) {
	wanted := make(map[string]int, len(columns))
	for index, name := range columns {
		wanted[name] = index
	}
	values := make([]any, len(columns))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field == nil {
			continue
		}
		name := field.GetFieldId()
		if _, ok := wanted[name]; !ok {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("read time-series row contains duplicate field %q", name)
		}
		seen[name] = struct{}{}
		values[wanted[name]] = typedValueToAny(field.GetValue())
	}
	return values, nil
}

func typedValueToAny(value *storagepb.TypedValue) any {
	if value == nil {
		return nil
	}
	switch item := value.GetValue().(type) {
	case *storagepb.TypedValue_StringValue:
		return item.StringValue
	case *storagepb.TypedValue_IntValue:
		return item.IntValue
	case *storagepb.TypedValue_DoubleValue:
		return item.DoubleValue
	case *storagepb.TypedValue_BoolValue:
		return item.BoolValue
	case *storagepb.TypedValue_TimeValue:
		return item.TimeValue
	case *storagepb.TypedValue_JsonValue:
		return item.JsonValue
	case *storagepb.TypedValue_BytesValue:
		return append([]byte(nil), item.BytesValue...)
	case *storagepb.TypedValue_NullValue:
		return nil
	case *storagepb.TypedValue_ListValue:
		if item.ListValue == nil {
			return []any(nil)
		}
		out := make([]any, 0, len(item.ListValue.GetValues()))
		for _, child := range item.ListValue.GetValues() {
			out = append(out, typedValueToAny(child))
		}
		return out
	default:
		return nil
	}
}
