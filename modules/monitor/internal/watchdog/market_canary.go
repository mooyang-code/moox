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
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"trpc.group/trpc-go/trpc-go/client"
)

type MarketCanaryConfig struct {
	SpaceID, DatasetID, SubjectID, Frequency string
	SeriesTag                                *string
	Freshness                                time.Duration
	ReturnThreshold                          float64
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
	DataTime time.Time
	Close    float64
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
