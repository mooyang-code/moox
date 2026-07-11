package marketdata

import (
	"fmt"
	"strings"
	"time"
)

type ProviderKline struct {
	SubjectID         string
	ProviderID        ProviderID
	ProviderSymbol    string
	Frequency         Frequency
	DataTime          time.Time
	CloseTime         time.Time
	TradeDate         string
	FeedScope         string
	VolumeUnit        string
	AmountUnit        string
	Open              Decimal
	High              Decimal
	Low               Decimal
	Close             Decimal
	Volume            *Decimal
	Amount            *Decimal
	ProviderTimestamp time.Time
	FetchedAt         time.Time
	RequestID         string
	Closed            bool
}

type ResolvedKline struct {
	ProviderKline
	SourceDatasetID string
	QualityStatus   string
	Revision        int64
	ResolvedAt      time.Time
}

func (k ProviderKline) Validate() error {
	if strings.TrimSpace(k.SubjectID) == "" || k.ProviderID == "" || strings.TrimSpace(k.ProviderSymbol) == "" {
		return fmt.Errorf("subject, provider and provider symbol are required")
	}
	frequency, err := ParseFrequency(string(k.Frequency))
	if err != nil {
		return err
	}
	if k.Frequency != frequency {
		return fmt.Errorf("frequency %q is not normalized", k.Frequency)
	}
	if k.DataTime.IsZero() || k.DataTime.Location() != time.UTC || !isBucketAligned(k.DataTime, frequency) {
		return fmt.Errorf("data time must be an aligned UTC bucket")
	}
	if k.CloseTime.IsZero() || k.CloseTime.Location() != time.UTC || !k.CloseTime.After(k.DataTime) {
		return fmt.Errorf("close time must be a later UTC time")
	}
	if _, err := time.Parse("2006-01-02", k.TradeDate); err != nil {
		return fmt.Errorf("invalid trade date: %w", err)
	}
	if strings.TrimSpace(k.FeedScope) == "" || strings.TrimSpace(k.VolumeUnit) == "" || strings.TrimSpace(k.AmountUnit) == "" {
		return fmt.Errorf("feed scope and units are required")
	}
	if !k.Closed {
		return fmt.Errorf("unclosed kline is not publishable")
	}
	if err := k.Open.Validate(-1, false); err != nil {
		return fmt.Errorf("open: %w", err)
	}
	if err := k.High.Validate(-1, false); err != nil {
		return fmt.Errorf("high: %w", err)
	}
	if err := k.Low.Validate(-1, false); err != nil {
		return fmt.Errorf("low: %w", err)
	}
	if err := k.Close.Validate(-1, false); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if k.High.Cmp(k.Open) < 0 || k.High.Cmp(k.Close) < 0 || k.High.Cmp(k.Low) < 0 {
		return fmt.Errorf("high must be at least open, close and low")
	}
	if k.Low.Cmp(k.Open) > 0 || k.Low.Cmp(k.Close) > 0 {
		return fmt.Errorf("low must not exceed open or close")
	}
	for name, value := range map[string]*Decimal{"volume": k.Volume, "amount": k.Amount} {
		if value != nil {
			if err := value.Validate(-1, false); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if k.ProviderTimestamp.IsZero() || k.ProviderTimestamp.Location() != time.UTC || k.FetchedAt.IsZero() || k.FetchedAt.Location() != time.UTC {
		return fmt.Errorf("provider timestamp and fetched at must be UTC")
	}
	if strings.TrimSpace(k.RequestID) == "" {
		return fmt.Errorf("request id is required")
	}
	return nil
}

func isBucketAligned(value time.Time, frequency Frequency) bool {
	minutes := frequency.DurationMinutes()
	if minutes == 0 || value.Second() != 0 || value.Nanosecond() != 0 {
		return false
	}
	if minutes >= 1440 {
		return value.Hour() == 0 && value.Minute() == 0
	}
	return value.Minute() == 0 && value.Hour()*60%minutes == 0
}
