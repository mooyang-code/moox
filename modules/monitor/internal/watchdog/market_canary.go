package watchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/commonpb"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/marketcalendar"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/client"
)

// MarketCanaryConfig 是一个行情探针。MarketID 决定按哪个市场的时钟切分 K 线，为空时按空间 ID（空间即市场）；
// SettleDelay 起的几项只对交易时段市场（A 股）生效。
type MarketCanaryConfig struct {
	SpaceID, DatasetID, SubjectID, Frequency string
	SeriesTag                                *string
	Freshness                                time.Duration
	ReturnThreshold                          float64
	MarketID                                 string
	SettleDelay                              time.Duration
	PostCloseDelay                           time.Duration
	CalendarWarningLead                      time.Duration
	ClosedBarCount                           int
	ClosedBarMinCoverage                     float64
	EligibleKlineProviders                   []string
}

type TimeSeriesReader interface {
	ReadRows(context.Context, *storagepb.PrimaryReadRowsReq, ...client.Option) (*storagepb.PrimaryReadRowsRsp, error)
}

type MarketCanary struct {
	Reader   TimeSeriesReader
	AuthInfo *commonpb.AuthInfo
	Config   MarketCanaryConfig
	Now      func() time.Time
}

// ErrStorageUnavailable means the monitor could not reach Primary during its
// startup probe. This is intentionally non-fatal: Primary may be starting at
// the same time as Monitor and the periodic canary will retry it.
var ErrStorageUnavailable = errors.New("storage unavailable")

// StorageAuthError is returned when Primary explicitly rejects the monitor's
// authentication. Unlike a network failure, continuing to start would leave
// a permanently noisy canary until someone restarts Monitor with the shared
// secret, so bootstrap treats this error as fatal.
type StorageAuthError struct {
	Code   int32
	Detail string
}

func (e *StorageAuthError) Error() string {
	if e == nil {
		return "storage primary authentication rejected"
	}
	if e.Detail == "" {
		return fmt.Sprintf("storage primary authentication rejected (code=%d)", e.Code)
	}
	return "storage primary authentication rejected: " + e.Detail
}

func IsStorageAuthError(err error) bool {
	var target *StorageAuthError
	return errors.As(err, &target)
}

type marketBar struct {
	DataTime       time.Time
	Open, High     float64
	Low, Close     float64
	Volume, Amount float64
	SourceProvider string
}

// marketCanaryThresholdDiagnostic is persisted in CheckResult.BodyExcerpt so
// the alerting layer can include the compared values without adding monitor
// schema columns or coupling alert formatting to the watchdog package.
type marketCanaryThresholdDiagnostic struct {
	Type            string  `json:"type"`
	PreviousTime    string  `json:"previous_time"`
	CurrentTime     string  `json:"current_time"`
	PreviousClose   float64 `json:"previous_close"`
	CurrentClose    float64 `json:"current_close"`
	PriceReturn     float64 `json:"price_return"`
	ReturnThreshold float64 `json:"return_threshold"`
}

type closedBarCoverageDiagnostic struct {
	Type     string  `json:"type"`
	Expected int     `json:"expected"`
	Actual   int     `json:"actual"`
	Coverage float64 `json:"coverage"`
	Minimum  float64 `json:"minimum"`
}

var (
	sensitiveStorageKeyPattern = regexp.MustCompile(
		`(?i)(token|secret|password|credential|api[_-]?key|app[_-]?key)\s*[:=]`,
	)
	absoluteStoragePathPattern = regexp.MustCompile(`(?i)(/[a-z0-9._-]+){2,}|[a-z]:[\\/]`)
)

func MarketCanaryCheckID(config MarketCanaryConfig) string {
	tag := "<missing>"
	if config.SeriesTag != nil {
		tag = *config.SeriesTag
	}
	return strings.Join([]string{"market_canary", config.DatasetID, config.SubjectID, config.Frequency, tag}, ":")
}

func MarketCanaryTarget(config MarketCanaryConfig) string {
	tag := "<missing>"
	if config.SeriesTag != nil {
		tag = *config.SeriesTag
	}
	return strings.Join([]string{config.SpaceID, config.DatasetID, config.SubjectID, config.Frequency, tag}, "/")
}

