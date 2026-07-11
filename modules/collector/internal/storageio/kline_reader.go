package storageio

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Candidates is the Pipeline-facing exact source read. It never performs a
// dataset scan: every enabled provider dataset contributes one exact key.
func (c *Client) Candidates(ctx context.Context, spaceID string, datasetIDs []string, subjectID string, frequency marketdata.Frequency, dataTime time.Time) ([]marketdata.ProviderKline, error) {
	rows, err := c.ReadCandidates(ctx, spaceID, datasetIDs, subjectID, frequency, dataTime)
	if err != nil {
		return nil, err
	}
	result := make([]marketdata.ProviderKline, 0, len(rows))
	for _, row := range rows {
		candidate, err := timeSeriesRowToKline(row)
		if err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (c *Client) Unified(ctx context.Context, spaceID, datasetID, subjectID string, frequency marketdata.Frequency, dataTime time.Time) (*marketdata.ResolvedKline, error) {
	binding, err := c.binding(datasetID, RoleUnifiedData)
	if err != nil {
		return nil, err
	}
	if binding.SpaceID != spaceID {
		return nil, fmt.Errorf("dataset %q belongs to space %q, not %q", datasetID, binding.SpaceID, spaceID)
	}
	timestamp := dataTime.UTC().Format(time.RFC3339)
	rsp, err := c.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{AuthInfo: c.auth, Keys: []*storagepb.TimeSeriesKey{{SpaceId: binding.SpaceID, DatasetId: binding.DatasetID, SubjectId: subjectID, Freq: string(frequency), DataTime: timestamp}}, TimeRange: &storagepb.TimeRange{StartTime: timestamp, EndTime: timestamp}})
	if err != nil {
		return nil, err
	}
	if err := ensureOK("read unified kline", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	value, err := timeSeriesRowToKline(rsp.GetRows()[0])
	if err != nil {
		return nil, err
	}
	columns := rowColumns(rsp.GetRows()[0])
	revision := int64(0)
	if column := columns["revision"]; column != nil {
		revision = column.GetValue().GetIntValue()
	}
	resolvedAt, _ := time.Parse(time.RFC3339, columnString(columns["resolved_at"]))
	return &marketdata.ResolvedKline{ProviderKline: value, SourceDatasetID: columnString(columns["source_dataset_id"]), QualityStatus: columnString(columns["quality_status"]), Revision: revision, ResolvedAt: resolvedAt}, nil
}

func timeSeriesRowToKline(row *storagepb.TimeSeriesRow) (marketdata.ProviderKline, error) {
	if row == nil || row.GetKey() == nil {
		return marketdata.ProviderKline{}, fmt.Errorf("storage source row is empty")
	}
	columns := rowColumns(row)
	decimalValue := func(name string) (marketdata.Decimal, error) {
		return marketdata.ParseDecimal(columnString(columns[name+"_exact"]))
	}
	open, err := decimalValue("open")
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	high, err := decimalValue("high")
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	low, err := decimalValue("low")
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	closeValue, err := decimalValue("close")
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	dataTime, err := time.Parse(time.RFC3339, row.GetKey().GetDataTime())
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	closeTime, err := time.Parse(time.RFC3339, columnString(columns["close_time"]))
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	providerTimestamp, err := time.Parse(time.RFC3339, columnString(columns["provider_timestamp"]))
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	fetchedAt, err := time.Parse(time.RFC3339, columnString(columns["fetched_at"]))
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	frequency, err := marketdata.ParseFrequency(row.GetKey().GetFreq())
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	result := marketdata.ProviderKline{SubjectID: row.GetKey().GetSubjectId(), ProviderID: marketdata.ProviderID(row.GetAttributes()["provider_id"]), ProviderSymbol: row.GetAttributes()["provider_symbol"], Frequency: frequency, DataTime: dataTime, CloseTime: closeTime, TradeDate: columnString(columns["trade_date"]), FeedScope: columnString(columns["feed_scope"]), VolumeUnit: columnString(columns["volume_unit"]), AmountUnit: columnString(columns["amount_unit"]), Open: open, High: high, Low: low, Close: closeValue, ProviderTimestamp: providerTimestamp, FetchedAt: fetchedAt, RequestID: columnString(columns["request_id"]), Closed: columnBool(columns["is_closed"])}
	if columns["volume_exact"] != nil {
		v, err := decimalValue("volume")
		if err != nil {
			return result, err
		}
		result.Volume = &v
	}
	if columns["amount_exact"] != nil {
		v, err := decimalValue("amount")
		if err != nil {
			return result, err
		}
		result.Amount = &v
	}
	return result, nil
}
func rowColumns(row *storagepb.TimeSeriesRow) map[string]*storagepb.ColumnValue {
	out := make(map[string]*storagepb.ColumnValue, len(row.GetColumns()))
	for _, column := range row.GetColumns() {
		out[column.GetColumnName()] = column
	}
	return out
}
func columnString(column *storagepb.ColumnValue) string {
	if column == nil || column.GetValue() == nil {
		return ""
	}
	if value := column.GetValue().GetStringValue(); value != "" {
		return value
	}
	return column.GetValue().GetTimeValue()
}
func columnBool(column *storagepb.ColumnValue) bool {
	if column == nil || column.GetValue() == nil {
		return false
	}
	return column.GetValue().GetBoolValue()
}
