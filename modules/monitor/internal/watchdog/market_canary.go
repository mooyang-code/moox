package watchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/client"
)

type MarketCanaryConfig struct {
	SpaceID, DatasetID, SubjectID, Frequency string
	SeriesTag                                *string
	Freshness                                time.Duration
	ReturnThreshold                          float64
	MarketID                                 string
	CalendarPath                             string
	SettleDelay                              time.Duration
	CalendarWarningLead                      time.Duration
	ClosedBarCount                           int
	EligibleKlineProviders                   []string
}

type TimeSeriesReader interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
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
		ResultID:   fmt.Sprintf("%s-%d", MarketCanaryCheckID(config), now.UnixNano()),
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
	if strings.EqualFold(strings.TrimSpace(config.MarketID), "stock_cn") || strings.EqualFold(strings.TrimSpace(config.SpaceID), "stock_cn") {
		return c.runStockCN(ctx, result, now)
	}
	storageFrequency, err := canonicalStorageFrequency(config.Frequency)
	if err != nil {
		result.ErrorMessage = "invalid_config"
		return result
	}
	interval, err := report.ParseDatasetFrequency(storageFrequency)
	if err != nil {
		result.ErrorMessage = "invalid_config"
		return result
	}
	candidateCount, err := marketCanaryCandidateCount(config.Freshness, interval)
	if err != nil {
		result.ErrorMessage = "invalid_config"
		return result
	}
	candidateTimes, err := report.RecentDatasetTimes(storageFrequency, now, candidateCount)
	if err != nil {
		result.ErrorMessage = "invalid_config"
		return result
	}
	startedAt := time.Now()
	rsp, err := c.Reader.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo: c.AuthInfo,
		SpaceId:  config.SpaceID, DatasetId: config.DatasetID,
		Keys:        recentMarketCanaryKeys(config, storageFrequency, candidateTimes),
		ColumnNames: []string{"close"},
	}, client.WithFilter(trpcretry.ReadOnly()))
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		result.ErrorMessage = "storage_unreachable"
		return result
	}
	if rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != 0 {
		result.ErrorMessage = storageRejectionError(rsp.GetRetInfo())
		return result
	}
	bars, err := decodeMarketBars(rsp.GetRows(), *config.SeriesTag)
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

type stockCalendarFile struct {
	Timezone      string         `yaml:"timezone"`
	CoverageStart string         `yaml:"coverage_start"`
	CoverageEnd   string         `yaml:"coverage_end"`
	Sessions      []stockSession `yaml:"sessions"`
	ClosedDates   []string       `yaml:"closed_dates"`
	OpenDates     []string       `yaml:"exceptional_open_dates"`
}

type stockSession struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

type stockCalendar struct {
	location       *time.Location
	coverageStart  time.Time
	coverageEnd    time.Time
	sessions       []stockSession
	closed, opened map[string]struct{}
}

func (c MarketCanary) runStockCN(ctx context.Context, result domain.CheckResult, now time.Time) domain.CheckResult {
	config := c.Config
	if strings.TrimSpace(config.CalendarPath) == "" || config.SettleDelay < 0 || config.CalendarWarningLead <= 0 || config.ClosedBarCount <= 0 {
		result.ErrorMessage = "invalid_config"
		return result
	}
	if len(config.EligibleKlineProviders) == 0 {
		result.ErrorMessage = "no_eligible_kline_feed"
		return result
	}
	calendar, err := loadStockCalendar(config.CalendarPath)
	if err != nil {
		result.ErrorMessage = "calendar_unavailable"
		return result
	}
	localNow := now.In(calendar.location)
	if localNow.After(calendar.coverageEnd.Add(24 * time.Hour)) {
		result.ErrorMessage = "calendar_expired"
		return result
	}
	if calendar.coverageEnd.Sub(localNow) < config.CalendarWarningLead {
		result.ErrorMessage = "calendar_expiring"
		return result
	}
	if !calendar.isTradingDay(localNow) || !calendar.inSession(localNow) {
		return healthyIdleResult(result)
	}
	expected := calendar.closedBarStarts(localNow, config.SettleDelay, config.ClosedBarCount)
	if len(expected) < config.ClosedBarCount {
		return healthyIdleResult(result)
	}
	startedAt := time.Now()
	rsp, err := c.Reader.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo: c.AuthInfo, SpaceId: config.SpaceID, DatasetId: config.DatasetID,
		Keys:        recentMarketCanaryKeys(config, "1m", expected),
		ColumnNames: []string{"open", "high", "low", "close", "volume", "amount", "source_provider"},
	}, client.WithFilter(trpcretry.ReadOnly()))
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		result.ErrorMessage = "storage_unreachable"
		return result
	}
	if rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != 0 {
		result.ErrorMessage = storageRejectionError(rsp.GetRetInfo())
		return result
	}
	bars, err := decodeStockMarketBars(rsp.GetRows(), *config.SeriesTag)
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
	for _, want := range expected {
		if _, ok := byTime[want.UTC().Unix()]; !ok {
			result.ErrorMessage = "stale_expected_bucket"
			return result
		}
	}
	result.Success, result.Connected, result.Status = true, true, domain.CheckStatusOK
	return result
}

