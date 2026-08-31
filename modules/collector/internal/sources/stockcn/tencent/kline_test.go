package tencent

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type fakeRawGetter struct {
	payloads map[int]string
	queries  []url.Values
}

func (f *fakeRawGetter) GetStream(_ context.Context, _ string, _ string, query url.Values, consume func(io.Reader) error) error {
	f.queries = append(f.queries, query)
	var year int
	if _, err := fmt.Sscanf(query.Get("_var"), "kline_day%d", &year); err != nil {
		return err
	}
	return consume(strings.NewReader(f.payloads[year]))
}

func TestNormalizeSymbol(t *testing.T) {
	for raw, want := range map[string]string{"SH.600000": "sh600000", "sz000001": "sz000001", "300750": "sz300750", "830001": "bj830001"} {
		got, err := NormalizeSymbol(raw)
		if err != nil || got != want {
			t.Fatalf("NormalizeSymbol(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := NormalizeSymbol("123456"); err == nil {
		t.Fatal("unknown bare code should be rejected")
	}
}

func TestFetchKlinesParsesTencentJSONPAndYearSlices(t *testing.T) {
	getter := &fakeRawGetter{payloads: map[int]string{
		2025: `v_kline_day2025={"data":{"sz000001":{"day":[["2025-12-31","10","10.5","11","9.5","100","0","1.5","123.45"]]}}};`,
		2026: `v_kline_day2026={"data":{"sz000001":{"day":[["2026-01-02","11","11.5","12","10.5","200","0","1.5","234.56"]]}}};`,
	}}
	client := NewClient(getter)
	client.Now = func() time.Time { return time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC) }
	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_cn", InstrumentType: "equity", SubjectID: "SZ.000001", ProviderSymbol: "SZ.000001", Frequency: "1d",
		StartTime: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 {
		t.Fatalf("got %d bars, want 2: %+v", len(bars), bars)
	}
	if bars[0].Open.String() != "10" || bars[0].High.String() != "11" || bars[0].Volume.String() != "10000" || bars[0].Amount.Value.String() != "1234500" {
		t.Fatalf("unexpected first bar: %+v", bars[0])
	}
	if len(getter.queries) != 2 || getter.queries[0].Get("param") != "sz000001,day,2025-01-01,2026-12-31,640," {
		t.Fatalf("unexpected queries: %+v", getter.queries)
	}
}

func TestDecodeRowsRejectsMissingSymbol(t *testing.T) {
	_, err := decodeRows([]byte(`x={"data":{}};`), "sz000001")
	if err == nil || !strings.Contains(err.Error(), "no data") {
		t.Fatalf("decodeRows() error = %v", err)
	}
}
