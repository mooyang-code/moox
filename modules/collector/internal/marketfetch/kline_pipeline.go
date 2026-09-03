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
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
)

const (
	StockCNSpaceID   = "stock_cn"
	StockCNDatasetID = "stock_cn_kline"
	StockCNRouteID   = "stock_cn_kline_1m_v1"
	// A single invocation may try the configured provider and one fallback.
	// Retry generations resume after that same bounded window.
	klineProviderAttemptBudget = 2
)

// KlinePipeline is the provider-independent stock write boundary. Fetchers
// return complete bars; the pipeline writes each bar as one complete row.
type KlinePipeline struct {
	Router         *marketdata.Router
	Storage        Storage
	CandidateChain []string
	RouteID        string
	SpaceID        string
	MarketID       string
	ProductType    marketdata.ProductType
	InstrumentType marketdata.InstrumentType
	DatasetID      string
	SourceID       string
	AutoBindSource bool
	SeriesTag      string
	SettleDelay    time.Duration
	Now            func() time.Time
	Calendar       *stockmarket.Calendar
	Metrics        *Metrics
}

func (p *KlinePipeline) Execute(ctx context.Context, req Request) (*marketfetchpb.MarketFetchBatchCompleted, error) {
	if p == nil || p.Router == nil || p.Storage == nil {
		return nil, fmt.Errorf("stock kline pipeline is not initialized")
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	spaceID := firstNonEmptyString(p.SpaceID, req.SpaceID)
	datasetID := firstNonEmptyString(p.DatasetID, req.DatasetID)
	frequency, frequencyErr := marketdata.ParseFrequency(req.Frequency)
	if frequencyErr != nil {
		return nil, frequencyErr
	}
	isStock := p.MarketID == StockCNSpaceID || req.SpaceID == StockCNSpaceID
	isStockEquity := isStock && (p.InstrumentType == "" || p.InstrumentType == marketdata.InstrumentEquity)
	if req.SpaceID != spaceID || req.DatasetID != datasetID || (isStockEquity && frequency != marketdata.FrequencyMinute) {
		return nil, fmt.Errorf("kline pipeline requires %s/%s/1m for stock_cn", spaceID, datasetID)
	}
	sourceID := firstNonEmptyString(p.SourceID, req.SourceID)
	if p.SourceID != "" && req.SourceID != "" && !strings.EqualFold(strings.TrimSpace(p.SourceID), strings.TrimSpace(req.SourceID)) {
		return nil, fmt.Errorf("kline pipeline source binding differs: pipeline=%s request=%s", p.SourceID, req.SourceID)
	}
	chain := normalizeCandidateChain(p.CandidateChain, req.Provider)
	if len(chain) == 0 {
		return nil, fmt.Errorf("kline pipeline requires at least one candidate provider")
	}
	stockRouteVersion := ""
	var stockSources []stockCNSource
	if isStock && p.AutoBindSource && sourceID == "" {
		stockRouteVersion, stockSources, frequencyErr = stockCNAssignmentRoute()
		if frequencyErr != nil {
			return nil, frequencyErr
		}
	}
	if p.SourceID != "" {
		if req.Provider != "" && !strings.EqualFold(strings.TrimSpace(req.Provider), strings.TrimSpace(chain[0])) {
			return nil, fmt.Errorf("kline pipeline provider binding differs: pipeline=%s request=%s", chain[0], req.Provider)
		}
		chain = []string{chain[0]}
	}
	started := time.Now()
	if p.Now != nil {
		started = p.Now()
	}
	if req.BatchKind == domain.BatchKindRealtime && p.Calendar != nil && frequency == marketdata.FrequencyMinute {
		shouldCollect, err := stockCNShouldCollectMinute(p.Calendar, started, p.SettleDelay)
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
			providerSymbol := strings.TrimSpace(item.Symbol)
			if p.MarketID == StockCNSpaceID || p.MarketID == "" && req.SpaceID == StockCNSpaceID {
				converted, symbolErr := stockProviderSymbol(item.SubjectID, providerSymbol)
				if symbolErr != nil {
					results[index] = failureResult(item, domain.ItemOutcomeInvalid, "symbol", symbolErr)
					return
				}
				providerSymbol = converted
			}
			if providerSymbol == "" {
				results[index] = failureResult(item, domain.ItemOutcomeInvalid, "symbol", fmt.Errorf("provider symbol is required"))
				return
			}
			itemSourceID := firstNonEmptyString(item.SourceID, sourceID)
			itemChain := chain
			if isStock && p.AutoBindSource && itemSourceID == "" {
				selectedSource, ok := stockSourceForSubject(stockSources, stockRouteVersion, item.SubjectID)
				if !ok {
					results[index] = failureResult(item, domain.ItemOutcomeInvalid, "source_binding", fmt.Errorf("stock_cn has no source assignment for %s", item.SubjectID))
					return
				}
				itemSourceID = selectedSource.SourceID
				itemChain = []string{selectedSource.Provider}
			}
			limit := item.BarLimit
			if limit <= 0 {
				limit = MaxRealtimeRows
			}
			marketID := marketdata.MarketID(firstNonEmptyString(p.MarketID, req.SpaceID))
			productType := p.ProductType
			if productType == "" {
				productType = marketdata.ProductEquity
			}
			instrumentType := p.InstrumentType
			if instrumentType == "" {
				instrumentType = marketdata.InstrumentEquity
			}
			exchangeID := marketdata.ExchangeID("")
			if p.MarketID == StockCNSpaceID || p.MarketID == "" && req.SpaceID == StockCNSpaceID {
				exchangeID = stockProviderExchange(item.SubjectID)
			}
			coverageStart := parseOptionalTime(item.StartTime)
			coverageEnd := parseOptionalTime(item.EndTime)
			historyAsOf := started.UTC()
			if item.Canary && isStock && p.Calendar != nil {
				canaryStart, canaryEnd, calendarErr := p.Calendar.LatestClosedMinute(started.UTC(), p.SettleDelay)
				if calendarErr != nil {
					results[index] = failureResult(item, domain.ItemOutcomeProviderError, "calendar", calendarErr)
					return
				}
				coverageStart, coverageEnd, historyAsOf = canaryStart, canaryEnd, canaryEnd
			}
			fetched, selectedProvider, nextCandidateIndex, err := fetchKlinesFromChain(ctx, routerSession, marketdata.KlineRequest{MarketID: marketID, ExchangeID: exchangeID, ProductType: productType, InstrumentType: instrumentType, SubjectID: item.SubjectID, ProviderSymbol: providerSymbol, SourceID: itemSourceID, Frequency: req.Frequency, Limit: limit, StartTime: coverageStart, EndTime: coverageEnd, Now: started.UTC(), HistoryAsOf: historyAsOf, RequestID: firstNonEmptyString(item.SourceEventID, req.RequestID, req.BatchID), RateBudgetRatio: item.RateBudgetRatio}, itemChain, item.CandidateIndex)
			if err != nil {
				item.CandidateIndex = nextCandidateIndex
				p.observeFeed(req, selectedProvider, "kline", metricKlineResult(err))
				results[index] = failureResult(item, classifyError(err), errorType(err), err)
				return
			}
			if err := bindKlineSource(fetched, itemChain[0], itemSourceID); err != nil {
				item.CandidateIndex = nextCandidateIndex
				results[index] = failureResult(item, domain.ItemOutcomeInvalid, "source_binding", err)
				return
			}
			itemRows := make([]*storagepb.RowFieldUpsert, 0, len(fetched))
			for _, bar := range fetched {
				// The scheduler normally selects a settled target bar, but the
				// provider response is still an untrusted boundary. Realtime rows
				// must remain outside the settle window even when a feed returns a
				// newer, just-closed bar or ignores the requested range.
				if req.BatchKind == domain.BatchKindRealtime && p.SettleDelay > 0 && bar.BarEnd.Add(p.SettleDelay).After(started.UTC()) {
					continue
				}
				if !coverageStart.IsZero() && bar.BarStart.Before(coverageStart) {
					continue
				}
				// Some public feeds ignore range parameters and return their
				// latest page. Enforce the half-open requested interval at the
				// common write boundary so GapRepair cannot write the wrong bar.
				if !coverageEnd.IsZero() && !bar.BarStart.Before(coverageEnd) {
					continue
				}
				rank := providerRank(itemChain, bar.ProviderID)
				row, rowErr := p.rowFor(bar, req, requestKlineRouteID(p, req.Frequency), rank)
				if rowErr != nil {
					results[index] = failureResult(item, domain.ItemOutcomeInvalid, "normalize", rowErr)
					return
				}
				itemRows = append(itemRows, row)
			}
			if len(itemRows) == 0 {
				p.observeFeed(req, selectedProvider, "kline", "invalid")
				results[index] = failureResult(item, domain.ItemOutcomeInvalid, "coverage", fmt.Errorf("provider returned no bars inside the requested coverage"))
				return
			}
			result := "success"
			if providerRank(itemChain, selectedProvider) > 1 {
				result = "fallback"
			}
			p.observeFeed(req, selectedProvider, "kline", result)
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
			err = storage.UpsertFieldsWithSource(ctx, rows, sourceEventID(req))
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

func fetchKlinesFromChain(ctx context.Context, session *marketdata.RouterSession, req marketdata.KlineRequest, chain []string, startIndex int) ([]marketdata.NormalizedKline, string, int, error) {
	var lastErr error
	lastProvider := "none"
	if len(chain) == 0 {
		return nil, lastProvider, 0, fmt.Errorf("kline candidate chain is empty")
	}
	if strings.TrimSpace(req.SourceID) != "" && len(chain) > 1 {
		// A source-bound Timer must never cross to another Provider merely
		// because the first bound source failed. Route fallback is only valid
		// among endpoints belonging to that same SourceKey.
		chain = chain[:1]
	}
	startIndex %= len(chain)
	if startIndex < 0 {
		startIndex += len(chain)
	}
	maxAttempts := len(chain)
	if maxAttempts > klineProviderAttemptBudget {
		maxAttempts = klineProviderAttemptBudget
	}
	attempts := 0
	for index := 0; index < maxAttempts; index++ {
		chainIndex := (startIndex + index) % len(chain)
		provider := chain[chainIndex]
		lastProvider = provider
		attemptCtx := ctx
		cancel := func() {}
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			attemptsLeft := maxAttempts - index
			if remaining <= 0 {
				return nil, provider, (startIndex + attempts) % len(chain), ctx.Err()
			}
			attemptCtx, cancel = context.WithTimeout(ctx, remaining/time.Duration(attemptsLeft))
		}
		rows, err := session.FetchKlines(attemptCtx, req, []string{provider})
		cancel()
		if err == nil {
			spec, specErr := session.KlineSpec(provider)
			if specErr != nil {
				lastErr = specErr
				attempts++
				continue
			}
			if coverageErr := spec.History.ValidateCoverage(rows, req.StartTime); coverageErr != nil {
				lastErr = coverageErr
				attempts++
				continue
			}
			if !hasRowsWithinCoverage(rows, req.StartTime, req.EndTime) {
				lastErr = fmt.Errorf("%w: provider %s returned no bars inside requested interval", marketdata.ErrHistoryCoverage, provider)
				attempts++
				continue
			}
			return rows, provider, startIndex, nil
		}
		attempts++
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
			return nil, provider, (startIndex + attempts) % len(chain), err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("kline provider chain exhausted")
	}
	return nil, lastProvider, (startIndex + attempts) % len(chain), lastErr
}

func stockSourceForSubject(sources []stockCNSource, routeVersion, subject string) (stockCNSource, bool) {
	totalWeight := 0
	for _, source := range sources {
		totalWeight += source.Weight
	}
	if totalWeight <= 0 || len(sources) == 0 {
		return stockCNSource{}, false
	}
	selected := weightedSourceBucket(routeVersion, subject, sources, totalWeight)
	return selected, selected.Provider != "" && selected.SourceID != ""
}

func bindKlineSource(rows []marketdata.NormalizedKline, providerID, sourceID string) error {
	if strings.TrimSpace(sourceID) == "" {
		return nil
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	for index := range rows {
		if providerID != "" && !strings.EqualFold(strings.TrimSpace(rows[index].ProviderID), providerID) {
			return fmt.Errorf("bar[%d] provider_id %q differs from bound provider %q", index, rows[index].ProviderID, providerID)
		}
		if strings.TrimSpace(rows[index].SourceID) == "" {
			rows[index].SourceID = sourceID
		} else if sourceID != "" && !strings.EqualFold(strings.TrimSpace(rows[index].SourceID), sourceID) {
			return fmt.Errorf("bar[%d] source_id %q differs from bound source %q", index, rows[index].SourceID, sourceID)
		}
	}
	return nil
}

func hasRowsWithinCoverage(rows []marketdata.NormalizedKline, start, end time.Time) bool {
	if start.IsZero() && end.IsZero() {
		return len(rows) > 0
	}
	for _, row := range rows {
		if !start.IsZero() && row.BarStart.Before(start) {
			continue
		}
		if !end.IsZero() && !row.BarStart.Before(end) {
			continue
		}
		return true
	}
	return false
}

func (p *KlinePipeline) observeFeed(req Request, providerID, feedKind, result string) {
	if p == nil || p.Metrics == nil {
		return
	}
	p.Metrics.ObserveFeedResult(FeedMetric{
		MarketID: firstNonEmptyString(p.MarketID, req.SpaceID), RouteID: requestKlineRouteID(p, req.Frequency),
		ProviderID: providerID, SourceID: firstNonEmptyString(p.SourceID, req.SourceID),
		InstrumentType: string(p.InstrumentType), Frequency: req.Frequency, FeedKind: feedKind,
		BatchKind: string(req.BatchKind), Result: result, SourceKind: "provider", Transport: "https",
		SCFRegion: req.Region, EgressScope: "scf-public", Rows: 0, FallbackRank: providerRank([]string{providerID}, providerID),
		ErrorKind: result, CalendarID: firstNonEmptyString(req.SpaceID, "none"),
		GroupID: req.GroupID, GroupCount: req.GroupCount,
	})
}

func requestKlineRouteID(p *KlinePipeline, frequency string) string {
	if p == nil {
		return StockCNRouteID
	}
	if strings.EqualFold(strings.TrimSpace(firstNonEmptyString(p.MarketID, p.SpaceID)), "crypto") {
		product := "spot"
		if p.ProductType == marketdata.ProductSwap || p.InstrumentType == marketdata.InstrumentSwap {
			product = "swap"
		}
		if parsed, err := marketdata.ParseFrequency(frequency); err == nil {
			frequency = string(parsed)
		} else {
			frequency = strings.ToLower(strings.TrimSpace(frequency))
		}
		return "binance_" + product + "_kline_" + frequency
	}
	return firstNonEmptyString(p.RouteID, StockCNRouteID)
}

func metricKlineResult(err error) string {
	if err == nil {
		return "success"
	}
	switch classifyError(err) {
	case domain.ItemOutcomeHTTP429:
		return "http_429"
	case domain.ItemOutcomeHTTP5xx:
		return "http_5xx"
	case domain.ItemOutcomeNetworkError:
		return "timeout"
	case domain.ItemOutcomeInvalid:
		return "invalid"
	default:
		return "no_candidate"
	}
}

func sourceEventID(req Request) string {
	if strings.TrimSpace(req.SyncPointID) != "" {
		return strings.TrimSpace(req.SyncPointID)
	}
	if len(req.Items) == 1 && strings.TrimSpace(req.Items[0].SourceEventID) != "" {
		return strings.TrimSpace(req.Items[0].SourceEventID)
	}
	return req.BatchID
}

func stockProviderExchange(subjectID string) marketdata.ExchangeID {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(subjectID)), ".", 2)
	if len(parts) != 2 {
		return ""
	}
	switch parts[1] {
	case "XSHG", "XSHE", "XBSE":
		return marketdata.ExchangeID(parts[1])
	default:
		return ""
	}
}

func (p *KlinePipeline) rowFor(bar marketdata.NormalizedKline, req Request, routeID string, routeRank int) (*storagepb.RowFieldUpsert, error) {
	if (p.MarketID == StockCNSpaceID || req.SpaceID == StockCNSpaceID) &&
		(p.InstrumentType == "" || p.InstrumentType == marketdata.InstrumentEquity) {
		return stockKlineRow(bar, routeID, routeRank)
	}
	if err := marketdata.ValidateNormalizedKline(bar); err != nil {
		return nil, err
	}
	seriesTag := strings.TrimSpace(p.SeriesTag)
	if seriesTag == "" {
		seriesTag = "venue:" + strings.ToLower(strings.TrimSpace(bar.ProviderID))
	}
	fields := []*storagepb.FieldValue{
		doubleValue("open", bar.Open), doubleValue("high", bar.High), doubleValue("low", bar.Low), doubleValue("close", bar.Close),
		doubleValue("volume", bar.VolumeShares),
	}
	if strings.EqualFold(firstNonEmptyString(p.MarketID, req.SpaceID), "crypto") {
		fields = append(fields, doubleValue("quote_volume", bar.AmountCNY), intValue("trade_num", bar.TradeCount))
	} else {
		fields = append(fields, doubleValue("amount", bar.AmountCNY))
		if p.InstrumentType == marketdata.InstrumentIndex {
			fields = append(fields, stringValue("index_code", bar.ProviderSymbol))
		}
	}
	fields = append(fields,
		stringValue("provider_id", bar.ProviderID), stringValue("source_id", bar.SourceID),
		stringValue("provider_symbol", bar.ProviderSymbol),
	)
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{SpaceId: req.SpaceID, DatasetId: req.DatasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
			SubjectId: bar.SubjectID, Freq: bar.Frequency, DataTime: bar.BarStart.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag,
		}}},
		Fields: fields,
	}, nil
}

