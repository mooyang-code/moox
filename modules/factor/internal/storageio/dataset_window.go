package storageio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

const primaryExactReadKeyLimit = 512

// ErrInsufficientHistory means the Primary window has fewer distinct periods than lookback.
var ErrInsufficientHistory = errors.New("insufficient primary history")

type datasetPrimaryReader interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
}

func usesPrimaryDatasetWindow(key WindowKey) bool {
	return strings.TrimSpace(key.SourceDataset) != "" && strings.TrimSpace(key.InputContractVersion) == ""
}

func (c *Client) datasetPrimary() datasetPrimaryReader {
	if c == nil {
		return nil
	}
	if c.primary != nil {
		return c.primary
	}
	reader, _ := c.access.(datasetPrimaryReader)
	return reader
}

// ReadDatasetWindow reads one subject's lookback from Primary using exact keys.
// Missing periods are returned as an incomplete frame; callers use RequireDatasetLookback.
func (c *Client) ReadDatasetWindow(
	ctx context.Context,
	key WindowKey,
	period, endTime time.Time,
	lookbackPeriods int,
	columns []string,
) (*RangeChunk, error) {
	subjectID := strings.TrimSpace(key.SubjectID)
	if subjectID == "" {
		return nil, nonRetryableRead(fmt.Errorf("dataset window subject is required"))
	}
	chunks, err := c.readPrimaryPeriodChunks(ctx, key, []string{subjectID}, period, endTime, lookbackPeriods, columns)
	if err != nil {
		return nil, err
	}
	return chunks[subjectID], nil
}

func (c *Client) readPrimaryPeriodChunks(
	ctx context.Context,
	key WindowKey,
	subjectIDs []string,
	period, endTime time.Time,
	lookbackPeriods int,
	columns []string,
) (map[string]*RangeChunk, error) {
	reader := c.datasetPrimary()
	if reader == nil {
		return nil, nonRetryableRead(fmt.Errorf("storage Primary client is unavailable"))
	}
	datasetID := strings.TrimSpace(key.SourceDataset)
	if datasetID == "" {
		return nil, nonRetryableRead(fmt.Errorf("source dataset is required"))
	}
	if period.IsZero() || endTime.IsZero() || !period.Before(endTime) {
		return nil, nonRetryableRead(fmt.Errorf("valid dataset window period and end_time are required"))
	}
	if lookbackPeriods < 1 {
		lookbackPeriods = 1
	}
	columns = compactUniqueStrings(columns)
	if len(columns) == 0 {
		return nil, nonRetryableRead(fmt.Errorf("dataset window column_names are required"))
	}
	times, err := lookbackPeriodTimes(period.UTC(), key.Freq, lookbackPeriods)
	if err != nil {
		return nil, nonRetryableRead(err)
	}
	ids := uniqueSortedStrings(subjectIDs)
	if len(ids) == 0 {
		return map[string]*RangeChunk{}, nil
	}
	batchSize := primarySubjectBatchSize(lookbackPeriods, len(columns))
	out := make(map[string]*RangeChunk, len(ids))
	for start := 0; start < len(ids); start += batchSize {
		part := ids[start:min(start+batchSize, len(ids))]
		keys := make([]*storagepb.TimeSeriesKey, 0, len(part)*len(times))
		for _, subjectID := range part {
			for _, at := range times {
				keys = append(keys, &storagepb.TimeSeriesKey{
					SpaceId: key.SpaceID, DatasetId: datasetID, SubjectId: subjectID,
					Freq: key.Freq, DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: datasetWindowSeriesTag(key),
				})
			}
		}
		rows, readErr := c.readPrimaryExactKeys(ctx, reader, key.SpaceID, datasetID, columns, keys)
		if readErr != nil {
			return nil, readErr
		}
		bySubject := make(map[string][]*storagepb.TimeSeriesRow, len(part))
		for _, row := range rows {
			dataTime, parseErr := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
			if parseErr != nil {
				return nil, nonRetryableRead(fmt.Errorf("parse data_time %q: %w", row.GetKey().GetDataTime(), parseErr))
			}
			at := dataTime.UTC()
			if !at.Before(endTime) || at.After(period) {
				return nil, nonRetryableRead(fmt.Errorf("primary window contains future row data_time=%s period=%s", at.Format(time.RFC3339Nano), period.UTC().Format(time.RFC3339Nano)))
			}
			subjectID := strings.TrimSpace(row.GetKey().GetSubjectId())
			bySubject[subjectID] = append(bySubject[subjectID], row)
		}
		for _, subjectID := range part {
			chunk, chunkErr := rangeChunkFromRows(bySubject[subjectID], columns, period, endTime, false, time.Time{})
			if chunkErr != nil {
				return nil, chunkErr
			}
			if chunk.Frame != nil && len(chunk.Frame.DataTimes) > 0 {
				chunk.IndexedTo = chunk.Frame.DataTimes[len(chunk.Frame.DataTimes)-1]
			}
			chunk.Complete = RequireDatasetLookback(chunk, lookbackPeriods) == nil
			out[subjectID] = chunk
		}
	}
	return out, nil
}