func (c MarketCanary) Run(ctx context.Context) domain.CheckResult {
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	config := c.Config
	result := domain.CheckResult{
		ResultID:   domain.ScopedResultID(config.SpaceID, MarketCanaryCheckID(config), now),
		SpaceID:    config.SpaceID,
		CheckID:    MarketCanaryCheckID(config),
		InstanceID: "monitor",
		Status:     domain.CheckStatusDown,
		CheckedAt:  now,
		CreatedAt:  now,
	}
	if c.Reader == nil || strings.TrimSpace(config.SpaceID) == "" || strings.TrimSpace(config.DatasetID) == "" ||
		strings.TrimSpace(config.SubjectID) == "" || strings.TrimSpace(config.Frequency) == "" ||
		config.SeriesTag == nil ||
		config.Freshness <= 0 || config.ReturnThreshold <= 0 {
		result.ErrorMessage = "invalid_config"
		return result
	}
	clock, err := newMarketCanaryClock(config.market(), config.Frequency)
	if err != nil {
		result.ErrorMessage = "invalid_config"
		if errors.Is(err, barclock.ErrNoCalendar) {
			result.ErrorMessage = "calendar_unavailable"
		}
		return result
	}
	if clock.session {
		return c.runSession(ctx, result, now, config, clock)
	}
	return c.runContinuous(ctx, result, now, config, clock)
}

// market 是探针所在的市场：没有单独配置时空间 ID 即市场 ID。
func (config MarketCanaryConfig) market() string {
	if market := strings.TrimSpace(config.MarketID); market != "" {
		return market
	}
	return strings.TrimSpace(config.SpaceID)
}

// marketCanaryClock 是探针用的市场时钟：哪些 K 线应已收盘统一由 barclock 回答，
// 连续市场与交易时段市场只在“取到这些桶之后怎么判断”上不同。
type marketCanaryClock struct {
	clock barclock.Clock
	// frequency 是存储里的规范频率。
	frequency string
	// session 表示交易时段市场（A 股）；coverageEnd 是交易日历最后一天的次日零点（市场时区）。
	session     bool
	coverageEnd time.Time
}

// ValidateMarketCanary 检查探针能否按市场时钟切分 K 线，交易时段市场还必须配置可用的 K 线来源；
// 供配置校验在启动时拦下无效的探针。
func ValidateMarketCanary(config MarketCanaryConfig) error {
	clock, err := newMarketCanaryClock(config.market(), config.Frequency)
	if err != nil {
		return err
	}
	if clock.session && len(config.EligibleKlineProviders) == 0 {
		return errors.New("交易时段市场的探针必须配置 eligible_kline_providers")
	}
	return nil
}

// newMarketCanaryClock 构造市场某个频率的时钟；市场没有在 barclock 登记时报错。
func newMarketCanaryClock(marketID, frequency string) (marketCanaryClock, error) {
	parsed, err := frequencypkg.Parse(frequency)
	if err != nil {
		return marketCanaryClock{}, fmt.Errorf("频率 %q 无效：%w", frequency, err)
	}
	market, err := barclock.MarketFor(marketID)
	if err != nil {
		return marketCanaryClock{}, err
	}
	clock, err := market.Clock(parsed.Duration())
	if err != nil {
		return marketCanaryClock{}, fmt.Errorf("市场 %s 不能按 %s 切分 K 线：%w", marketID, parsed, err)
	}
	out := marketCanaryClock{clock: clock, frequency: string(parsed)}
	if market.Alignment != barclock.AlignmentSession {
		return out, nil
	}
	calendar, err := marketcalendar.Load(market.CalendarID)
	if err != nil {
		return marketCanaryClock{}, fmt.Errorf("%w：%v", barclock.ErrNoCalendar, err)
	}
	location, err := time.LoadLocation(market.Timezone)
	if err != nil {
		return marketCanaryClock{}, fmt.Errorf("%w：时区 %q 无效", barclock.ErrNoCalendar, market.Timezone)
	}
	last := calendar.LastDate()
	out.session = true
	out.coverageEnd = time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, location)
	return out, nil
}

