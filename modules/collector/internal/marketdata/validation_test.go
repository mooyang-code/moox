package marketdata

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateNormalizedKline(t *testing.T) {
	start := time.Date(2026, 8, 29, 1, 2, 0, 0, time.UTC)
	valid := NormalizedKline{
		SubjectID: "600000.XSHG", ProviderID: "test", ProviderSymbol: "sh600000",
		Frequency: "1m", BarStart: start, BarEnd: start.Add(time.Minute),
		Open: 10, High: 12, Low: 9, Close: 11, VolumeShares: 100, AmountCNY: 1100,
		ProviderTimestamp: start.Add(time.Minute), FetchedAt: start.Add(2 * time.Minute), RequestID: "req-1",
	}

	require.NoError(t, ValidateNormalizedKline(valid))

	tests := []struct {
		name   string
		mutate func(*NormalizedKline)
	}{
		{name: "nan open", mutate: func(k *NormalizedKline) { k.Open = math.NaN() }},
		{name: "infinite amount", mutate: func(k *NormalizedKline) { k.AmountCNY = math.Inf(1) }},
		{name: "non positive close", mutate: func(k *NormalizedKline) { k.Close = 0 }},
		{name: "high below close", mutate: func(k *NormalizedKline) { k.High = 10.5 }},
		{name: "low above open", mutate: func(k *NormalizedKline) { k.Low = 10.5 }},
		{name: "negative volume", mutate: func(k *NormalizedKline) { k.VolumeShares = -1 }},
		{name: "unsupported frequency", mutate: func(k *NormalizedKline) { k.Frequency = "5m" }},
		{name: "wrong bar end", mutate: func(k *NormalizedKline) { k.BarEnd = k.BarStart.Add(59 * time.Second) }},
		{name: "non UTC start", mutate: func(k *NormalizedKline) {
			k.BarStart = k.BarStart.In(time.FixedZone("CST", 8*60*60))
		}},
		{name: "missing provider symbol", mutate: func(k *NormalizedKline) { k.ProviderSymbol = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valid
			tt.mutate(&got)
			assert.Error(t, ValidateNormalizedKline(got))
		})
	}
}

func TestInstrumentSnapshotValidationRequiresCompleteSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	valid := InstrumentSnapshot{
		SnapshotID: "snap-1", SourceProvider: "test", MarketID: "stock_cn", FetchedAt: now,
		Complete: true, PageCount: 1, ExchangeCounts: map[string]int{"XSHG": 1},
		Instruments: []Instrument{{
			SubjectID: "600000.XSHG", ProviderSymbol: "sh600000", Exchange: "XSHG",
			Name: "Pudong Bank", Status: "active",
		}},
	}
	require.NoError(t, ValidateInstrumentSnapshot(valid))

	tests := []struct {
		name   string
		mutate func(*InstrumentSnapshot)
	}{
		{name: "incomplete", mutate: func(s *InstrumentSnapshot) { s.Complete = false }},
		{name: "empty", mutate: func(s *InstrumentSnapshot) { s.Instruments = nil; s.ExchangeCounts = nil }},
		{name: "no pages", mutate: func(s *InstrumentSnapshot) { s.PageCount = 0 }},
		{name: "wrong exchange count", mutate: func(s *InstrumentSnapshot) { s.ExchangeCounts["XSHG"] = 2 }},
		{name: "missing symbol", mutate: func(s *InstrumentSnapshot) { s.Instruments[0].ProviderSymbol = "" }},
		{name: "duplicate subject", mutate: func(s *InstrumentSnapshot) {
			s.Instruments = append(s.Instruments, s.Instruments[0])
			s.ExchangeCounts["XSHG"] = 2
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valid
			got.ExchangeCounts = map[string]int{"XSHG": 1}
			got.Instruments = append([]Instrument(nil), valid.Instruments...)
			tt.mutate(&got)
			assert.Error(t, ValidateInstrumentSnapshot(got))
		})
	}
}
