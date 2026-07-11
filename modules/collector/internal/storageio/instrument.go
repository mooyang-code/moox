package storageio

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (c *Client) WriteProviderInstruments(ctx context.Context, datasetID string, generation time.Time, values []providers.ProviderInstrument) error {
	binding, err := c.binding(datasetID, RoleProviderData)
	if err != nil {
		return err
	}
	if binding.Feed != "instrument" {
		return fmt.Errorf("dataset %q is not an instrument dataset", datasetID)
	}
	rows := make([]*storagepb.RecordRow, 0, len(values))
	for _, value := range values {
		row, err := instrumentRecord(binding, generation, value, false, "", "")
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	rsp, err := c.access.WriteRecordRows(ctx, &storagepb.WriteRecordRowsReq{AuthInfo: c.auth, Rows: rows, WriteMode: storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE})
	if err != nil {
		return err
	}
	return ensureOK("write provider instruments", rsp.GetRetInfo())
}

func (c *Client) WriteUnifiedInstrument(ctx context.Context, datasetID string, value providers.ResolvedInstrument) error {
	binding, err := c.binding(datasetID, RoleUnifiedData)
	if err != nil {
		return err
	}
	if binding.Feed != "instrument" {
		return fmt.Errorf("dataset %q is not an instrument dataset", datasetID)
	}
	row, err := instrumentRecord(binding, value.Generation, value.ProviderInstrument, true, value.SourceDatasetID, value.QualityStatus)
	if err != nil {
		return err
	}
	row.Columns = append(row.Columns, timeColumn("resolved_at", value.ResolvedAt))
	rsp, err := c.access.WriteRecordRows(ctx, &storagepb.WriteRecordRowsReq{AuthInfo: c.auth, Rows: []*storagepb.RecordRow{row}, WriteMode: storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE})
	if err != nil {
		return err
	}
	return ensureOK("write unified instrument", rsp.GetRetInfo())
}

func (c *Client) InstrumentCandidates(ctx context.Context, spaceID string, datasetIDs []string, subjectID string, generation time.Time) ([]providers.ProviderInstrument, error) {
	keys := make([]*storagepb.RecordKey, 0, len(datasetIDs))
	for _, datasetID := range datasetIDs {
		binding, err := c.binding(datasetID, RoleProviderData)
		if err != nil {
			return nil, err
		}
		if binding.SpaceID != spaceID {
			return nil, fmt.Errorf("dataset %q belongs to another space", datasetID)
		}
		keys = append(keys, &storagepb.RecordKey{SpaceId: spaceID, DatasetId: datasetID, RecordId: subjectID, Version: generation.UTC().Format(time.RFC3339Nano)})
	}
	rsp, err := c.access.ReadRecordRows(ctx, &storagepb.ReadRecordRowsReq{AuthInfo: c.auth, Keys: keys})
	if err != nil {
		return nil, err
	}
	if err := ensureOK("read instrument candidates", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	result := make([]providers.ProviderInstrument, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		value, err := recordToInstrument(row)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func instrumentRecord(binding Binding, generation time.Time, value providers.ProviderInstrument, unified bool, sourceDatasetID, quality string) (*storagepb.RecordRow, error) {
	if value.SubjectID == "" || value.ProviderID == "" || value.ProviderSymbol == "" || generation.IsZero() {
		return nil, fmt.Errorf("subject, provider, provider symbol and generation are required")
	}
	columns := []*storagepb.ColumnValue{stringColumn("subject_id", value.SubjectID), stringColumn("provider_id", string(value.ProviderID)), stringColumn("provider_symbol", value.ProviderSymbol), stringColumn("exchange_id", string(value.ExchangeID)), stringColumn("product_type", string(value.ProductType)), stringColumn("instrument_type", string(value.InstrumentType)), stringColumn("name", value.Name), stringColumn("currency", value.Currency), stringColumn("listing_date", value.ListingDate), stringColumn("delisting_date", value.DelistingDate), stringColumn("status", value.Status), timeColumn("effective_at", value.EffectiveAt), timeColumn("fetched_at", value.FetchedAt), stringColumn("request_id", value.RequestID)}
	if unified {
		columns = append(columns, stringColumn("source_provider", string(value.ProviderID)), stringColumn("source_dataset_id", sourceDatasetID), stringColumn("quality_status", quality), timeColumn("generation", generation))
	}
	return &storagepb.RecordRow{Key: &storagepb.RecordKey{SpaceId: binding.SpaceID, DatasetId: binding.DatasetID, RecordId: value.SubjectID, Version: generation.UTC().Format(time.RFC3339Nano)}, Columns: columns, Attributes: map[string]string{"provider_id": string(value.ProviderID), "provider_symbol": value.ProviderSymbol}}, nil
}

func recordToInstrument(row *storagepb.RecordRow) (providers.ProviderInstrument, error) {
	if row == nil || row.GetKey() == nil {
		return providers.ProviderInstrument{}, fmt.Errorf("instrument row is empty")
	}
	columns := make(map[string]*storagepb.ColumnValue, len(row.GetColumns()))
	for _, column := range row.GetColumns() {
		columns[column.GetColumnName()] = column
	}
	parseTime := func(name string) time.Time {
		value, _ := time.Parse(time.RFC3339Nano, columnString(columns[name]))
		return value
	}
	return providers.ProviderInstrument{SubjectID: columnString(columns["subject_id"]), ProviderID: marketdata.ProviderID(columnString(columns["provider_id"])), ProviderSymbol: columnString(columns["provider_symbol"]), ExchangeID: marketdata.ExchangeID(columnString(columns["exchange_id"])), ProductType: marketdata.ProductType(columnString(columns["product_type"])), InstrumentType: marketdata.InstrumentType(columnString(columns["instrument_type"])), Name: columnString(columns["name"]), Currency: columnString(columns["currency"]), ListingDate: columnString(columns["listing_date"]), DelistingDate: columnString(columns["delisting_date"]), Status: columnString(columns["status"]), EffectiveAt: parseTime("effective_at"), FetchedAt: parseTime("fetched_at"), RequestID: columnString(columns["request_id"])}, nil
}