func (c *Client) readPrimaryExactKeys(
	ctx context.Context,
	reader datasetPrimaryReader,
	spaceID, datasetID string,
	columns []string,
	keys []*storagepb.TimeSeriesKey,
) ([]*storagepb.TimeSeriesRow, error) {
	rows := make([]*storagepb.TimeSeriesRow, 0, len(keys))
	for start := 0; start < len(keys); start += primaryExactReadKeyLimit {
		part := keys[start:min(start+primaryExactReadKeyLimit, len(keys))]
		rsp, err := reader.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			AuthInfo: c.auth, SpaceId: spaceID, DatasetId: datasetID,
			Keys: part, ColumnNames: append([]string(nil), columns...),
		})
		if err != nil {
			return nil, fmt.Errorf("read primary time-series rows: %w", err)
		}
		if retErr := classifyViewReadRet("read primary time-series rows", rsp.GetRetInfo()); retErr != nil {
			return nil, retErr
		}
		rows = append(rows, rsp.GetRows()...)
	}
	return rows, nil
}

func lookbackPeriodTimes(latest time.Time, freq string, lookback int) ([]time.Time, error) {
	if lookback < 1 {
		lookback = 1
	}
	out := make([]time.Time, 0, lookback)
	cursor := latest.UTC()
	for i := 0; i < lookback; i++ {
		out = append(out, cursor)
		if i+1 == lookback {
			break
		}
		prev, err := domain.PrevPeriod(cursor, freq)
		if err != nil {
			return nil, err
		}
		cursor = prev
	}
	return out, nil
}

func datasetWindowSeriesTag(key WindowKey) string {
	if key.FilterSourceSeriesTag || strings.TrimSpace(key.SourceSeriesTag) != "" {
		return key.SourceSeriesTag
	}
	return ""
}

func compactUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func primarySubjectBatchSize(lookback, columns int) int {
	limit := storagepb.SeriesWindowSubjectLimit(lookback, columns)
	if limit < 1 {
		limit = 1
	}
	byKeys := primaryExactReadKeyLimit
	if lookback > 0 {
		byKeys = max(1, primaryExactReadKeyLimit/lookback)
	}
	return min(limit, byKeys)
}

// RequireDatasetLookback fails when distinct period times are fewer than lookback.
func RequireDatasetLookback(chunk *RangeChunk, lookback int) error {
	if lookback < 1 {
		lookback = 1
	}
	if chunk == nil || chunk.Frame == nil {
		return ErrInsufficientHistory
	}
	seen := make(map[int64]struct{}, len(chunk.Frame.DataTimes))
	for _, at := range chunk.Frame.DataTimes {
		seen[at.UTC().UnixNano()] = struct{}{}
	}
	if len(seen) < lookback {
		return ErrInsufficientHistory
	}
	return nil
}

// ValidateDatasetOutputs rejects out-of-universe and duplicate result keys.
func ValidateDatasetOutputs(task *engine.FactorTask, result *engine.FactorResult) error {
	if task == nil || result == nil {
		return fmt.Errorf("task and result are required")
	}
	seen := make(map[string]struct{}, len(result.Rows))
	available := uniqueSortedStrings(task.AvailableSubjects)
	allow := make(map[string]struct{}, len(available))
	for _, subject := range available {
		allow[subject] = struct{}{}
	}
	for _, row := range result.Rows {
		if len(allow) > 0 {
			candidate := strings.TrimSpace(row.SubjectID)
			if candidate == "" {
				candidate = strings.TrimSpace(task.SubjectID)
			}
			if _, ok := allow[candidate]; !ok {
				return fmt.Errorf("result subject %q is outside the available universe", candidate)
			}
		}
		subject, err := engine.ResultSubject(task, row)
		if err != nil {
			return err
		}
		key := subject + "\x00" + row.DataTime.UTC().Format(time.RFC3339Nano) + "\x00" + row.SeriesTag
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate factor output key subject=%q data_time=%s series_tag=%q",
				subject, row.DataTime.UTC().Format(time.RFC3339Nano), row.SeriesTag)
		}
		seen[key] = struct{}{}
	}
	return nil
}
