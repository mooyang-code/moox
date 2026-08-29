package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	stocksource "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
)

const (
	StockCNSpaceID   = "stock_cn"
	StockCNDatasetID = "stock_cn_kline"
	StockCNRouteID   = "stock_cn_kline_1m_v1"
)

// KlinePipeline is the provider-independent stock write boundary. Fetchers
// return complete bars; the pipeline writes each bar as one complete row.
type KlinePipeline struct {
	Router         *marketdata.Router
	Storage        Storage
	CandidateChain []string
	RouteID        string
	Now            func() time.Time
	Calendar       *stockmarket.Calendar
}

func (p *KlinePipeline) Execute(ctx context.Context, req Request) (*marketfetchpb.MarketFetchBatchCompleted, error) {
	if p == nil || p.Router == nil || p.Storage == nil {
		return nil, fmt.Errorf("stock kline pipeline is not initialized")
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	if req.SpaceID != StockCNSpaceID || req.DatasetID != StockCNDatasetID || req.Frequency != "1m" {
		return nil, fmt.Errorf("stock kline pipeline requires stock_cn/stock_cn_kline/1m")
	}
	chain := normalizeCandidateChain(p.CandidateChain, req.Provider)
	if len(chain) < 2 {
		return nil, fmt.Errorf("stock kline pipeline requires at least two candidate providers")
	}
	started := time.Now()
	if p.Now != nil {
		started = p.Now()
	}
	if req.BatchKind == domain.BatchKindRealtime && p.Calendar != nil {
		shouldCollect, err := stockCNShouldCollectMinute(p.Calendar, started)
		if err != nil {
			return nil, err
		}
		if !shouldCollect {
			results := make([]domain.ItemResult, len(req.Items))
			for index, item := range req.Items {
				results[index] = successResult(item)
			}
			return buildCompletion(req, results, started, 0), nil
		}
	}
	results := make([]domain.ItemResult, len(req.Items))
	rows := make([]*storagepb.RowFieldUpsert, 0, len(req.Items)*MaxRealtimeRows)
	routerSession := p.Router.NewSession()
	concurrency := req.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency > len(req.Items) {
		concurrency = len(req.Items)
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var rowsMu sync.Mutex
	for index, item := range req.Items {
		index, item := index, item
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[index] = failureResult(item, domain.ItemOutcomeNetworkError, "deadline_exhausted", ctx.Err())
				return
			}
			providerSymbol, err := stocksource.ProviderSymbol(item.SubjectID)
			if err != nil {
				results[index] = failureResult(item, domain.ItemOutcomeInvalid, "symbol", err)
				return
			}
			limit := item.BarLimit
			if limit <= 0 {
				limit = MaxRealtimeRows
			}
			fetched, err := fetchKlinesFromChain(ctx, routerSession, marketdata.KlineRequest{MarketID: StockCNSpaceID, ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, SubjectID: item.SubjectID, ProviderSymbol: providerSymbol, Frequency: "1m", Limit: limit, StartTime: parseOptionalTime(item.StartTime), Now: started.UTC(), RequestID: firstNonEmptyString(item.SourceEventID, req.RequestID, req.BatchID)}, chain)
			if err != nil {
				results[index] = failureResult(item, classifyError(err), errorType(err), err)
				return
			}
			itemRows := make([]*storagepb.RowFieldUpsert, 0, len(fetched))
			coverageStart := parseOptionalTime(item.StartTime)
			for _, bar := range fetched {
				if !coverageStart.IsZero() && bar.BarStart.Before(coverageStart) {
					continue
				}
				rank := providerRank(chain, bar.ProviderID)
				row, rowErr := stockKlineRow(bar, firstNonEmptyString(p.RouteID, StockCNRouteID), rank)
				if rowErr != nil {
					results[index] = failureResult(item, domain.ItemOutcomeInvalid, "normalize", rowErr)
					return
				}
				itemRows = append(itemRows, row)
			}
			if len(itemRows) == 0 {
				results[index] = failureResult(item, domain.ItemOutcomeInvalid, "coverage", fmt.Errorf("provider returned no bars inside the requested coverage"))
				return
			}
			rowsMu.Lock()
			rows = append(rows, itemRows...)
			rowsMu.Unlock()
			results[index] = successResult(item)
		}()
	}
	wg.Wait()
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i].GetKey().GetTimeSeries(), rows[j].GetKey().GetTimeSeries()
		if left.GetSubjectId() == right.GetSubjectId() {
			return left.GetDataTime() < right.GetDataTime()
		}
		return left.GetSubjectId() < right.GetSubjectId()
	})
	if len(rows) > 0 {
		var err error
		if storage, ok := p.Storage.(sourceStorage); ok {
			err = storage.UpsertFieldsWithSource(ctx, rows, req.BatchID)
		} else {
			err = p.Storage.UpsertFields(ctx, rows)
		}
		if err != nil {
			for index := range results {
				if results[index].Outcome == domain.ItemOutcomeSuccess {
					results[index] = failureResult(results[index].CollectionItem, domain.ItemOutcomeStorageError, "storage", err)
				}
			}
		}
	}
	completed := time.Now()
	if p.Now != nil {
		completed = p.Now()
	}
	return buildCompletion(req, results, completed, completed.Sub(started)), nil
}

