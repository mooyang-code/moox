package binance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/jobcontext"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

// 产品类型常量
const (
	InstTypeSPOT = "SPOT" // 现货
	InstTypeSWAP = "SWAP" // 永续合约
)

var errKlineNotClosed = errors.New("K线尚未闭合")

// KlineCollector K线数据采集器
type KlineCollector struct {
	client         *binanceapi.Client
	spotAPI        *binanceapi.SpotAPI
	swapAPI        *binanceapi.SwapAPI
	storage        klineStorage
	fetchKlinePage func(context.Context, *sources.CollectParams, *exchange.KlineRequest) ([]*exchange.Kline, error)
	now            func() time.Time
}

type klineStorage interface {
	LatestTimeSeriesTime(context.Context, *storagepb.TimeSeriesKey) (time.Time, bool, error)
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
}

// init 自注册到采集器注册中心
func init() {
	// 创建采集器实例
	client := newConfiguredClient()
	c := &KlineCollector{
		client:  client,
		spotAPI: binanceapi.NewSpotAPI(client),
		swapAPI: binanceapi.NewSwapAPI(client),
	}

	for _, market := range []struct {
		id string
		cn string
	}{
		{id: "spot", cn: "现货"},
		{id: "swap", cn: "永续合约"},
	} {
		err := sources.NewBuilder().
			Source("binance", "币安").
			Market(market.id, market.cn).
			DataType("kline", "K线").
			Description("币安K线数据采集器").
			Collector(c).
			Register()
		if err != nil {
			panic(fmt.Sprintf("注册K线采集器失败: %v", err))
		}
	}
}

// Source 返回数据源标识
func (c *KlineCollector) Source() string {
	return "binance"
}

// DataType 返回数据类型标识
func (c *KlineCollector) DataType() string {
	return "kline"
}

// Collect 执行一次K线采集
func (c *KlineCollector) Collect(ctx context.Context, params *sources.CollectParams) error {
	_, err := c.CollectWithResult(ctx, params)
	return err
}

// CollectWithResult executes one K-line collection and returns write evidence.
func (c *KlineCollector) CollectWithResult(
	ctx context.Context,
	params *sources.CollectParams,
) (sources.CollectResult, error) {
	result := sources.CollectResult{}
	if params == nil {
		return result, fmt.Errorf("K线采集参数不能为空")
	}
	spaceID := strings.TrimSpace(params.SpaceID)
	if spaceID == "" {
		return result, fmt.Errorf("space_id 不能为空")
	}
	datasetID := strings.TrimSpace(params.DatasetID)
	if datasetID == "" {
		return result, fmt.Errorf("dataset_id 不能为空")
	}
	log.InfoContextf(ctx, "K线采集开始: space_id=%s, dataset_id=%s, inst_type=%s, symbol=%s, interval=%s",
		spaceID, datasetID, params.InstType, params.Symbol, params.Interval)

	freq, err := normalizeFreq(params.Interval)
	if err != nil {
		return result, err
	}
	binding, err := ResolveStorageBinding(params.InstType)
	if err != nil {
		return result, err
	}
	storageSubjectID := strings.TrimSpace(params.SubjectID)
	if storageSubjectID == "" {
		storageSubjectID = params.Symbol
	}
	writer := c.storage
	if writer == nil {
		accessTarget := runtimeapp.GetStorageRPCGatewayTarget()
		if accessTarget == "" {
			return result, fmt.Errorf("未配置存储 access tRPC 地址")
		}
		writer = newStorageWriter(accessTarget, "", storageAuthInfo(binding))
	}
	watermark, found, err := writer.LatestTimeSeriesTime(ctx, &storagepb.TimeSeriesKey{
		SpaceId: spaceID, DatasetId: datasetID, SubjectId: storageSubjectID, Freq: freq,
	})
	if err != nil {
		return result, fmt.Errorf("读取K线水位线失败: %w", err)
	}
	var watermarkPtr *time.Time
	if found {
		watermarkPtr = &watermark
	}
	cursor := newKlineCursor(watermarkPtr)
	var total uint64
	var outputWatermark time.Time
	for {
		req, ok := cursor.NextRequest(params.Symbol, params.Interval)
		if !ok {
			break
		}
		exchangeKlines, err := c.fetchKlines(ctx, params, req)
		if err != nil {
			return result, err
		}
		klines := convertExchangeKlines(exchangeKlines, params.Symbol, params.Interval)
		closed, skipped := filterClosedKlines(klines, c.currentTime())
		if skipped > 0 {
			log.InfoContextf(ctx, "跳过未闭合K线: space_id=%s, dataset_id=%s, inst_type=%s, symbol=%s, interval=%s, skipped=%d",
				spaceID, datasetID, params.InstType, params.Symbol, params.Interval, skipped)
		}
		if len(closed) > 0 {
			rows, buildErr := buildKlineRows(closed, spaceID, datasetID, storageSubjectID, freq)
			if buildErr != nil {
				return result, buildErr
			}
			if err := writer.UpsertFields(ctx, rows); err != nil {
				return result, fmt.Errorf("K线写入存储失败: %w", err)
			}
			total += uint64(len(rows))
			for _, row := range rows {
				dataTime, parseErr := time.Parse(time.RFC3339Nano, row.GetKey().GetTimeSeries().GetDataTime())
				if parseErr != nil {
					return result, fmt.Errorf("K线写入确认包含非法 data_time: %w", parseErr)
				}
				if outputWatermark.IsZero() || dataTime.After(outputWatermark) {
					outputWatermark = dataTime
				}
			}
		}
		more, advanceErr := cursor.Advance(exchangeKlines)
		if advanceErr != nil {
			return result, advanceErr
		}
		if !more {
			break
		}
	}
	log.InfoContextf(
		ctx,
		"K线采集完成: space_id=%s, dataset_id=%s, inst_type=%s, symbol=%s, interval=%s, count=%d, job_item_id=%q",
		spaceID, datasetID, params.InstType, params.Symbol, params.Interval, total, jobcontext.JobItemID(ctx),
	)
	result.RowsWritten = total
	if total == 0 {
		log.InfoContextf(
			ctx,
			"event=%q space_id=%q dataset_id=%q subject_id=%q freq=%q rows_written=0",
			"collector_kline_no_new_closed", spaceID, datasetID, storageSubjectID, freq,
		)
	} else {
		result.OutputWatermark = outputWatermark.UTC().Format(time.RFC3339Nano)
	}
	return result, nil
}

