package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/baidu"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoney"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sina"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tencent"
)

const maxProbeResponseBytes int64 = 1 << 20

type activeKlineProvider interface {
	Descriptor() marketdata.ProviderDescriptor
	KlineSpec() marketdata.KlineSpec
	FetchKlines(context.Context, marketdata.KlineRequest) ([]marketdata.NormalizedKline, error)
}

type shadowKlineProvider interface {
	Descriptor() marketdata.ProviderDescriptor
	ShadowSpec() marketdata.KlineSpec
	FetchShadowKlines(context.Context, marketdata.KlineRequest) ([]commonsrc.ShadowPoint, error)
}

type probeArgs struct {
	Market        string
	Feed          string
	Frequency     string
	Subjects      string
	Output        string
	Format        string
	RequestTimout time.Duration
}

func main() {
	args := parseArgs()
	if err := run(context.Background(), args); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "providerprobe failed: %v\n", err)
		os.Exit(1)
	}
}

func parseArgs() probeArgs {
	var args probeArgs
	flag.StringVar(&args.Market, "market", "stock_cn", "market id")
	flag.StringVar(&args.Feed, "feed", "all", "feed kind: all, kline, instrument")
	flag.StringVar(&args.Frequency, "frequency", "1m", "kline frequency")
	flag.StringVar(&args.Subjects, "subjects", "", "comma-separated subject ids")
	flag.StringVar(&args.Output, "output", "", "optional output path")
	flag.StringVar(&args.Format, "format", "auto", "output format: auto, json, markdown")
	flag.DurationVar(&args.RequestTimout, "request-timeout", 2*time.Second, "per-request timeout")
	flag.Parse()
	return args
}

func run(ctx context.Context, args probeArgs) error {
	subjects, err := normalizeSubjects(args.Subjects, args.Feed)
	if err != nil {
		return err
	}
	if strings.TrimSpace(args.Market) != "stock_cn" {
		return fmt.Errorf("--market 仅支持 stock_cn")
	}
	if strings.TrimSpace(args.Frequency) != "1m" {
		return fmt.Errorf("--frequency 仅支持 1m")
	}
	feed := strings.TrimSpace(strings.ToLower(args.Feed))
	if feed != "all" && feed != "kline" && feed != "instrument" {
		return fmt.Errorf("--feed 必须是 all、kline 或 instrument")
	}
	if args.RequestTimout <= 0 {
		return fmt.Errorf("--request-timeout 必须大于 0")
	}

	report := commonsrc.ProbeReport{
		MarketID:    "stock_cn",
		Frequency:   "1m",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Subjects:    subjects,
		Entries:     make([]commonsrc.ProbeEntry, 0, 16),
	}
	clockNow := func() time.Time { return time.Now().UTC() }
	if feed == "all" || feed == "kline" {
		report.Entries = append(report.Entries, probeActiveProvider(ctx, subjects, clockNow, args.RequestTimout, "sina", func(client *http.Client) activeKlineProvider {
			return sina.New(sina.Config{HTTPClient: client, Now: clockNow})
		})...)
		report.Entries = append(report.Entries, probeActiveProvider(ctx, subjects, clockNow, args.RequestTimout, "tencent", func(client *http.Client) activeKlineProvider {
			return tencent.New(tencent.Config{HTTPClient: client, Now: clockNow})
		})...)
		report.Entries = append(report.Entries, probeActiveProvider(ctx, subjects, clockNow, args.RequestTimout, "eastmoney", func(client *http.Client) activeKlineProvider {
			return eastmoney.New(eastmoney.Config{HTTPClient: client, Now: clockNow})
		})...)
		report.Entries = append(report.Entries, probeShadowProvider(ctx, subjects, clockNow, args.RequestTimout, "baidu", func(client *http.Client) shadowKlineProvider {
			return baidu.New(baidu.Config{HTTPClient: client, Now: clockNow})
		})...)
	}
	if feed == "all" || feed == "instrument" {
		for _, providerID := range []string{"sina", "tencent", "eastmoney", "baidu"} {
			report.Entries = append(report.Entries, commonsrc.NewInstrumentNotSupportedEntry(providerID))
		}
	}
	commonsrc.SortEntries(report.Entries)
	if _, err := report.MarshalJSONStrict(); err != nil {
		return err
	}
	content, err := renderReport(report, args.Format, args.Output)
	if err != nil {
		return err
	}
	if strings.TrimSpace(args.Output) == "" {
		_, err = os.Stdout.Write(content)
		if err == nil && len(content) > 0 && content[len(content)-1] != '\n' {
			_, err = os.Stdout.Write([]byte("\n"))
		}
		return err
	}
	return writeAtomic(args.Output, content)
}

