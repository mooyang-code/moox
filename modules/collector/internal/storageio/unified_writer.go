package storageio

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (c *Client) WriteUnifiedKline(ctx context.Context, datasetID string, row marketdata.ResolvedKline) error {
	binding, err := c.binding(datasetID, RoleUnifiedData)
	if err != nil {
		return err
	}
	if binding.Feed != "kline" {
		return fmt.Errorf("dataset %q is not kline", datasetID)
	}
	if err := row.ProviderKline.Validate(); err != nil {
		return err
	}
	columns, err := klineColumns(row.ProviderKline)
	if err != nil {
		return err
	}
	columns = append(columns, stringColumn("source_provider", string(row.ProviderID)), stringColumn("source_dataset_id", row.SourceDatasetID), stringColumn("source_fetched_at", row.FetchedAt.Format(time.RFC3339)), stringColumn("quality_status", row.QualityStatus), intColumn("revision", row.Revision), stringColumn("resolved_at", row.ResolvedAt.Format(time.RFC3339)))
	rsp, err := c.access.WriteTimeSeriesRows(ctx, &storagepb.WriteTimeSeriesRowsReq{AuthInfo: c.auth, WriteMode: storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE, Rows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SpaceId: binding.SpaceID, DatasetId: binding.DatasetID, SubjectId: row.SubjectID, Freq: string(row.Frequency), DataTime: row.DataTime.Format(time.RFC3339)}, Columns: columns, Attributes: map[string]string{"provider_id": string(row.ProviderID), "provider_symbol": row.ProviderSymbol}}}})
	if err != nil {
		return err
	}
	return ensureOK("write unified kline", rsp.GetRetInfo())
}
func intColumn(name string, value int64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: value}}}
}
