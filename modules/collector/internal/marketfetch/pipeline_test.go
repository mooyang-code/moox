package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type pipelineFetcher struct {
	descriptor marketdata.ProviderDescriptor
	spec       marketdata.KlineSpec
	bars       []marketdata.NormalizedKline
}

func (f pipelineFetcher) Descriptor() marketdata.ProviderDescriptor { return f.descriptor }
func (f pipelineFetcher) KlineSpec() marketdata.KlineSpec           { return f.spec }
func (f pipelineFetcher) FetchKlines(context.Context, marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	return f.bars, nil
}

type pipelineWriter struct {
	rows   []*storagepb.RowFieldUpsert
	source string
}

func (w *pipelineWriter) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	w.rows = rows
	return nil
}

func (w *pipelineWriter) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	w.rows, w.source = rows, source
	return nil
}

func TestKlinePipelineWritesCanonicalRowsAndExplicitNull(t *testing.T) {
	fetcher := pipelineFetcher{
		descriptor: marketdata.ProviderDescriptor{ProviderID: "eastmoney", SourceID: "a_http", ProtocolVariant: marketdata.ProtocolHTTP, Transport: "http", Markets: []string{"stock_cn"}, InstrumentTypes: []string{"equity"}},
		spec:       marketdata.KlineSpec{MarketID: "stock_cn", InstrumentType: "equity", Frequencies: []string{"1d"}, CompleteOHLCV: true, VolumeUnit: "share", MaxBarsPerRequest: 1000, RequestTimeoutSeconds: 10},
		bars: []marketdata.NormalizedKline{{
			SubjectID: "600000", ProviderID: "eastmoney", SourceID: "a_http", ProviderSymbol: "SH.600000", Frequency: "1d",
			BarStart: time.Date(2026, 8, 31, 1, 30, 0, 0, time.UTC), BarEnd: time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC),
			Open: common.NewDecimal("10"), High: common.NewDecimal("11"), Low: common.NewDecimal("9"), Close: common.NewDecimal("10.5"), Volume: common.NewDecimal("100"),
			Amount: marketdata.OptionalDecimal{Valid: true, Null: true}, VolumeUnit: "share", FetchedAt: time.Now(),
		}},
	}
	writer := &pipelineWriter{}
	pipeline := KlinePipeline{Fetcher: fetcher, Writer: writer}
	result, err := pipeline.FetchAndWrite(context.Background(), PipelineRequest{SpaceID: "stock_cn", DatasetID: "stock_cn_kline", SeriesTag: "primary", SourceEventID: "batch-1", Request: marketdata.KlineRequest{MarketID: "stock_cn", InstrumentType: "equity", SubjectID: "600000", ProviderSymbol: "SH.600000", Frequency: "1d", Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsWritten != 1 || len(writer.rows) != 1 || writer.source != "batch-1" {
		t.Fatalf("unexpected pipeline result: %+v rows=%d source=%q", result, len(writer.rows), writer.source)
	}
	if got := writer.rows[0].GetKey().GetTimeSeries().GetDataTime(); got != "2026-08-31T01:30:00Z" {
		t.Fatalf("data_time = %q", got)
	}
	fields := writer.rows[0].GetFields()
	if fields[len(fields)-1].GetValue().GetNullValue() != storagepb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("amount was not explicit null: %+v", fields)
	}
}

func TestNormalizeKlineRowsRejectsDuplicateLogicalRows(t *testing.T) {
	bar := marketdata.NormalizedKline{SubjectID: "600000", ProviderID: "p", SourceID: "s", ProviderSymbol: "600000", Frequency: "1d", BarStart: time.Unix(10, 0), BarEnd: time.Unix(20, 0), Open: common.NewDecimal("1"), High: common.NewDecimal("1"), Low: common.NewDecimal("1"), Close: common.NewDecimal("1"), Volume: common.NewDecimal("1"), VolumeUnit: "share"}
	_, _, err := NormalizeKlineRows("space", "dataset", "", "event", []marketdata.NormalizedKline{bar, bar})
	if err == nil {
		t.Fatal("expected duplicate logical row error")
	}
}
