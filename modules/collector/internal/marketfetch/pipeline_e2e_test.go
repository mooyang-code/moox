package marketfetch

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type e2eHTTPGetter struct {
	payload []byte
}

func (getter e2eHTTPGetter) Get(_ context.Context, _ string, _ string, _ url.Values, result interface{}) error {
	return json.Unmarshal(getter.payload, result)
}

type e2eWriter struct {
	rows   []*storagepb.RowFieldUpsert
	source string
}

func (writer *e2eWriter) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	writer.rows = rows
	return nil
}

func (writer *e2eWriter) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	writer.rows = rows
	writer.source = source
	return nil
}

func TestEastMoneyHTTPToCanonicalStoragePipeline(t *testing.T) {
	getter := e2eHTTPGetter{payload: []byte(`{"rc":0,"data":{"klines":["2026-08-31 09:30,10,10.5,11,9.5,100,1050"]}}`)}
	fetcher := markethttp.NewClient(markethttp.Config{
		ProviderID: "eastmoney", SourceID: "stock_cn_http", MarketID: "stock_cn", InstrumentType: "equity",
		SecID: func(symbol string) (string, error) { return "1." + symbol[3:], nil }, Frequencies: []string{"1m"},
		VolumeUnit: "share", AmountUnit: "cny",
	}, getter)
	writer := &e2eWriter{}
	pipeline := KlinePipeline{Fetcher: fetcher, Writer: writer}
	result, err := pipeline.FetchAndWrite(context.Background(), PipelineRequest{
		SpaceID: "stock_cn", DatasetID: "stock_cn_kline", SeriesTag: "primary", SourceEventID: "batch-e2e",
		Request: marketdata.KlineRequest{MarketID: "stock_cn", InstrumentType: "equity", SubjectID: "SH.600000", ProviderSymbol: "SH.600000", Frequency: "1m", Limit: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsWritten != 1 || len(writer.rows) != 1 || writer.source != "batch-e2e" {
		t.Fatalf("unexpected result rows=%d writer=%+v", result.RowsWritten, writer)
	}
	row := writer.rows[0]
	if got := row.GetKey().GetTimeSeries().GetDataTime(); got != "2026-08-31T01:30:00Z" {
		t.Fatalf("data_time = %q", got)
	}
	if got := row.GetAttributes()["source_id"].GetStringValue(); got != "stock_cn_http" {
		t.Fatalf("source_id = %q", got)
	}
	if got := row.GetFields()[4].GetValue().GetDoubleValue(); got != 100 {
		t.Fatalf("volume = %v", got)
	}
	if got := row.GetFields()[5].GetValue().GetDoubleValue(); got != 1050 {
		t.Fatalf("amount = %v", got)
	}
}
