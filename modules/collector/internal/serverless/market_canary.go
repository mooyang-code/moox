package serverless

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/client"
)

type MarketCanaryConfig struct {
	SpaceID              string
	DatasetID            string
	SubjectID            string
	Frequency            string
	SeriesTag            *string
	Freshness            time.Duration
	ReturnThreshold      float64
	VolumeRatioThreshold float64
	AuthInfo             *storagepb.AuthInfo
}

type TimeSeriesReader interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
}

type marketBar struct {
	DataTime time.Time
	Close    float64
	Volume   float64
}

// StorageMarketCanaryCheck uses the real signed Storage PrimaryStore tRPC
// contract. Collector only persists closed K lines, so the latest two stored
// rows are the latest two closed bars.
func StorageMarketCanaryCheck(reader TimeSeriesReader, cfg MarketCanaryConfig) WatchdogCheck {
	return func(ctx context.Context) CheckResult {
		startedAt := time.Now().UTC()
		result := CheckResult{
			CheckID: "market_canary", Kind: "storage", CheckedAt: startedAt,
			Target: marketCanaryTarget(cfg.SpaceID, cfg.DatasetID, cfg.SubjectID, cfg.Frequency, cfg.SeriesTag),
		}
		if reader == nil || strings.TrimSpace(cfg.SpaceID) == "" || strings.TrimSpace(cfg.DatasetID) == "" ||
			strings.TrimSpace(cfg.SubjectID) == "" || strings.TrimSpace(cfg.Frequency) == "" ||
			cfg.SeriesTag == nil ||
			cfg.AuthInfo == nil || strings.TrimSpace(cfg.AuthInfo.GetAppId()) == "" || strings.TrimSpace(cfg.AuthInfo.GetAppKey()) == "" ||
			cfg.Freshness <= 0 || cfg.ReturnThreshold <= 0 || cfg.VolumeRatioThreshold <= 0 {
			result.ErrorCode, result.Error = "invalid_config", "market canary Storage scope and thresholds are required"
			return result
		}
		frequency, err := sources.NormalizeFreq(cfg.Frequency)
		if err != nil {
			result.ErrorCode, result.Error = "invalid_config", "market canary frequency is invalid"
			return result
		}
		interval, err := report.ParseDatasetFrequency(frequency)
		if err != nil {
			result.ErrorCode, result.Error = "invalid_config", "market canary frequency is invalid"
			return result
		}
		candidateCount, err := marketCanaryCandidateCount(cfg.Freshness, interval)
		if err != nil {
			result.ErrorCode, result.Error = "invalid_config", "market canary freshness is too large"
			return result
		}
		candidateTimes, err := report.RecentDatasetTimes(frequency, startedAt, candidateCount)
		if err != nil {
			result.ErrorCode, result.Error = "invalid_config", "market canary frequency is invalid"
			return result
		}
		result.Target = marketCanaryTarget(cfg.SpaceID, cfg.DatasetID, cfg.SubjectID, frequency, cfg.SeriesTag)
		rsp, err := reader.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			AuthInfo: cfg.AuthInfo,
			SpaceId:  cfg.SpaceID, DatasetId: cfg.DatasetID,
			Keys:        recentMarketCanaryKeys(cfg, frequency, candidateTimes),
			ColumnNames: []string{"close", "volume"},
		})
		result.Latency = time.Since(startedAt)
		if err != nil {
			result.ErrorCode, result.Error = "unreachable", "Storage market canary request failed"
			return result
		}
		if rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != 0 {
			result.ErrorCode, result.Error = "storage_error", "Storage rejected market canary query"
			return result
		}
		bars, err := decodeMarketBars(rsp.GetRows(), *cfg.SeriesTag)
		if err != nil {
			result.ErrorCode, result.Error = "decode", err.Error()
			return result
		}
		if len(bars) < 2 {
			result.ErrorCode = "insufficient_closed_bars"
			result.Error = fmt.Sprintf("Storage 查询只返回 %d 根已收盘 K 线，至少需要 2 根", len(bars))
			return result
		}
		sort.Slice(bars, func(i, j int) bool { return bars[i].DataTime.Before(bars[j].DataTime) })
		previous, current := bars[len(bars)-2], bars[len(bars)-1]
		if age := startedAt.Sub(current.DataTime); age < 0 || age > cfg.Freshness {
			result.ErrorCode = "stale_watermark"
			result.Error = fmt.Sprintf(
				"最新已收盘 K 线时间为 %s，距检查时间 %s，超过允许的 %s",
				current.DataTime.Format(time.RFC3339), age.Round(time.Second), cfg.Freshness,
			)
			return result
		}
		priceReturn := math.Abs(current.Close/previous.Close - 1)
		volumeRatio := current.Volume / math.Max(previous.Volume, 1e-12)
		if priceReturn >= cfg.ReturnThreshold || volumeRatio >= cfg.VolumeRatioThreshold {
			result.ErrorCode, result.Error = "threshold_exceeded", "market movement threshold exceeded"
			return result
		}
		result.Success = true
		return result
	}
}

func recentMarketCanaryKeys(cfg MarketCanaryConfig, frequency string, times []time.Time) []*storagepb.TimeSeriesKey {
	keys := make([]*storagepb.TimeSeriesKey, 0, len(times))
	for _, at := range times {
		keys = append(keys, &storagepb.TimeSeriesKey{
			SpaceId: cfg.SpaceID, DatasetId: cfg.DatasetID,
			SubjectId: cfg.SubjectID, Freq: frequency,
			DataTime: at.Format(time.RFC3339Nano), SeriesTag: *cfg.SeriesTag,
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

func decodeMarketBars(rows []*storagepb.TimeSeriesRow, seriesTag string) ([]marketBar, error) {
	bars := make([]marketBar, 0, len(rows))
	for _, row := range rows {
		if row.GetKey() == nil {
			return nil, fmt.Errorf("market canary row key is missing")
		}
		if row.GetKey().GetSeriesTag() != seriesTag {
			continue
		}
		dataTime, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil {
			return nil, fmt.Errorf("market canary data_time is invalid")
		}
		var closeValue, volumeValue float64
		var haveClose, haveVolume bool
		for _, field := range row.GetFields() {
			fieldID := field.GetFieldId()
			if index := strings.LastIndexByte(fieldID, '.'); index >= 0 {
				fieldID = fieldID[index+1:]
			}
			switch fieldID {
			case "close":
				closeValue, haveClose = field.GetValue().GetDoubleValue(), field.GetValue() != nil
			case "volume":
				volumeValue, haveVolume = field.GetValue().GetDoubleValue(), field.GetValue() != nil
			}
		}
		if !haveClose || !haveVolume || closeValue <= 0 || volumeValue < 0 ||
			math.IsNaN(closeValue) || math.IsNaN(volumeValue) || math.IsInf(closeValue, 0) || math.IsInf(volumeValue, 0) {
			return nil, fmt.Errorf("market canary close or volume is invalid")
		}
		bars = append(bars, marketBar{DataTime: dataTime.UTC(), Close: closeValue, Volume: volumeValue})
	}
	return bars, nil
}

func marketCanaryTarget(spaceID, datasetID, subjectID, frequency string, seriesTag *string) string {
	tag := "<missing>"
	if seriesTag != nil {
		tag = *seriesTag
	}
	return strings.Join([]string{spaceID, datasetID, subjectID, frequency, tag}, "/")
}
