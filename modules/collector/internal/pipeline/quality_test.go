package pipeline

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

func TestResolveKlineChoosesOneWholeProviderRowAndRevisionsOnlyOnBusinessChange(t *testing.T) {
	primary := pipelineKline("primary", "10")
	fallback := pipelineKline("fallback", "11")
	resolver := QualityResolver{Policy: QualityPolicy{ProviderPriority: []marketdata.ProviderID{"primary", "fallback"}, AuthoritativeSingleSource: true}}
	decision, err := resolver.Resolve([]marketdata.ProviderKline{fallback, primary}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Row == nil || decision.Row.ProviderID != "primary" || decision.Row.Close.String() != "10" {
		t.Fatalf("wrong whole-row winner: %+v", decision.Row)
	}
	if decision.Row.Revision != 1 || decision.Row.QualityStatus != "confirmed" {
		t.Fatalf("wrong initial decision: %+v", decision.Row)
	}
	existing := *decision.Row
	unchanged, err := resolver.Resolve([]marketdata.ProviderKline{primary}, &existing)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Row.Revision != existing.Revision {
		t.Fatalf("retry changed revision: %d", unchanged.Row.Revision)
	}
	changed := pipelineKline("primary", "12")
	revised, err := resolver.Resolve([]marketdata.ProviderKline{changed}, &existing)
	if err != nil {
		t.Fatal(err)
	}
	if revised.Row.Revision != 2 {
		t.Fatalf("business change revision=%d", revised.Row.Revision)
	}
}

func TestResolveKlineRejectsInvalidRowsAndCanRemainProvisional(t *testing.T) {
	row := pipelineKline("fallback", "10")
	resolver := QualityResolver{Policy: QualityPolicy{ProviderPriority: []marketdata.ProviderID{"primary", "fallback"}, AuthoritativeSingleSource: false}}
	decision, err := resolver.Resolve([]marketdata.ProviderKline{row}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Row.QualityStatus != "provisional" {
		t.Fatalf("status=%s", decision.Row.QualityStatus)
	}
	bad := row
	bad.High = marketdata.MustDecimal("1")
	if _, err := resolver.Resolve([]marketdata.ProviderKline{bad}, nil); err == nil {
		t.Fatal("invalid ohlc accepted")
	}
}

func pipelineKline(provider, close string) marketdata.ProviderKline {
	value := marketdata.MustDecimal(close)
	high := marketdata.MustDecimal("13")
	now := time.Date(2026, 7, 11, 0, 1, 0, 0, time.UTC)
	return marketdata.ProviderKline{SubjectID: "BTC-USDT", ProviderID: marketdata.ProviderID(provider), ProviderSymbol: "BTCUSDT", Frequency: marketdata.FrequencyMinute, DataTime: now.Add(-time.Minute), CloseTime: now, TradeDate: "2026-07-11", FeedScope: "spot", VolumeUnit: "base", AmountUnit: "quote", Open: marketdata.MustDecimal("10"), High: high, Low: marketdata.MustDecimal("9"), Close: value, Volume: pipelineDecimal(marketdata.MustDecimal("1")), Amount: pipelineDecimal(marketdata.MustDecimal("10")), ProviderTimestamp: now, FetchedAt: now, RequestID: provider, Closed: true}
}
func pipelineDecimal(v marketdata.Decimal) *marketdata.Decimal { return &v }
