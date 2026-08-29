package stockcn

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

type ProbeFeedKind string

const (
	ProbeFeedKline      ProbeFeedKind = "kline"
	ProbeFeedInstrument ProbeFeedKind = "instrument"
)

type ProbeResult string

const (
	ProbeResultPass         ProbeResult = "pass"
	ProbeResultFail         ProbeResult = "fail"
	ProbeResultShadowOnly   ProbeResult = "shadow_only"
	ProbeResultNotSupported ProbeResult = "not_supported"
)

type ShadowPoint struct {
	SubjectID      string
	ProviderID     string
	ProviderSymbol string
	BarStart       time.Time
	Price          float64
	VolumeShares   float64
	AmountCNY      float64
	FetchedAt      time.Time
	RequestID      string
}

type ProbeEntry struct {
	ProviderID       string        `json:"provider_id"`
	FeedKind         ProbeFeedKind `json:"feed_kind"`
	Exchange         string        `json:"exchange"`
	SubjectID        string        `json:"subject_id"`
	Symbol           string        `json:"symbol"`
	HTTPStatus       int           `json:"http_status"`
	LatencyMS        int64         `json:"latency_ms"`
	Result           ProbeResult   `json:"result"`
	ErrorKind        string        `json:"error_kind"`
	Error            string        `json:"error"`
	BarCount         int           `json:"bar_count"`
	LatestBarStart   string        `json:"latest_bar_start"`
	LatestBarEnd     string        `json:"latest_bar_end"`
	EarliestBarStart string        `json:"earliest_bar_start"`
	SupportsRange    bool          `json:"supports_range"`
	HasOHLCV         bool          `json:"has_ohlcv"`
	VolumeUnit       string        `json:"volume_unit"`
	AmountUnit       string        `json:"amount_unit"`
	PageCount        int           `json:"page_count"`
	InstrumentCount  int           `json:"instrument_count"`
	Complete         bool          `json:"complete"`
	ExchangeCoverage []string      `json:"exchange_coverage"`
}

type ProbeReport struct {
	MarketID    string       `json:"market_id"`
	Frequency   string       `json:"frequency"`
	GeneratedAt string       `json:"generated_at"`
	Subjects    []string     `json:"subjects"`
	Entries     []ProbeEntry `json:"entries"`
}

func DecodeProbeReport(reader io.Reader) (ProbeReport, error) {
	var report ProbeReport
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return ProbeReport{}, err
	}
	if decoder.More() {
		return ProbeReport{}, fmt.Errorf("probe report must contain a single JSON document")
	}
	if err := report.Validate(); err != nil {
		return ProbeReport{}, err
	}
	return report, nil
}

func (r ProbeReport) Validate() error {
	if strings.TrimSpace(r.MarketID) == "" {
		return fmt.Errorf("market_id is required")
	}
	if strings.TrimSpace(r.Frequency) == "" {
		return fmt.Errorf("frequency is required")
	}
	if strings.TrimSpace(r.GeneratedAt) == "" {
		return fmt.Errorf("generated_at is required")
	}
	if _, err := time.Parse(time.RFC3339, r.GeneratedAt); err != nil {
		return fmt.Errorf("generated_at must be RFC3339: %w", err)
	}
	if len(r.Subjects) == 0 {
		return fmt.Errorf("subjects are required")
	}
	if len(r.Entries) == 0 {
		return fmt.Errorf("entries are required")
	}
	for index, entry := range r.Entries {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("entries[%d]: %w", index, err)
		}
	}
	return nil
}

func (e ProbeEntry) Validate() error {
	if strings.TrimSpace(e.ProviderID) == "" {
		return fmt.Errorf("provider_id is required")
	}
	if e.FeedKind != ProbeFeedKline && e.FeedKind != ProbeFeedInstrument {
		return fmt.Errorf("feed_kind %q is unsupported", e.FeedKind)
	}
	if strings.TrimSpace(e.Exchange) == "" {
		return fmt.Errorf("exchange is required")
	}
	if e.HTTPStatus < 0 {
		return fmt.Errorf("http_status must be >= 0")
	}
	if e.LatencyMS < 0 {
		return fmt.Errorf("latency_ms must be >= 0")
	}
	if e.Result != ProbeResultPass && e.Result != ProbeResultFail && e.Result != ProbeResultShadowOnly && e.Result != ProbeResultNotSupported {
		return fmt.Errorf("result %q is unsupported", e.Result)
	}
	if strings.TrimSpace(e.ErrorKind) == "" {
		return fmt.Errorf("error_kind is required")
	}
	if containsSensitiveToken(e.Error) {
		return fmt.Errorf("error must not contain header/cookie data")
	}
	switch e.FeedKind {
	case ProbeFeedKline:
		if strings.TrimSpace(e.SubjectID) == "" {
			return fmt.Errorf("subject_id is required for kline")
		}
		if strings.TrimSpace(e.Symbol) == "" {
			return fmt.Errorf("symbol is required for kline")
		}
		if e.BarCount < 0 {
			return fmt.Errorf("bar_count must be >= 0")
		}
		if strings.TrimSpace(e.VolumeUnit) == "" {
			return fmt.Errorf("volume_unit is required for kline")
		}
		if strings.TrimSpace(e.AmountUnit) == "" {
			return fmt.Errorf("amount_unit is required for kline")
		}
	case ProbeFeedInstrument:
		if e.PageCount < 0 || e.InstrumentCount < 0 {
			return fmt.Errorf("instrument counters must be >= 0")
		}
		if e.ExchangeCoverage == nil {
			return fmt.Errorf("exchange_coverage is required for instrument")
		}
	}
	return nil
}