// runContinuous 检查 24 小时连续市场：最近一根已收盘 K 线足够新，且与上一根的收盘价波动不超过阈值。
func (c MarketCanary) runContinuous(ctx context.Context, result domain.CheckResult, now time.Time, config MarketCanaryConfig, clock marketCanaryClock) domain.CheckResult {
	candidateCount, err := marketCanaryCandidateCount(config.Freshness, clock.clock.Period())
	if err != nil {
		result.ErrorMessage = "invalid_config"
		return result
	}
	buckets, err := clock.clock.ClosedBefore(now, candidateCount)
	if err != nil {
		result.ErrorMessage = "invalid_config"
		return result
	}
	startedAt := time.Now()
	rsp, err := c.Reader.ReadRows(ctx, &storagepb.PrimaryReadRowsReq{
		AuthInfo:    c.AuthInfo,
		Keys:        recentMarketCanaryKeys(config, clock.frequency, bucketKeys(buckets)),
		ColumnNames: []string{"close"},
	})
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		result.ErrorMessage = "storage_unreachable"
		return result
	}
	if rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != 0 {
		result.ErrorMessage = storageRejectionError(rsp.GetRetInfo())
		return result
	}
	bars, err := decodeMarketBars(timeSeriesRows(rsp.GetRows()), *config.SeriesTag)
	if err != nil {
		result.ErrorMessage = err.Error()
		return result
	}
	if len(bars) < 2 {
		result.ErrorMessage = "insufficient_closed_bars"
		return result
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].DataTime.Before(bars[j].DataTime) })
	previous, current := bars[len(bars)-2], bars[len(bars)-1]
	if age := now.Sub(current.DataTime); age < 0 || age > config.Freshness {
		result.ErrorMessage = "stale_watermark"
		return result
	}
	priceReturn := math.Abs(current.Close/previous.Close - 1)
	if priceReturn >= config.ReturnThreshold {
		result.ErrorMessage = fmt.Sprintf(
			"相邻K线收盘价从 %s 变为 %s，波动 %.2f%%，超过 %.2f%% 阈值",
			strconv.FormatFloat(previous.Close, 'f', -1, 64),
			strconv.FormatFloat(current.Close, 'f', -1, 64),
			priceReturn*100,
			config.ReturnThreshold*100,
		)
		result.BodyExcerpt = marketCanaryThresholdDetails(previous, current, priceReturn, config)
		return result
	}
	result.Success = true
	result.Connected = true
	result.Status = domain.CheckStatusOK
	return result
}

