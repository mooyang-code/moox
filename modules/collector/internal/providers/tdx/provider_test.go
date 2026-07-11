package tdx

import (
	"context"
	"testing"
	"time"

	"gitee.com/quant1x/exchange"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

type fakeDialer struct {
	client *fakeClient
	calls  int
}

func (d *fakeDialer) Dial(context.Context, string) (Client, error) { d.calls++; return d.client, nil }

type fakeClient struct {
	klines     []RawKline
	securities []RawSecurity
	count      uint16
	closed     bool
}

func (c *fakeClient) Klines(_ string, _ uint16, _ uint16, count uint16, _ bool) ([]RawKline, error) {
	c.count = count
	return c.klines, nil
}
func (c *fakeClient) Securities(_ uint8, _ uint16) ([]RawSecurity, error) { return c.securities, nil }
func (c *fakeClient) Close()                                              { c.closed = true }

func TestFetchKlinesNormalizesDailyAndCapsCount(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	client := &fakeClient{klines: []RawKline{{Time: time.Date(2026, 7, 11, 0, 0, 0, 0, location), Open: 10.1, High: 10.5, Low: 10, Close: 10.2, Volume: 100, Amount: 1020}}}
	dialer := &fakeDialer{client: client}
	p := New(Config{Address: "127.0.0.1:7709", Dialer: dialer, Now: func() time.Time { return now }})
	result, err := p.FetchKlines(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchKlinesRequest{ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, Frequency: marketdata.FrequencyDay, Subjects: []providers.ProviderSubject{{SubjectID: "600000.XSHG", ProviderSymbol: "600000"}}, Limit: 900})
	if err != nil {
		t.Fatal(err)
	}
	if client.count != 800 || dialer.calls != 1 || !client.closed || len(result.Rows) != 1 {
		t.Fatalf("result=%+v client=%+v dial=%d", result, client, dialer.calls)
	}
	row := result.Rows[0]
	if row.TradeDate != "2026-07-11" || row.Close.String() != "10.2" || row.DataTime.In(location).Hour() != 0 || row.CloseTime.In(location).Hour() != 15 || !row.Closed {
		t.Fatalf("row=%+v", row)
	}
}

func TestFetchKlinesDoesNotDialWhenGateDenies(t *testing.T) {
	dialer := &fakeDialer{client: &fakeClient{}}
	p := New(Config{Address: "127.0.0.1:7709", Dialer: dialer})
	_, err := p.FetchKlines(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: false, DenialReason: "quota"}}, providers.FetchKlinesRequest{Frequency: marketdata.FrequencyDay, Subjects: []providers.ProviderSubject{{SubjectID: "600000.XSHG", ProviderSymbol: "600000"}}})
	if err == nil || dialer.calls != 0 {
		t.Fatalf("err=%v dial=%d", err, dialer.calls)
	}
}

func TestFetchInstrumentsClassifiesExchangeScopedCodes(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	client := &fakeClient{securities: []RawSecurity{{Market: exchange.MarketIdShangHai, Code: "000001", Name: "上证指数"}, {Market: exchange.MarketIdShangHai, Code: "510300", Name: "沪深300ETF"}, {Market: exchange.MarketIdShangHai, Code: "600000", Name: "浦发银行"}}}
	p := New(Config{Address: "127.0.0.1:7709", Dialer: &fakeDialer{client: client}, Now: func() time.Time { return now }})
	result, err := p.FetchInstruments(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchInstrumentsRequest{SnapshotAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instruments) != 3 || result.Instruments[0].SubjectID != "000001.XSHG" || result.Instruments[0].ExchangeID != "XSHG" || result.Instruments[0].InstrumentType != marketdata.InstrumentIndex || result.Instruments[1].InstrumentType != marketdata.InstrumentETF || result.Instruments[2].InstrumentType != marketdata.InstrumentEquity {
		t.Fatalf("result=%+v", result)
	}
	if result.Complete || result.NextCursor != "1:0" {
		t.Fatalf("cursor=%q complete=%v", result.NextCursor, result.Complete)
	}
}

func TestMissingAddressFailsClosed(t *testing.T) {
	t.Setenv("MOOX_TDX_ADDRESS", "")
	_, err := New(Config{}).FetchKlines(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchKlinesRequest{Frequency: marketdata.FrequencyDay})
	if err == nil {
		t.Fatal("missing TDX address accepted")
	}
}
