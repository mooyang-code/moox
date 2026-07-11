package marketdata

import (
	"testing"
	"time"
)

func TestFrequencyNormalizesAliasesAndRejectsUnknown(t *testing.T) {
	f, err := ParseFrequency("1H")
	if err != nil || f != FrequencyHour {
		t.Fatalf("ParseFrequency(1H) = %q, %v", f, err)
	}
	if _, err := ParseFrequency("2h"); err == nil {
		t.Fatal("unknown frequency should be rejected")
	}
}

func TestProviderKlineValidate(t *testing.T) {
	valid := ProviderKline{
		SubjectID:      "600000",
		ProviderID:     "tdx",
		ProviderSymbol: "sh.600000",
		Frequency:      FrequencyHour,
		DataTime:       time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC),
		CloseTime:      time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC),
		TradeDate:      "2026-07-11",
		FeedScope:      "equity",
		VolumeUnit:     "share",
		AmountUnit:     "CNY",
		Open:           MustDecimal("10"), High: MustDecimal("12"), Low: MustDecimal("9"), Close: MustDecimal("11"),
		Volume: ptrDecimal(MustDecimal("100")), Amount: ptrDecimal(MustDecimal("1000")),
		ProviderTimestamp: time.Date(2026, 7, 11, 10, 0, 1, 0, time.UTC),
		FetchedAt:         time.Date(2026, 7, 11, 10, 0, 2, 0, time.UTC),
		RequestID:         "req-1",
		Closed:            true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid K-line rejected: %v", err)
	}

	cases := []ProviderKline{
		validWith(valid, func(k *ProviderKline) { k.DataTime = time.Date(2026, 7, 11, 9, 30, 1, 0, time.UTC) }),
		validWith(valid, func(k *ProviderKline) {
			k.DataTime = time.Date(2026, 7, 11, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
		}),
		validWith(valid, func(k *ProviderKline) { k.TradeDate = "2026/07/11" }),
		validWith(valid, func(k *ProviderKline) { k.Closed = false }),
		validWith(valid, func(k *ProviderKline) { k.Volume = ptrDecimal(MustDecimal("-1")) }),
		validWith(valid, func(k *ProviderKline) { k.High = MustDecimal("8") }),
		validWith(valid, func(k *ProviderKline) { k.Low = MustDecimal("13") }),
	}
	for i, candidate := range cases {
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid K-line case %d unexpectedly passed", i)
		}
	}
}

func validWith(value ProviderKline, mutate func(*ProviderKline)) ProviderKline {
	mutate(&value)
	return value
}

func ptrDecimal(value Decimal) *Decimal { return &value }