// runSession 检查交易时段市场（A 股）：最近 ClosedBarCount 根已收盘 K 线的覆盖率、OHLCV 与来源。
// 休市时间（午休、收盘后、非交易日、开盘前）不读存储，直接报空闲健康。
func (c MarketCanary) runSession(ctx context.Context, result domain.CheckResult, now time.Time, config MarketCanaryConfig, clock marketCanaryClock) domain.CheckResult {
	if config.SettleDelay < 0 || config.PostCloseDelay < 0 || config.CalendarWarningLead <= 0 || config.ClosedBarCount <= 0 ||
		config.ClosedBarMinCoverage <= 0 || config.ClosedBarMinCoverage > 1 {
		result.ErrorMessage = "invalid_config"
		return result
	}
	if len(config.EligibleKlineProviders) == 0 {
		result.ErrorMessage = "no_eligible_kline_feed"
		return result
	}
	if !now.Before(clock.coverageEnd) {
		result.ErrorMessage = "calendar_expired"
		return result
	}
	if clock.coverageEnd.Sub(now) < config.CalendarWarningLead {
		result.ErrorMessage = "calendar_expiring"
		return result
	}
	expected, active, err := sessionExpectedBuckets(clock.clock, now, config.SettleDelay, config.PostCloseDelay, config.Freshness, config.ClosedBarCount)
	if err != nil {
		result.ErrorMessage = "invalid_config"
		if errors.Is(err, barclock.ErrOutOfCoverage) {
			result.ErrorMessage = "calendar_expired"
		}
		return result
	}
	if !active {
		return healthyIdleResult(result)
	}
	startedAt := time.Now()
	rsp, err := c.Reader.ReadRows(ctx, &storagepb.PrimaryReadRowsReq{
		AuthInfo:    c.AuthInfo,
		Keys:        recentMarketCanaryKeys(config, clock.frequency, bucketKeys(expected)),
		ColumnNames: []string{"open", "high", "low", "close", "volume", "amount", "source_provider"},
	})
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		result.ErrorMessage = "storage_unreachable"
		return result
	}
	if rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != 0 {
		result.ErrorMessage = storageRejectionError(rsp.GetRetInfo())
		return result
	}
	bars, err := decodeStockMarketBars(timeSeriesRows(rsp.GetRows()), *config.SeriesTag)
	if err != nil {
		result.ErrorMessage = err.Error()
		return result
	}
	byTime := make(map[int64]marketBar, len(bars))
	eligible := make(map[string]struct{}, len(config.EligibleKlineProviders))
	for _, provider := range config.EligibleKlineProviders {
		eligible[strings.ToLower(strings.TrimSpace(provider))] = struct{}{}
	}
	for _, bar := range bars {
		if _, ok := eligible[strings.ToLower(bar.SourceProvider)]; !ok {
			result.ErrorMessage = "source_provider_not_eligible"
			return result
		}
		byTime[bar.DataTime.Unix()] = bar
	}
	present := 0
	for _, want := range expected {
		if _, ok := byTime[want.Key.Unix()]; ok {
			present++
		}
	}
	if present == 0 {
		result.ErrorMessage = "no_closed_bar_data"
		return result
	}
	coverage := float64(present) / float64(len(expected))
	if coverage < config.ClosedBarMinCoverage {
		result.ErrorMessage = "closed_bar_coverage_below_threshold"
		diagnostic, _ := json.Marshal(closedBarCoverageDiagnostic{
			Type: "closed_bar_coverage", Expected: len(expected), Actual: present,
			Coverage: coverage, Minimum: config.ClosedBarMinCoverage,
		})
		result.BodyExcerpt = string(diagnostic)
		return result
	}
	result.Success, result.Connected, result.Status = true, true, domain.CheckStatusOK
	return result
}

// sessionExpectedBuckets 返回交易时段市场此刻应已写入的最近 count 根 K 线；active 为假表示休市空闲，不检查。
//
// 收盘时刻晚于 now-settleDelay 的桶还没到写入时间，不算已收盘。最近一根已收盘的桶之后紧接着下一根（交易时段内）
// 时一直检查；它是某个交易时段的最后一根（午休前、收盘）时，只在其收盘后 [postCloseDelay, postCloseDelay+window]
// 内检查这几根，之后到下一时段第一根收盘前都空闲。非交易日、开盘前的最近一根是上一交易日的最后一根，早已出了检查窗口。
func sessionExpectedBuckets(clock barclock.Clock, now time.Time, settleDelay, postCloseDelay, window time.Duration, count int) ([]barclock.Bucket, bool, error) {
	at := now.Add(-settleDelay)
	latest, err := clock.LatestClosed(at)
	if err != nil {
		return nil, false, err
	}
	// 取下一根出错只会是日历到头：按时段结束处理。
	if next, nextErr := clock.Next(latest); nextErr != nil || !next.Start.Equal(latest.End) {
		sinceClose := now.Sub(latest.End)
		if sinceClose < postCloseDelay || sinceClose > postCloseDelay+window {
			return nil, false, nil
		}
	}
	expected, err := clock.ClosedBefore(at, count)
	if err != nil {
		return nil, false, err
	}
	if len(expected) < count {
		return nil, false, nil
	}
	return expected, true, nil
}

func bucketKeys(buckets []barclock.Bucket) []time.Time {
	keys := make([]time.Time, len(buckets))
	for i, bucket := range buckets {
		keys[i] = bucket.Key
	}
	return keys
}

func healthyIdleResult(result domain.CheckResult) domain.CheckResult {
	result.Success, result.Connected, result.Status = true, true, domain.CheckStatusOK
	result.BodyExcerpt = `{"state":"idle"}`
	return result
}

