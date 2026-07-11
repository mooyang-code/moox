package storageio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/coverage"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (c *Client) PresentBuckets(ctx context.Context, spaceID, datasetID, subjectID string, frequency marketdata.Frequency, start, end time.Time) ([]time.Time, error) {
	binding, err := c.binding(datasetID, RoleUnifiedData)
	if err != nil {
		return nil, err
	}
	if binding.SpaceID != spaceID || binding.Feed != "kline" {
		return nil, fmt.Errorf("dataset %q is not the requested unified kline dataset", datasetID)
	}
	rsp, err := c.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{AuthInfo: c.auth, Keys: []*storagepb.TimeSeriesKey{{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: string(frequency)}}, TimeRange: &storagepb.TimeRange{StartTime: start.UTC().Format(time.RFC3339Nano), EndTime: end.UTC().Format(time.RFC3339Nano)}, ColumnNames: []string{"close"}, Page: &storagepb.Page{Page: 1, Size: 1000}})
	if err != nil {
		return nil, err
	}
	if err := ensureOK("read unified coverage", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	result := make([]time.Time, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		value, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil {
			return nil, err
		}
		result = append(result, value.UTC())
	}
	return result, nil
}

func (c *Client) WriteCoverageState(ctx context.Context, value coverage.State) error {
	binding, err := c.binding("market_coverage", RoleCoverageState)
	if err != nil {
		return err
	}
	rawRanges, err := json.Marshal(value.MissingRanges)
	if err != nil {
		return err
	}
	keyHash := sha256.Sum256([]byte(value.DatasetID + "|" + value.SubjectID + "|" + string(value.Frequency) + "|" + value.PartitionID))
	row := &storagepb.RecordRow{Key: &storagepb.RecordKey{SpaceId: binding.SpaceID, DatasetId: binding.DatasetID, RecordId: hex.EncodeToString(keyHash[:]), Version: value.Start.UTC().Format(time.RFC3339Nano)}, Columns: []*storagepb.ColumnValue{stringColumn("unified_dataset_id", value.DatasetID), stringColumn("subject_id", value.SubjectID), stringColumn("frequency", string(value.Frequency)), stringColumn("partition_id", value.PartitionID), timeColumn("range_start", value.Start), timeColumn("range_end", value.End), intColumn("expected_count", int64(value.Expected)), intColumn("present_count", int64(value.Present)), intColumn("missing_count", int64(value.Missing)), stringColumn("missing_ranges", string(rawRanges)), stringColumn("coverage_status", value.Status), timeColumn("checked_at", value.CheckedAt)}}
	rsp, err := c.access.WriteRecordRows(ctx, &storagepb.WriteRecordRowsReq{AuthInfo: c.auth, Rows: []*storagepb.RecordRow{row}, WriteMode: storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE})
	if err != nil {
		return err
	}
	return ensureOK("write coverage state", rsp.GetRetInfo())
}