func stockCNShouldCollectMinute(calendar *stockmarket.Calendar, now time.Time, settleDelay time.Duration) (bool, error) {
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
	if settleDelay < 0 {
		settleDelay = 0
	}
	target := now.UTC().Add(-settleDelay).Truncate(time.Minute).Add(-time.Minute)
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
	amountQuality := "reported"
	if bar.AmountEstimated {
		amountQuality = "estimated_close_x_volume"
	}
	fields := []*storagepb.FieldValue{
		doubleValue("open", bar.Open), doubleValue("high", bar.High), doubleValue("low", bar.Low), doubleValue("close", bar.Close),
		doubleValue("volume", bar.VolumeShares), doubleValue("amount", bar.AmountCNY),
		timeValue("trade_date", tradeDateUTC(bar.BarStart)), timeValue("close_time", bar.BarEnd),
		stringValue("volume_unit", "shares"), stringValue("amount_unit", amountUnit(bar)),
		stringValue("provider_symbol", bar.ProviderSymbol), timeValue("provider_timestamp", bar.ProviderTimestamp),
		timeValue("fetched_at", bar.FetchedAt), stringValue("request_id", bar.RequestID),
		stringValue("route_id", routeID), intValue("route_rank", int64(routeRank)),
		stringValue("source_provider", bar.ProviderID),
		stringValue("quality_status", qualityStatus), stringValue("amount_quality", amountQuality),
	}
	if strings.TrimSpace(bar.SourceID) != "" {
		fields = append(fields, stringValue("provider_id", bar.ProviderID), stringValue("source_id", bar.SourceID))
	}
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{
			SpaceId: StockCNSpaceID, DatasetId: StockCNDatasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: bar.SubjectID, Freq: "1m", DataTime: bar.BarStart.UTC().Format(time.RFC3339Nano), SeriesTag: "",
			}},
		},
		Fields: fields,
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