// ProbeStorageAuth performs the smallest possible Primary read needed to
// validate credentials. It deliberately does not inspect returned rows, so a
// newly started collector or an empty dataset does not make Monitor fail its
// startup. Only an explicit auth/permission rejection is fatal to bootstrap.
func (c MarketCanary) ProbeStorageAuth(ctx context.Context) error {
	config := c.Config
	if c.Reader == nil || strings.TrimSpace(config.SpaceID) == "" || strings.TrimSpace(config.DatasetID) == "" ||
		strings.TrimSpace(config.SubjectID) == "" || strings.TrimSpace(config.Frequency) == "" || config.SeriesTag == nil {
		return fmt.Errorf("invalid market canary configuration")
	}
	frequency, err := frequencypkg.Normalize(config.Frequency)
	if err != nil {
		return fmt.Errorf("invalid market canary frequency: %w", err)
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	times, err := report.RecentDatasetTimes(frequency, now, 1)
	if err != nil {
		return fmt.Errorf("invalid market canary time window: %w", err)
	}
	rsp, err := c.Reader.ReadRows(ctx, &storagepb.PrimaryReadRowsReq{
		AuthInfo:    c.AuthInfo,
		Keys:        recentMarketCanaryKeys(config, frequency, times),
		ColumnNames: []string{"close"},
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return fmt.Errorf("storage probe returned no response")
	}
	retInfo := rsp.GetRetInfo()
	if retInfo.GetCode() == 0 {
		return nil
	}
	message := strings.ToLower(strings.TrimSpace(retInfo.GetMsg()))
	if retInfo.GetCode() == commonpb.ErrorCode_NO_PERMISSION || strings.Contains(message, "auth") {
		return &StorageAuthError{Code: int32(retInfo.GetCode()), Detail: storageRejectionError(retInfo)}
	}
	return fmt.Errorf("storage probe rejected: %s", storageRejectionError(retInfo))
}

func marketCanaryThresholdDetails(previous, current marketBar, priceReturn float64, config MarketCanaryConfig) string {
	diagnostic := marketCanaryThresholdDiagnostic{
		Type:            "market_canary_threshold",
		PreviousTime:    previous.DataTime.UTC().Format(time.RFC3339Nano),
		CurrentTime:     current.DataTime.UTC().Format(time.RFC3339Nano),
		PreviousClose:   previous.Close,
		CurrentClose:    current.Close,
		PriceReturn:     priceReturn,
		ReturnThreshold: config.ReturnThreshold,
	}
	raw, err := json.Marshal(diagnostic)
	if err != nil {
		return ""
	}
	return string(raw)
}

func recentMarketCanaryKeys(config MarketCanaryConfig, frequency string, times []time.Time) []*storagepb.RowKey {
	keys := make([]*storagepb.RowKey, 0, len(times))
	for _, at := range times {
		keys = append(keys, &storagepb.RowKey{
			SpaceId: config.SpaceID, DatasetId: config.DatasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: config.SubjectID, Freq: frequency,
				DataTime: at.Format(time.RFC3339Nano), SeriesTag: *config.SeriesTag,
			}},
		})
	}
	return keys
}

// timeSeriesRows 把精确读取返回的行（只含存在的行）转成解码用的时序行。
func timeSeriesRows(rows []*storagepb.RowFieldValues) []*storagepb.TimeSeriesRow {
	out := make([]*storagepb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		key := row.GetKey().GetTimeSeries()
		if key == nil {
			out = append(out, &storagepb.TimeSeriesRow{Fields: row.GetFields()})
			continue
		}
		out = append(out, &storagepb.TimeSeriesRow{
			Key:    &storagepb.TimeSeriesKey{SpaceId: row.GetKey().GetSpaceId(), DatasetId: row.GetKey().GetDatasetId(), SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag()},
			Fields: row.GetFields(),
		})
	}
	return out
}

func marketCanaryCandidateCount(freshness, interval time.Duration) (int, error) {
	const minCandidates, maxCandidates = 24, 512
	count := int((freshness-1)/interval) + 3
	if count < minCandidates {
		count = minCandidates
	}
	if count > maxCandidates {
		return 0, fmt.Errorf("market canary freshness requires more than %d exact keys", maxCandidates)
	}
	return count, nil
}

