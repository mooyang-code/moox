package marketfetch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type instrumentFetcherFake struct {
	descriptor marketdata.ProviderDescriptor
	snapshot   marketdata.InstrumentSnapshot
}

func (f instrumentFetcherFake) Descriptor() marketdata.ProviderDescriptor {
	return f.descriptor
}

func (f instrumentFetcherFake) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{MarketID: "crypto", InstrumentType: "spot", SupportsFull: true, HasStatus: true}
}

func (f instrumentFetcherFake) FetchInstruments(context.Context, marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	return f.snapshot, nil
}

type instrumentStorageFake struct {
	rows           []*storagepb.RowFieldUpsert
	sourceEventID  string
	sourceEventIDs []string
	registrations  []*storagepb.RegisterDataSubjectReq
	memberships    []*storagepb.DatasetSubject
	bound          []*storagepb.DatasetSubject
}

func (s *instrumentStorageFake) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *instrumentStorageFake) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, sourceEventID string) error {
	s.sourceEventID = sourceEventID
	s.sourceEventIDs = append(s.sourceEventIDs, sourceEventID)
	return s.UpsertFields(context.Background(), rows)
}

func (s *instrumentStorageFake) RegisterDataSubject(_ context.Context, registration *storagepb.RegisterDataSubjectReq) error {
	s.registrations = append(s.registrations, registration)
	return nil
}

func (s *instrumentStorageFake) ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error) {
	return s.memberships, nil
}

func (s *instrumentStorageFake) BindDatasetSubject(_ context.Context, membership *storagepb.DatasetSubject) error {
	s.bound = append(s.bound, membership)
	return nil
}

func testInstrumentFetcher() instrumentFetcherFake {
	descriptor := marketdata.ProviderDescriptor{
		ProviderID: "binance", SourceID: "spot_http", ProtocolVariant: marketdata.ProtocolHTTP,
		Transport: "https", Port: 443, Markets: []string{"crypto"}, InstrumentTypes: []string{"spot"}, Frequencies: []string{"1m"},
	}
	return instrumentFetcherFake{descriptor: descriptor, snapshot: marketdata.InstrumentSnapshot{
		MarketID: "crypto", InstrumentType: "spot", Version: time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Items: []marketdata.Instrument{
			{SubjectID: "BTC-USDT-SPOT", ProviderSymbol: "BTCUSDT", Name: "BTC-USDT", ExchangeID: "binance", Status: "active", InstrumentType: "spot"},
			{SubjectID: "ETH-USDT-SPOT", ProviderSymbol: "ETHUSDT", Name: "ETH-USDT", ExchangeID: "binance", Status: "active", InstrumentType: "spot"},
			{SubjectID: "SOL-USDT-SPOT", ProviderSymbol: "SOLUSDT", Name: "SOL-USDT", ExchangeID: "binance", Status: "active", InstrumentType: "spot"},
		},
	}}
}

func testInstrumentPipelineRequest() InstrumentPipelineRequest {
	fetcher := testInstrumentFetcher()
	return InstrumentPipelineRequest{
		SpaceID: "crypto", DatasetID: "binance_spot_symbols", SourceEventID: "snapshot-event",
		SourceKey: fetcher.descriptor.Key(), Request: marketdata.InstrumentRequest{MarketID: "crypto", InstrumentType: "spot"},
		WriteSpec: InstrumentWriteSpec{DataSourceID: "binance", SubjectType: "crypto_pair", SubjectMarket: "spot", SeriesTag: "venue:binance"},
	}
}

func TestInstrumentPipelineWritesCompleteSnapshotAndSourceIdentity(t *testing.T) {
	fetcher := testInstrumentFetcher()
	storage := &instrumentStorageFake{}
	request := testInstrumentPipelineRequest()
	request.ShardIndex = 0
	request.ShardCount = 1
	result, err := (&InstrumentPipeline{Fetcher: fetcher, Storage: storage}).FetchAndWrite(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Instruments != 3 || result.RowsWritten != 3 || len(storage.rows) != 3 || len(storage.registrations) != 3 {
		t.Fatalf("unexpected snapshot result=%+v rows=%d registrations=%d", result, len(storage.rows), len(storage.registrations))
	}
	if storage.sourceEventID != "snapshot-event" {
		t.Fatalf("source event id=%q", storage.sourceEventID)
	}
	if got := storage.rows[0].GetAttributes()["provider_symbol"].GetStringValue(); got != "BTCUSDT" {
		t.Fatalf("first snapshot row provider_symbol=%q", got)
	}
	if got := storage.registrations[0].GetDatasetBindings()[0].GetStatus(); got != "pending" {
		t.Fatalf("registration status=%q, want pending before activation", got)
	}
	if len(storage.bound) != 3 || storage.bound[0].GetStatus() != "active" {
		t.Fatalf("activation bindings=%v", storage.bound)
	}
}

func TestInstrumentPipelineReconcilesStaleMembershipAfterCompleteSnapshot(t *testing.T) {
	fetcher := testInstrumentFetcher()
	storage := &instrumentStorageFake{memberships: []*storagepb.DatasetSubject{{
		SpaceId: "crypto", DatasetId: "binance_spot_symbols", SubjectId: "OLD-USDT-SPOT", SubjectRole: "normal", Status: "active",
		Attributes: map[string]string{"provider_symbol": "OLDUSDT"},
	}}}
	request := testInstrumentPipelineRequest()
	request.ShardIndex = 0
	request.ShardCount = 1
	result, err := (&InstrumentPipeline{Fetcher: fetcher, Storage: storage}).FetchAndWrite(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Instruments != 3 || len(storage.bound) != 4 || storage.bound[3].GetStatus() != "disabled" {
		t.Fatalf("unexpected stale reconciliation result=%+v bound=%v", result, storage.bound)
	}
	if len(storage.rows) != 4 || storage.rows[3].GetFields()[0].GetValue().GetStringValue() != "OLD-USDT-SPOT" {
		t.Fatalf("stale row was not written: rows=%d", len(storage.rows))
	}
	if len(storage.sourceEventIDs) != 2 || storage.sourceEventIDs[0] != "snapshot-event" || storage.sourceEventIDs[1] != "snapshot-event:stale" {
		t.Fatalf("stale rows reused fresh source event: %v", storage.sourceEventIDs)
	}
}

func TestInstrumentPipelineRejectsSnapshotIdentityMismatch(t *testing.T) {
	fetcher := testInstrumentFetcher()
	fetcher.snapshot.MarketID = "stock_cn"
	request := testInstrumentPipelineRequest()
	_, err := (&InstrumentPipeline{Fetcher: fetcher, Storage: &instrumentStorageFake{}}).FetchAndWrite(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "does not match request") {
		t.Fatalf("identity mismatch error=%v", err)
	}
}

func TestInstrumentPipelineRejectsMultiInvocationSnapshot(t *testing.T) {
	request := testInstrumentPipelineRequest()
	request.ShardIndex = 0
	request.ShardCount = 2
	_, err := (&InstrumentPipeline{Fetcher: testInstrumentFetcher(), Storage: &instrumentStorageFake{}}).FetchAndWrite(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "one complete invocation") {
		t.Fatalf("multi-invocation snapshot error=%v", err)
	}
}
