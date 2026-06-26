package binance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/collector"
	"github.com/mooyang-code/moox/modules/collector/internal/exchange"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"github.com/mooyang-code/moox/modules/collector/pkg/storage"
	"trpc.group/trpc-go/trpc-go/log"
)

// 产品类型常量
const (
	InstTypeSPOT = "SPOT" // 现货
	InstTypeSWAP = "SWAP" // 永续合约
)

const (
	klineFetchLimit         = 5
	klineCloseRetryDelay    = 200 * time.Millisecond
	klineCloseRetryAttempts = 3
)

var errKlineNotClosed = errors.New("K线尚未闭合")

// KlineCollector K线数据采集器
type KlineCollector struct {
	client  *binanceapi.Client
	spotAPI *binanceapi.SpotAPI
	swapAPI *binanceapi.SwapAPI
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

	// 注册到全局注册中心
	err := collector.NewBuilder().
		Source("binance", "币安").
		DataType("kline", "K线").
		Description("币安K线数据采集器").
		Collector(c).
		Register()

	if err != nil {
		panic(fmt.Sprintf("注册K线采集器失败: %v", err))
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
func (c *KlineCollector) Collect(ctx context.Context, params *collector.CollectParams) error {
	log.InfoContextf(ctx, "K线采集开始: inst_type=%s, symbol=%s, interval=%s",
		params.InstType, params.Symbol, params.Interval)
	fmt.Printf("[kline] collect start inst_type=%s symbol=%s interval=%s\n", params.InstType, params.Symbol, params.Interval)

	// 从币安 API 获取 K 线数据
	fmt.Printf("[kline] fetch start inst_type=%s symbol=%s interval=%s spot_domain=%s swap_domain=%s\n",
		params.InstType, params.Symbol, params.Interval, c.client.SpotDomain(), c.client.SwapDomain())
	klines, err := c.fetchKlines(ctx, params)
	if err != nil {
		fmt.Printf("[kline] fetch error inst_type=%s symbol=%s interval=%s error=%v\n",
			params.InstType, params.Symbol, params.Interval, err)
		log.ErrorContextf(ctx, "K线采集失败: inst_type=%s, symbol=%s, interval=%s, error=%v",
			params.InstType, params.Symbol, params.Interval, err)
		return err
	}
	fmt.Printf("[kline] fetch done inst_type=%s symbol=%s interval=%s count=%d\n",
		params.InstType, params.Symbol, params.Interval, len(klines))

	if len(klines) > 0 {
		log.InfoContextf(ctx, "K线采集完成: inst_type=%s, symbol=%s, interval=%s, count=%d, latest=%+v",
			params.InstType, params.Symbol, params.Interval, len(klines), klines[0])
	}

	fmt.Printf("[kline] storage write start inst_type=%s symbol=%s interval=%s count=%d storage_url=%s\n",
		params.InstType, params.Symbol, params.Interval, len(klines), config.GetStorageURL())
	if err := c.reportKlines(ctx, params, klines); err != nil {
		fmt.Printf("[kline] storage write error inst_type=%s symbol=%s interval=%s error=%v\n",
			params.InstType, params.Symbol, params.Interval, err)
		log.ErrorContextf(ctx, "K线写入存储失败: inst_type=%s, symbol=%s, interval=%s, error=%v",
			params.InstType, params.Symbol, params.Interval, err)
		return err
	}
	fmt.Printf("[kline] storage write done inst_type=%s symbol=%s interval=%s count=%d\n",
		params.InstType, params.Symbol, params.Interval, len(klines))
	log.InfoContextf(ctx, "K线写入存储完成: inst_type=%s, symbol=%s, interval=%s, count=%d",
		params.InstType, params.Symbol, params.Interval, len(klines))
	return nil
}

// fetchKlines 从币安 API 获取 K 线数据
func (c *KlineCollector) fetchKlines(ctx context.Context, params *collector.CollectParams) ([]*market.Kline, error) {
	req := &exchange.KlineRequest{
		Symbol:   params.Symbol,
		Interval: params.Interval,
		Limit:    klineFetchLimit, // 只获取最新的5根K线
	}

	var closedKlines []*market.Kline
	err := retry.Do(
		func() error {
			exchangeKlines, err := c.fetchExchangeKlines(ctx, params, req)
			if err != nil {
				return err
			}
			klines := convertExchangeKlines(exchangeKlines, params.Symbol, params.Interval)
			closed, skipped := filterClosedKlines(klines, time.Now())
			if skipped > 0 {
				log.InfoContextf(ctx, "跳过未闭合K线: inst_type=%s, symbol=%s, interval=%s, skipped=%d",
					params.InstType, params.Symbol, params.Interval, skipped)
			}
			if len(klines) > 0 && len(closed) == 0 {
				return fmt.Errorf("%w: inst_type=%s, symbol=%s, interval=%s, latest_close_time=%s",
					errKlineNotClosed, params.InstType, params.Symbol, params.Interval, latestCloseTime(klines))
			}
			closedKlines = closed
			return nil
		},
		retry.Attempts(klineCloseRetryAttempts),
		retry.Delay(klineCloseRetryDelay),
		retry.LastErrorOnly(true),
		retry.RetryIf(func(err error) bool {
			return errors.Is(err, errKlineNotClosed)
		}),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "K线尚未闭合，重新请求 #%d: inst_type=%s, symbol=%s, interval=%s, err=%v",
				n+1, params.InstType, params.Symbol, params.Interval, err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		return nil, err
	}
	return closedKlines, nil
}

func (c *KlineCollector) fetchExchangeKlines(ctx context.Context, params *collector.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
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

func (c *KlineCollector) reportKlines(ctx context.Context, params *collector.CollectParams, klines []*market.Kline) error {
	if len(klines) == 0 {
		log.InfoContextf(ctx, "K线写入存储跳过: 无数据, inst_type=%s, symbol=%s, interval=%s",
			params.InstType, params.Symbol, params.Interval)
		return nil
	}

	storageURL := config.GetStorageURL()
	if storageURL == "" {
		return fmt.Errorf("未配置存储服务地址")
	}

	freq, err := normalizeFreq(params.Interval)
	if err != nil {
		return err
	}

	binding, err := ResolveStorageBinding(params.InstType)
	if err != nil {
		return err
	}

	rows, err := buildKlineRows(klines, params.Symbol, binding, freq)
	if err != nil {
		return err
	}

	return c.sendTimeSeriesRowsWithRetry(ctx, storageURL, binding, rows)
}

func normalizeFreq(interval string) (string, error) {
	if interval == "" {
		return "", fmt.Errorf("interval 不能为空")
	}
	unit := interval[len(interval)-1]
	switch unit {
	case 'h', 'H':
		return interval[:len(interval)-1] + "H", nil
	case 'd', 'D':
		return interval[:len(interval)-1] + "D", nil
	case 'w', 'W':
		return interval[:len(interval)-1] + "W", nil
	case 'y', 'Y':
		return interval[:len(interval)-1] + "Y", nil
	case 'm', 'M':
		return interval, nil
	default:
		return interval, nil
	}
}

func buildKlineRows(klines []*market.Kline, symbol string, binding StorageBinding, freq string) ([]storage.TimeSeriesRow, error) {
	closedKlines, _ := filterClosedKlines(klines, time.Now())
	if len(klines) > 0 && len(closedKlines) == 0 {
		return nil, fmt.Errorf("%w: symbol=%s, freq=%s, latest_close_time=%s", errKlineNotClosed, symbol, freq, latestCloseTime(klines))
	}

	rows := make([]storage.TimeSeriesRow, 0, len(klines))
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

		rows = append(rows, storage.TimeSeriesRow{
			Key: storage.TimeSeriesKey{
				SpaceID:   binding.SpaceID,
				DatasetID: binding.KlineDatasetID,
				SubjectID: symbol,
				Freq:      freq,
				DataTime:  openTime,
			},
			Columns: []storage.ColumnValue{
				storage.DoubleField("open", openValue),
				storage.DoubleField("high", highValue),
				storage.DoubleField("low", lowValue),
				storage.DoubleField("close", closeValue),
				storage.DoubleField("volume", volumeValue),
				storage.DoubleField("quote_volume", quoteVolumeValue),
				storage.IntField("trade_num", kline.TradeCount),
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

func (c *KlineCollector) sendTimeSeriesRowsWithRetry(ctx context.Context, storageURL string, binding StorageBinding, rows []storage.TimeSeriesRow) error {
	return retry.Do(
		func() error {
			client := storage.NewClient(storageURL, storageAuthInfo(binding))
			return client.WriteTimeSeriesRows(ctx, rows)
		},
		retry.Attempts(3),
		retry.Delay(500*time.Millisecond),
		retry.DelayType(retry.BackOffDelay),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "K线写入存储重试第 %d 次: %v", n+1, err)
		}),
		retry.Context(ctx),
	)
}