func storageRejectionError(retInfo *storagepb.RetInfo) string {
	if retInfo == nil {
		return "storage_rejected_query:missing_ret_info"
	}
	rawMessage := strings.TrimSpace(retInfo.GetMsg())
	message := strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t', ':':
			return ' '
		default:
			return r
		}
	}, rawMessage)
	if sensitiveStorageKeyPattern.MatchString(rawMessage) || absoluteStoragePathPattern.MatchString(rawMessage) {
		message = "details_redacted"
	}
	runes := []rune(message)
	if len(runes) > 160 {
		message = string(runes[:160])
	}
	if message == "" {
		return fmt.Sprintf("storage_rejected_query:%d", retInfo.GetCode())
	}
	return fmt.Sprintf("storage_rejected_query:%d:%s", retInfo.GetCode(), message)
}

func decodeMarketBars(rows []*storagepb.TimeSeriesRow, seriesTag string) ([]marketBar, error) {
	bars := make([]marketBar, 0, len(rows))
	for _, row := range rows {
		if row.GetKey() == nil {
			return nil, fmt.Errorf("missing_row_key")
		}
		if row.GetKey().GetSeriesTag() != seriesTag {
			continue
		}
		dataTime, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil {
			return nil, fmt.Errorf("invalid_data_time")
		}
		var closeValue float64
		var haveClose bool
		for _, field := range row.GetFields() {
			if field.GetValue() == nil {
				continue
			}
			switch field.GetFieldId() {
			case "close":
				closeValue, haveClose = field.GetValue().GetDoubleValue(), true
			}
		}
		if !haveClose || closeValue <= 0 || math.IsNaN(closeValue) || math.IsInf(closeValue, 0) {
			return nil, fmt.Errorf("invalid_close")
		}
		bars = append(bars, marketBar{DataTime: dataTime.UTC(), Close: closeValue})
	}
	return bars, nil
}

func decodeStockMarketBars(rows []*storagepb.TimeSeriesRow, seriesTag string) ([]marketBar, error) {
	bars := make([]marketBar, 0, len(rows))
	for _, row := range rows {
		if row.GetKey() == nil {
			return nil, fmt.Errorf("missing_row_key")
		}
		if row.GetKey().GetSeriesTag() != seriesTag {
			continue
		}
		dataTime, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil {
			return nil, fmt.Errorf("invalid_data_time")
		}
		values := make(map[string]*storagepb.TypedValue, len(row.GetFields()))
		for _, field := range row.GetFields() {
			if field.GetValue() == nil {
				continue
			}
			values[field.GetFieldId()] = field.GetValue()
		}
		required := []string{"open", "high", "low", "close", "volume", "amount", "source_provider"}
		for _, field := range required {
			if values[field] == nil {
				if field == "source_provider" {
					return nil, fmt.Errorf("missing_source_provider")
				}
				return nil, fmt.Errorf("invalid_ohlcv")
			}
		}
		bar := marketBar{DataTime: dataTime.UTC(), Open: values["open"].GetDoubleValue(), High: values["high"].GetDoubleValue(), Low: values["low"].GetDoubleValue(), Close: values["close"].GetDoubleValue(), Volume: values["volume"].GetDoubleValue(), Amount: values["amount"].GetDoubleValue(), SourceProvider: strings.TrimSpace(values["source_provider"].GetStringValue())}
		if bar.SourceProvider == "" {
			return nil, fmt.Errorf("missing_source_provider")
		}
		if !validPositive(bar.Open) || !validPositive(bar.High) || !validPositive(bar.Low) || !validPositive(bar.Close) || !validNonNegative(bar.Volume) || !validNonNegative(bar.Amount) || bar.High < math.Max(bar.Open, math.Max(bar.Close, bar.Low)) || bar.Low > math.Min(bar.Open, math.Min(bar.Close, bar.High)) {
			return nil, fmt.Errorf("invalid_ohlcv")
		}
		bars = append(bars, bar)
	}
	return bars, nil
}

func validPositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
