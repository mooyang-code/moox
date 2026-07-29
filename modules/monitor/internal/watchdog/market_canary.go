package watchdog

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"trpc.group/trpc-go/trpc-go/client"
)

type MarketCanaryConfig struct {
	SpaceID, DatasetID, SubjectID, Frequency string
	Freshness                                time.Duration
	ReturnThreshold, VolumeRatioThreshold    float64
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

type marketBar struct {
	DataTime time.Time
	Close    float64
	Volume   float64
}

var (
	sensitiveStorageKeyPattern = regexp.MustCompile(
		`(?i)(token|secret|password|credential|api[_-]?key|app[_-]?key)\s*[:=]`,
	)
	absoluteStoragePathPattern = regexp.MustCompile(`(?i)(/[a-z0-9._-]+){2,}|[a-z]:[\\/]`)
)

func MarketCanaryCheckID(config MarketCanaryConfig) string {
	return strings.Join([]string{"market_canary", config.DatasetID, config.SubjectID, config.Frequency}, ":")
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
		config.Freshness <= 0 || config.ReturnThreshold <= 0 || config.VolumeRatioThreshold <= 0 {
		result.ErrorMessage = "invalid_config"
		return result
	}
	startedAt := time.Now()
	rsp, err := c.Reader.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo: c.AuthInfo,
		SpaceId:  config.SpaceID, DatasetId: config.DatasetID,
		Keys: []*storagepb.TimeSeriesKey{{
			SpaceId: config.SpaceID, DatasetId: config.DatasetID,
			SubjectId: config.SubjectID, Freq: config.Frequency,
		}},
		Order:       storagepb.SortOrder_SORT_ORDER_DESC,
		ColumnNames: []string{"close", "volume"},
		Page:        &storagepb.Page{Page: 1, Size: 2},
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
	bars, err := decodeMarketBars(rsp.GetRows())
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
	volumeRatio := current.Volume / math.Max(previous.Volume, 1e-12)
	if priceReturn >= config.ReturnThreshold || volumeRatio >= config.VolumeRatioThreshold {
		result.ErrorMessage = "threshold_exceeded"
		return result
	}
	result.Success = true
	result.Connected = true
	result.Status = domain.CheckStatusOK
	return result
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

func decodeMarketBars(rows []*storagepb.TimeSeriesRow) ([]marketBar, error) {
	bars := make([]marketBar, 0, len(rows))
	for _, row := range rows {
		if row.GetKey() == nil {
			return nil, fmt.Errorf("missing_row_key")
		}
		dataTime, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil {
			return nil, fmt.Errorf("invalid_data_time")
		}
		var closeValue, volumeValue float64
		var haveClose, haveVolume bool
		for _, field := range row.GetFields() {
			if field.GetValue() == nil {
				continue
			}
			switch field.GetFieldId() {
			case "close":
				closeValue, haveClose = field.GetValue().GetDoubleValue(), true
			case "volume":
				volumeValue, haveVolume = field.GetValue().GetDoubleValue(), true
			}
		}
		if !haveClose || !haveVolume || closeValue <= 0 || volumeValue < 0 ||
			math.IsNaN(closeValue) || math.IsNaN(volumeValue) || math.IsInf(closeValue, 0) || math.IsInf(volumeValue, 0) {
			return nil, fmt.Errorf("invalid_close_or_volume")
		}
		bars = append(bars, marketBar{DataTime: dataTime.UTC(), Close: closeValue, Volume: volumeValue})
	}
	return bars, nil
}
