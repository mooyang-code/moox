package serverless

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type MarketCanaryConfig struct {
	SpaceID              string
	DatasetID            string
	SubjectID            string
	Frequency            string
	Freshness            time.Duration
	ReturnThreshold      float64
	VolumeRatioThreshold float64
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
			Target: strings.Join([]string{cfg.SpaceID, cfg.DatasetID, cfg.SubjectID, cfg.Frequency}, "/"),
		}
		if reader == nil || strings.TrimSpace(cfg.SpaceID) == "" || strings.TrimSpace(cfg.DatasetID) == "" ||
			strings.TrimSpace(cfg.SubjectID) == "" || strings.TrimSpace(cfg.Frequency) == "" ||
			cfg.Freshness <= 0 || cfg.ReturnThreshold <= 0 || cfg.VolumeRatioThreshold <= 0 {
			result.ErrorCode, result.Error = "invalid_config", "market canary Storage scope and thresholds are required"
			return result
		}
		rsp, err := reader.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			SpaceId: cfg.SpaceID, DatasetId: cfg.DatasetID,
			Keys: []*storagepb.TimeSeriesKey{{
				SpaceId: cfg.SpaceID, DatasetId: cfg.DatasetID,
				SubjectId: cfg.SubjectID, Freq: cfg.Frequency,
			}},
			Order:       storagepb.SortOrder_SORT_ORDER_DESC,
			ColumnNames: []string{"close", "volume"},
			Page:        &storagepb.Page{Page: 1, Size: 2},
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
		bars, err := decodeMarketBars(rsp.GetRows())
		if err != nil {
			result.ErrorCode, result.Error = "decode", err.Error()
			return result
		}
		if len(bars) < 2 {
			result.ErrorCode, result.Error = "insufficient_closed_bars", "fewer than two closed bars"
			return result
		}
		sort.Slice(bars, func(i, j int) bool { return bars[i].DataTime.Before(bars[j].DataTime) })
		previous, current := bars[len(bars)-2], bars[len(bars)-1]
		if age := time.Now().UTC().Sub(current.DataTime); age < 0 || age > cfg.Freshness {
			result.ErrorCode, result.Error = "stale_watermark", "latest closed bar is stale"
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

func decodeMarketBars(rows []*storagepb.TimeSeriesRow) ([]marketBar, error) {
	bars := make([]marketBar, 0, len(rows))
	for _, row := range rows {
		if row.GetKey() == nil {
			return nil, fmt.Errorf("market canary row key is missing")
		}
		dataTime, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil {
			return nil, fmt.Errorf("market canary data_time is invalid")
		}
		var closeValue, volumeValue float64
		var haveClose, haveVolume bool
		for _, field := range row.GetFields() {
			switch field.GetFieldId() {
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