func (c *KlineCollector) currentTime() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

// fetchKlines 从币安 API 获取一页 K 线数据。
func (c *KlineCollector) fetchKlines(ctx context.Context, params *sources.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
	if c.fetchKlinePage != nil {
		return c.fetchKlinePage(ctx, params, req)
	}
	return c.fetchExchangeKlines(ctx, params, req)
}

func (c *KlineCollector) fetchExchangeKlines(ctx context.Context, params *sources.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
	switch params.InstType {
	case InstTypeSPOT:
		return c.spotAPI.GetKline(ctx, req)
	case InstTypeSWAP:
		return c.swapAPI.GetKline(ctx, req)
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", params.InstType)
	}
}

func convertExchangeKlines(exchangeKlines []*exchange.Kline, symbol string, interval string) []*market.Kline {
	klines := make([]*market.Kline, 0, len(exchangeKlines))
	for _, ek := range exchangeKlines {
		kline := market.NewKline("binance", symbol, interval)
		kline.OpenTime = ek.OpenTime
		kline.CloseTime = ek.CloseTime
		kline.Open = ek.Open
		kline.High = ek.High
		kline.Low = ek.Low
		kline.Close = ek.Close
		kline.Volume = ek.Volume
		kline.QuoteVolume = ek.QuoteVolume
		kline.TradeCount = ek.TradeCount
		klines = append(klines, kline)
	}
	return klines
}

func normalizeFreq(interval string) (string, error) {
	return sources.NormalizeFreq(interval)
}

func buildKlineRows(klines []*market.Kline, spaceID string, datasetID string, symbol string, freq string) ([]*storagepb.RowFieldUpsert, error) {
	closedKlines, _ := filterClosedKlines(klines, time.Now())
	if len(klines) > 0 && len(closedKlines) == 0 {
		return nil, fmt.Errorf("%w: symbol=%s, freq=%s, latest_close_time=%s", errKlineNotClosed, symbol, freq, latestCloseTime(klines))
	}

	rows := make([]*storagepb.RowFieldUpsert, 0, len(klines))
	for _, kline := range closedKlines {
		openTime := formatKlineTime(kline.OpenTime)
		openValue, err := kline.Open.Float64()
		if err != nil {
			return nil, fmt.Errorf("解析开盘价失败: %w", err)
		}
		highValue, err := kline.High.Float64()
		if err != nil {
			return nil, fmt.Errorf("解析最高价失败: %w", err)
		}
		lowValue, err := kline.Low.Float64()
		if err != nil {
			return nil, fmt.Errorf("解析最低价失败: %w", err)
		}
		closeValue, err := kline.Close.Float64()
		if err != nil {
			return nil, fmt.Errorf("解析收盘价失败: %w", err)
		}
		volumeValue, err := kline.Volume.Float64()
		if err != nil {
			return nil, fmt.Errorf("解析成交量失败: %w", err)
		}
		quoteVolumeValue, err := kline.QuoteVolume.Float64()
		if err != nil {
			return nil, fmt.Errorf("解析成交额失败: %w", err)
		}

		rows = append(rows, &storagepb.RowFieldUpsert{
			Key: &storagepb.RowKey{
				SpaceId: spaceID, DatasetId: datasetID,
				Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: symbol, Freq: freq, DataTime: openTime}},
			},
			Fields: []*storagepb.FieldValue{
				doubleField("open", openValue),
				doubleField("high", highValue),
				doubleField("low", lowValue),
				doubleField("close", closeValue),
				doubleField("volume", volumeValue),
				doubleField("quote_volume", quoteVolumeValue),
				intField("trade_num", kline.TradeCount),
			},
		})
	}
	return rows, nil
}

func filterClosedKlines(klines []*market.Kline, now time.Time) ([]*market.Kline, int) {
	closed := make([]*market.Kline, 0, len(klines))
	skipped := 0
	for _, kline := range klines {
		if isKlineClosed(kline, now) {
			closed = append(closed, kline)
			continue
		}
		skipped++
	}
	return closed, skipped
}

func isKlineClosed(kline *market.Kline, now time.Time) bool {
	if kline == nil || kline.CloseTime.IsZero() {
		return false
	}
	return now.After(kline.CloseTime)
}

func latestCloseTime(klines []*market.Kline) string {
	var latest time.Time
	for _, kline := range klines {
		if kline == nil || kline.CloseTime.IsZero() {
			continue
		}
		if latest.IsZero() || kline.CloseTime.After(latest) {
			latest = kline.CloseTime
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339Nano)
}

func formatKlineTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
