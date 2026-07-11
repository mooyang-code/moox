package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestInstrumentMetadataRegistrarBindsLogicalDatasetsAndProviderSymbol(t *testing.T) {
	metadata := &metadataSubjectFake{}
	registrar := NewInstrumentMetadataRegistrar(metadata, nil, "crypto_binance", "binance", "UTC", []string{"instruments", "spot_kline"})
	value := providers.ResolvedInstrument{ProviderInstrument: providers.ProviderInstrument{SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", ExchangeID: "BINANCE", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Name: "BTC/USDT", Currency: "USDT", Status: "TRADING"}, Generation: time.Now()}
	if err := registrar.RegisterInstruments(context.Background(), []providers.ResolvedInstrument{value}); err != nil {
		t.Fatal(err)
	}
	item := metadata.req.GetItems()[0]
	if item.GetSubject().GetSubjectId() != "BTC-USDT" || item.GetExternalSymbol() != "BTCUSDT" || len(item.GetDatasetBindings()) != 2 || item.GetSubject().GetStatus() != "active" {
		t.Fatalf("item=%+v", item)
	}
}

type metadataSubjectFake struct {
	req *storagepb.BatchRegisterDataSubjectsReq
}

func (f *metadataSubjectFake) BatchRegisterDataSubjects(_ context.Context, req *storagepb.BatchRegisterDataSubjectsReq, _ ...client.Option) (*storagepb.BatchRegisterDataSubjectsRsp, error) {
	f.req = req
	return &storagepb.BatchRegisterDataSubjectsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}