func healthyIdleResult(result domain.CheckResult) domain.CheckResult {
	result.Success, result.Connected, result.Status = true, true, domain.CheckStatusOK
	result.BodyExcerpt = `{"state":"idle"}`
	return result
}

func loadStockCalendar(path string) (*stockCalendar, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file stockCalendarFile
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(strings.TrimSpace(file.Timezone))
	if err != nil {
		return nil, err
	}
	start, err := time.ParseInLocation("2006-01-02", file.CoverageStart, location)
	if err != nil {
		return nil, err
	}
	end, err := time.ParseInLocation("2006-01-02", file.CoverageEnd, location)
	if err != nil {
		return nil, err
	}
	calendar := &stockCalendar{location: location, coverageStart: start, coverageEnd: end, sessions: file.Sessions, closed: map[string]struct{}{}, opened: map[string]struct{}{}}
	for _, day := range file.ClosedDates {
		calendar.closed[day] = struct{}{}
	}
	for _, day := range file.OpenDates {
		calendar.opened[day] = struct{}{}
	}
	return calendar, nil
}

func (c *stockCalendar) isTradingDay(at time.Time) bool {
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, c.location)
	if day.Before(c.coverageStart) || day.After(c.coverageEnd) {
		return false
	}
	date := day.Format("2006-01-02")
	if _, ok := c.closed[date]; ok {
		return false
	}
	if _, ok := c.opened[date]; ok {
		return true
	}
	return day.Weekday() != time.Saturday && day.Weekday() != time.Sunday
}

func (c *stockCalendar) inSession(at time.Time) bool {
	minute := at.Format("15:04")
	for _, session := range c.sessions {
		if minute >= session.Start && minute < session.End {
			return true
		}
	}
	return false
}

func (c *stockCalendar) closedBarStarts(now time.Time, settleDelay time.Duration, count int) []time.Time {
	date := now.Format("2006-01-02")
	all := make([]time.Time, 0, 240)
	for _, session := range c.sessions {
		start, startErr := time.ParseInLocation("2006-01-02 15:04", date+" "+session.Start, c.location)
		end, endErr := time.ParseInLocation("2006-01-02 15:04", date+" "+session.End, c.location)
		if startErr != nil || endErr != nil {
			continue
		}
		for cursor := start; cursor.Before(end); cursor = cursor.Add(time.Minute) {
			if !cursor.Add(time.Minute).Add(settleDelay).After(now) {
				all = append(all, cursor.UTC())
			}
		}
	}
	if len(all) <= count {
		return all
	}
	return all[len(all)-count:]
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
	frequency, err := canonicalStorageFrequency(config.Frequency)
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
	rsp, err := c.Reader.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo: c.AuthInfo,
		SpaceId:  config.SpaceID, DatasetId: config.DatasetID,
		Keys:        recentMarketCanaryKeys(config, frequency, times),
		ColumnNames: []string{"close"},
	}, client.WithFilter(trpcretry.ReadOnly()))
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

func recentMarketCanaryKeys(config MarketCanaryConfig, frequency string, times []time.Time) []*storagepb.TimeSeriesKey {
	keys := make([]*storagepb.TimeSeriesKey, 0, len(times))
	for _, at := range times {
		keys = append(keys, &storagepb.TimeSeriesKey{
			SpaceId: config.SpaceID, DatasetId: config.DatasetID,
			SubjectId: config.SubjectID, Freq: frequency,
			DataTime: at.Format(time.RFC3339Nano), SeriesTag: *config.SeriesTag,
		})
	}
	return keys
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

func canonicalStorageFrequency(frequency string) (string, error) {
	frequency = strings.TrimSpace(frequency)
	if len(frequency) < 2 {
		return "", fmt.Errorf("frequency %q is invalid", frequency)
	}
	count, err := strconv.ParseUint(frequency[:len(frequency)-1], 10, 64)
	if err != nil || count == 0 {
		return "", fmt.Errorf("frequency %q is invalid", frequency)
	}
	switch frequency[len(frequency)-1] {
	case 'h', 'H':
		return frequency[:len(frequency)-1] + "H", nil
	case 'd', 'D':
		return frequency[:len(frequency)-1] + "D", nil
	case 'w', 'W':
		return frequency[:len(frequency)-1] + "W", nil
	case 'y', 'Y':
		return frequency[:len(frequency)-1] + "Y", nil
	case 'm', 'M':
		return frequency, nil
	default:
		return "", fmt.Errorf("frequency %q has an unsupported unit", frequency)
	}
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
			fieldID := field.GetFieldId()
			if index := strings.LastIndexByte(fieldID, '.'); index >= 0 {
				fieldID = fieldID[index+1:]
			}
			switch fieldID {
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
			fieldID := field.GetFieldId()
			if index := strings.LastIndexByte(fieldID, '.'); index >= 0 {
				fieldID = fieldID[index+1:]
			}
			values[fieldID] = field.GetValue()
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