func normalizeSubjects(raw, feed string) ([]string, error) {
	if strings.EqualFold(strings.TrimSpace(feed), "instrument") {
		return []string{}, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("--subjects is required for kline probes")
	}
	parts := strings.Split(raw, ",")
	subjects := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		subjectID := strings.TrimSpace(part)
		code, exchange, err := splitSubject(subjectID)
		if err != nil {
			return nil, err
		}
		normalized, err := commonsrc.CanonicalSubjectIDWithExchange(code, exchange)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		subjects = append(subjects, normalized)
	}
	if len(subjects) == 0 {
		return nil, fmt.Errorf("no valid subjects were provided")
	}
	return subjects, nil
}

func splitSubject(subjectID string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(subjectID), ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid subject %q", subjectID)
	}
	return parts[0], parts[1], nil
}

func probeActiveProvider(
	ctx context.Context,
	subjects []string,
	now func() time.Time,
	timeout time.Duration,
	providerID string,
	build func(client *http.Client) activeKlineProvider,
) []commonsrc.ProbeEntry {
	entries := make([]commonsrc.ProbeEntry, 0, len(subjects))
	for _, subjectID := range subjects {
		symbol, err := commonsrc.ProviderSymbol(subjectID)
		if err != nil {
			entries = append(entries, unsupportedSymbolEntry(providerID, subjectID, err))
			continue
		}
		client, observation := newObservedClient(timeout)
		provider := build(client)
		started := time.Now()
		rows, fetchErr := provider.FetchKlines(ctx, marketdata.KlineRequest{
			SubjectID:      subjectID,
			ProviderSymbol: symbol,
			Frequency:      "1m",
			Limit:          3,
			Now:            now(),
			RequestID:      fmt.Sprintf("providerprobe-%s-%d", providerID, started.UnixNano()),
		})
		entry := commonsrc.ProbeEntry{
			ProviderID:    providerID,
			FeedKind:      commonsrc.ProbeFeedKline,
			Exchange:      subjectExchange(subjectID),
			SubjectID:     subjectID,
			Symbol:        symbol,
			HTTPStatus:    observation.StatusCode(),
			LatencyMS:     time.Since(started).Milliseconds(),
			Result:        commonsrc.ProbeResultFail,
			ErrorKind:     classifyError(fetchErr),
			Error:         sanitizeError(fetchErr),
			SupportsRange: false,
			HasOHLCV:      false,
			VolumeUnit:    "unknown",
			AmountUnit:    "unknown",
		}
		if fetchErr == nil {
			entry.Result = commonsrc.ProbeResultPass
			entry.ErrorKind = "none"
			entry.VolumeUnit, entry.AmountUnit = unitsForActiveProvider(providerID)
			entry.HasOHLCV = hasCompleteOHLCV(rows)
			entry.BarCount = len(rows)
			if len(rows) > 0 {
				entry.EarliestBarStart = rows[0].BarStart.UTC().Format(time.RFC3339)
				entry.LatestBarStart = rows[len(rows)-1].BarStart.UTC().Format(time.RFC3339)
				entry.LatestBarEnd = rows[len(rows)-1].BarEnd.UTC().Format(time.RFC3339)
			}
			if !entry.HasOHLCV {
				entry.Result = commonsrc.ProbeResultFail
				entry.ErrorKind = "protocol"
				entry.Error = "latest closed bar is missing complete OHLCV"
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func probeShadowProvider(
	ctx context.Context,
	subjects []string,
	now func() time.Time,
	timeout time.Duration,
	providerID string,
	build func(client *http.Client) shadowKlineProvider,
) []commonsrc.ProbeEntry {
	entries := make([]commonsrc.ProbeEntry, 0, len(subjects))
	for _, subjectID := range subjects {
		symbol, err := commonsrc.ProviderSymbol(subjectID)
		if err != nil {
			entries = append(entries, unsupportedSymbolEntry(providerID, subjectID, err))
			continue
		}
		client, observation := newObservedClient(timeout)
		provider := build(client)
		started := time.Now()
		points, fetchErr := provider.FetchShadowKlines(ctx, marketdata.KlineRequest{
			SubjectID:      subjectID,
			ProviderSymbol: symbol,
			Frequency:      "1m",
			Limit:          3,
			Now:            now(),
			RequestID:      fmt.Sprintf("providerprobe-%s-%d", providerID, started.UnixNano()),
		})
		entry := commonsrc.ProbeEntry{
			ProviderID:    providerID,
			FeedKind:      commonsrc.ProbeFeedKline,
			Exchange:      subjectExchange(subjectID),
			SubjectID:     subjectID,
			Symbol:        symbol,
			HTTPStatus:    observation.StatusCode(),
			LatencyMS:     time.Since(started).Milliseconds(),
			Result:        commonsrc.ProbeResultFail,
			ErrorKind:     classifyError(fetchErr),
			Error:         sanitizeError(fetchErr),
			SupportsRange: false,
			HasOHLCV:      false,
			VolumeUnit:    "unknown",
			AmountUnit:    "unknown",
		}
		if fetchErr == nil {
			entry.Result = commonsrc.ProbeResultShadowOnly
			entry.ErrorKind = "shadow_only"
			entry.Error = "only price/volume/amount observed"
			entry.AmountUnit = "cny"
			entry.BarCount = len(points)
			if len(points) > 0 {
				entry.EarliestBarStart = points[0].BarStart.UTC().Format(time.RFC3339)
				entry.LatestBarStart = points[len(points)-1].BarStart.UTC().Format(time.RFC3339)
				entry.LatestBarEnd = points[len(points)-1].BarStart.UTC().Add(time.Minute).Format(time.RFC3339)
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func renderReport(report commonsrc.ProbeReport, format, output string) ([]byte, error) {
	switch resolveFormat(format, output) {
	case "json":
		return report.MarshalJSONStrict()
	case "markdown":
		return []byte(report.RenderMarkdown()), nil
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

func resolveFormat(format, output string) string {
	switch normalized := strings.TrimSpace(strings.ToLower(format)); normalized {
	case "", "auto":
		extension := strings.ToLower(filepath.Ext(strings.TrimSpace(output)))
		if extension == ".md" || extension == ".markdown" {
			return "markdown"
		}
		return "json"
	case "json", "markdown":
		return normalized
	default:
		return normalized
	}
}

func writeAtomic(path string, content []byte) (err error) {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = temp.Write(content); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func unitsForActiveProvider(providerID string) (string, string) {
	switch providerID {
	case "sina", "eastmoney":
		return "shares", "cny"
	case "tencent":
		return "shares", "not_available"
	default:
		return "unknown", "unknown"
	}
}

func hasCompleteOHLCV(rows []marketdata.NormalizedKline) bool {
	if len(rows) == 0 {
		return false
	}
	last := rows[len(rows)-1]
	if err := marketdata.ValidateNormalizedKline(last); err != nil {
		return false
	}
	return last.VolumeShares >= 0 && last.AmountCNY >= 0
}

func classifyError(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, marketdata.ErrUnsupportedSymbol):
		return "unsupported_symbol"
	default:
		return string(marketdata.ClassifyError(err))
	}
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, token := range []string{"cookie", "header"} {
		if strings.Contains(strings.ToLower(message), token) {
			return "sensitive transport details redacted"
		}
	}
	return message
}

func subjectExchange(subjectID string) string {
	_, exchange, err := splitSubject(subjectID)
	if err != nil {
		return "UNKNOWN"
	}
	return exchange
}

func unsupportedSymbolEntry(providerID, subjectID string, err error) commonsrc.ProbeEntry {
	return commonsrc.ProbeEntry{
		ProviderID:    providerID,
		FeedKind:      commonsrc.ProbeFeedKline,
		Exchange:      subjectExchange(subjectID),
		SubjectID:     subjectID,
		Symbol:        "",
		HTTPStatus:    0,
		LatencyMS:     0,
		Result:        commonsrc.ProbeResultFail,
		ErrorKind:     "unsupported_symbol",
		Error:         sanitizeError(err),
		SupportsRange: false,
		HasOHLCV:      false,
		VolumeUnit:    "unknown",
		AmountUnit:    "unknown",
	}
}

type observedClient struct {
	mu         sync.Mutex
	statusCode int
}

func (o *observedClient) setStatusCode(statusCode int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.statusCode = statusCode
}

func (o *observedClient) StatusCode() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.statusCode
}

type observedRoundTripper struct {
	base        http.RoundTripper
	observation *observedClient
}

func (t observedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.observation.setStatusCode(resp.StatusCode)
	resp.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: io.LimitReader(resp.Body, maxProbeResponseBytes),
		Closer: resp.Body,
	}
	return resp, nil
}

func newObservedClient(timeout time.Duration) (*http.Client, *observedClient) {
	observation := &observedClient{}
	base := http.DefaultTransport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Timeout: timeout,
		Transport: observedRoundTripper{
			base:        base,
			observation: observation,
		},
	}, observation
}