func (r ProbeReport) MarshalJSONStrict() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(r, "", "  ")
}

func (r ProbeReport) RenderMarkdown() string {
	var b strings.Builder
	b.WriteString("# stock_cn Provider Probe\n\n")
	fmt.Fprintf(&b, "- GeneratedAt: `%s`\n", r.GeneratedAt)
	fmt.Fprintf(&b, "- Frequency: `%s`\n", r.Frequency)
	fmt.Fprintf(&b, "- Subjects: `%s`\n\n", strings.Join(r.Subjects, "`, `"))
	b.WriteString("| Provider | Feed | Result | Exchange | Subject | Symbol | HTTP | LatencyMs | ErrorKind |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | ---: | ---: | --- |\n")
	for _, entry := range r.Entries {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %d | %d | %s |\n",
			entry.ProviderID,
			entry.FeedKind,
			strings.ToUpper(string(entry.Result)),
			entry.Exchange,
			displayValue(entry.SubjectID, "-"),
			displayValue(entry.Symbol, "-"),
			entry.HTTPStatus,
			entry.LatencyMS,
			entry.ErrorKind,
		)
		switch entry.FeedKind {
		case ProbeFeedKline:
			fmt.Fprintf(&b, "\n  - bars=%d latest=`%s -> %s` earliest=`%s` has_ohlcv=%t supports_range=%t volume_unit=`%s` amount_unit=`%s`",
				entry.BarCount,
				displayValue(entry.LatestBarStart, "-"),
				displayValue(entry.LatestBarEnd, "-"),
				displayValue(entry.EarliestBarStart, "-"),
				entry.HasOHLCV,
				entry.SupportsRange,
				entry.VolumeUnit,
				entry.AmountUnit,
			)
		case ProbeFeedInstrument:
			fmt.Fprintf(&b, "\n  - pages=%d instruments=%d complete=%t exchanges=`%s`",
				entry.PageCount,
				entry.InstrumentCount,
				entry.Complete,
				strings.Join(entry.ExchangeCoverage, "`, `"),
			)
		}
		if strings.TrimSpace(entry.Error) != "" {
			fmt.Fprintf(&b, "\n  - note: %s", strings.ReplaceAll(entry.Error, "\n", " "))
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func containsSensitiveToken(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "cookie") || strings.Contains(lower, "header")
}

func displayValue(value, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func NewInstrumentNotSupportedEntry(providerID string) ProbeEntry {
	return ProbeEntry{
		ProviderID:       strings.TrimSpace(providerID),
		FeedKind:         ProbeFeedInstrument,
		Exchange:         "ALL",
		SubjectID:        "",
		Symbol:           "",
		HTTPStatus:       0,
		LatencyMS:        0,
		Result:           ProbeResultNotSupported,
		ErrorKind:        "not_supported",
		Error:            "instrument probe is not implemented",
		BarCount:         0,
		LatestBarStart:   "",
		LatestBarEnd:     "",
		EarliestBarStart: "",
		SupportsRange:    false,
		HasOHLCV:         false,
		VolumeUnit:       "",
		AmountUnit:       "",
		PageCount:        0,
		InstrumentCount:  0,
		Complete:         false,
		ExchangeCoverage: []string{},
	}
}

func SortEntries(entries []ProbeEntry) {
	slices.SortFunc(entries, func(left, right ProbeEntry) int {
		if compare := strings.Compare(left.ProviderID, right.ProviderID); compare != 0 {
			return compare
		}
		if compare := strings.Compare(string(left.FeedKind), string(right.FeedKind)); compare != 0 {
			return compare
		}
		if compare := strings.Compare(left.SubjectID, right.SubjectID); compare != 0 {
			return compare
		}
		return strings.Compare(left.Exchange, right.Exchange)
	})
}