func fetchKlinesFromChain(ctx context.Context, session *marketdata.RouterSession, req marketdata.KlineRequest, chain []string) ([]marketdata.NormalizedKline, error) {
	var lastErr error
	for index, provider := range chain {
		attemptCtx := ctx
		cancel := func() {}
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			attemptsLeft := len(chain) - index
			if remaining <= 0 {
				return nil, ctx.Err()
			}
			attemptCtx, cancel = context.WithTimeout(ctx, remaining/time.Duration(attemptsLeft))
		}
		rows, err := session.FetchKlines(attemptCtx, req, []string{provider})
		cancel()
		if err == nil {
			spec, specErr := session.KlineSpec(provider)
			if specErr != nil {
				return nil, specErr
			}
			if coverageErr := spec.History.ValidateCoverage(rows, req.StartTime); coverageErr != nil {
				lastErr = coverageErr
				continue
			}
			return rows, nil
		}
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("%w: provider %s attempt budget exhausted", marketdata.ErrTimeout, provider)
		}
		if errors.Is(err, marketdata.ErrProviderNotFound) {
			// A shared invocation breaker reports a provider skipped after its
			// failure streak as ErrProviderNotFound. Keep the remaining budget
			// for the next candidate instead of treating that skip as terminal.
			lastErr = err
			continue
		}
		if errors.Is(err, marketdata.ErrHistoryOutOfRange) || errors.Is(err, marketdata.ErrHistoryCoverage) {
			lastErr = err
			continue
		}
		if !marketdata.CanFallback(ctx, err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func stockCNShouldCollectMinute(calendar *stockmarket.Calendar, now time.Time) (bool, error) {
	if calendar == nil {
		return false, fmt.Errorf("stock_cn calendar is required")
	}
	if err := calendar.ValidateHorizon(now, 14); err != nil {
		return false, fmt.Errorf("stock_cn calendar horizon: %w", err)
	}
	location := calendar.Location()
	if location == nil {
		return false, fmt.Errorf("stock_cn calendar timezone is unavailable")
	}
	local := now.In(location)
	expected, err := calendar.ExpectedMinuteBars(local.Format("2006-01-02"))
	if err != nil {
		return false, err
	}
	target := now.UTC().Truncate(time.Minute).Add(-time.Minute)
	for _, barStart := range expected {
		if barStart.Equal(target) {
			return true, nil
		}
	}
	return false, nil
}

func stockKlineRow(bar marketdata.NormalizedKline, routeID string, routeRank int) (*storagepb.RowFieldUpsert, error) {
	if err := marketdata.ValidateNormalizedKline(bar); err != nil {
		return nil, err
	}
	qualityStatus := "accepted"
	if routeRank > 1 {
		qualityStatus = "fallback"
	}
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{
			SpaceId: StockCNSpaceID, DatasetId: StockCNDatasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: bar.SubjectID, Freq: "1m", DataTime: bar.BarStart.UTC().Format(time.RFC3339Nano), SeriesTag: "",
			}},
		},
		Fields: []*storagepb.FieldValue{
			doubleValue("open", bar.Open), doubleValue("high", bar.High), doubleValue("low", bar.Low), doubleValue("close", bar.Close),
			doubleValue("volume", bar.VolumeShares), doubleValue("amount", bar.AmountCNY),
			timeValue("trade_date", tradeDateUTC(bar.BarStart)), timeValue("close_time", bar.BarEnd),
			stringValue("volume_unit", "shares"), stringValue("amount_unit", amountUnit(bar)),
			stringValue("provider_symbol", bar.ProviderSymbol), timeValue("provider_timestamp", bar.ProviderTimestamp),
			timeValue("fetched_at", bar.FetchedAt), stringValue("request_id", bar.RequestID),
			stringValue("route_id", routeID), intValue("route_rank", int64(routeRank)),
			stringValue("source_provider", bar.ProviderID), stringValue("quality_status", qualityStatus),
		},
	}, nil
}

func normalizeCandidateChain(configured []string, primary string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, 3)
	for _, provider := range append([]string{primary}, configured...) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" || provider == "stock_cn_multi" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		result = append(result, provider)
		if len(result) == 3 {
			break
		}
	}
	return result
}

func providerRank(chain []string, provider string) int {
	for index, candidate := range chain {
		if candidate == provider {
			return index + 1
		}
	}
	return len(chain) + 1
}

func parseOptionalTime(raw string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	return parsed.UTC()
}

func tradeDateUTC(value time.Time) time.Time {
	location, _ := time.LoadLocation("Asia/Shanghai")
	local := value.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).UTC()
}

func amountUnit(bar marketdata.NormalizedKline) string {
	if bar.AmountCNY == 0 && bar.ProviderID == "tencent" {
		return "not_available"
	}
	return "cny"
}

func doubleValue(field string, value float64) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: field, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func intValue(field string, value int64) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: field, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: value}}}
}

func stringValue(field, value string) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: field, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}}
}

func timeValue(field string, value time.Time) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: field, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_TimeValue{TimeValue: value.UTC().Format(time.RFC3339Nano)}}}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
