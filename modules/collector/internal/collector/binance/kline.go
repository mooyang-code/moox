package binance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/collector"
	"github.com/mooyang-code/moox/modules/collector/internal/exchange"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"trpc.group/trpc-go/trpc-go/log"
)

// 产品类型常量
const (
	InstTypeSPOT = "SPOT" // 现货
	InstTypeSWAP = "SWAP" // 永续合约
)

// KlineCollector K线数据采集器
type KlineCollector struct {
	client  *binanceapi.Client
	spotAPI *binanceapi.SpotAPI
	swapAPI *binanceapi.SwapAPI
}

// init 自注册到采集器注册中心
func init() {
	// 创建采集器实例
	client := binanceapi.NewClient()
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

	// 从币安 API 获取 K 线数据
	klines, err := c.fetchKlines(ctx, params)
	if err != nil {
		log.ErrorContextf(ctx, "K线采集失败: inst_type=%s, symbol=%s, interval=%s, error=%v",
			params.InstType, params.Symbol, params.Interval, err)
		return err
	}

	if len(klines) > 0 {
		log.InfoContextf(ctx, "K线采集完成: inst_type=%s, symbol=%s, interval=%s, count=%d, latest=%+v",
			params.InstType, params.Symbol, params.Interval, len(klines), klines[0])
	}

	if err := c.reportKlines(ctx, params, klines); err != nil {
		log.ErrorContextf(ctx, "K线写入存储失败: inst_type=%s, symbol=%s, interval=%s, error=%v",
			params.InstType, params.Symbol, params.Interval, err)
		return err
	}
	log.InfoContextf(ctx, "K线写入存储完成: inst_type=%s, symbol=%s, interval=%s, count=%d",
		params.InstType, params.Symbol, params.Interval, len(klines))
	return nil
}

// fetchKlines 从币安 API 获取 K 线数据
func (c *KlineCollector) fetchKlines(ctx context.Context, params *collector.CollectParams) ([]*market.Kline, error) {
	req := &exchange.KlineRequest{
		Symbol:   params.Symbol,
		Interval: params.Interval,
		Limit:    5, // 只获取最新的5根K线
	}

	var exchangeKlines []*exchange.Kline
	var err error

	// 根据产品类型选择 API
	switch params.InstType {
	case InstTypeSPOT:
		exchangeKlines, err = c.spotAPI.GetKline(ctx, req)
	case InstTypeSWAP:
		exchangeKlines, err = c.swapAPI.GetKline(ctx, req)
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", params.InstType)
	}

	if err != nil {
		return nil, err
	}

	// 转换为 market.Kline 格式
	klines := make([]*market.Kline, 0, len(exchangeKlines))
	for _, ek := range exchangeKlines {
		kline := market.NewKline("binance", params.Symbol, params.Interval)
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
	return klines, nil
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

	datasetID, err := klineDataSetID(params.InstType, freq)
	if err != nil {
		return err
	}

	rows, err := buildKlineRows(klines, params.Symbol, datasetID, freq)
	if err != nil {
		return err
	}

	return c.sendWriteRowsWithRetry(ctx, storageURL, rows)
}

func klineDataSetID(instType, freq string) (string, error) {
	switch instType {
	case InstTypeSWAP:
		return "binance_swap_kline_" + strings.ToLower(freq), nil
	case InstTypeSPOT:
		return "binance_spot_kline_" + strings.ToLower(freq), nil
	default:
		return "", fmt.Errorf("不支持的产品类型: %s", instType)
	}
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

func buildKlineRows(klines []*market.Kline, symbol, datasetID, freq string) ([]DataRow, error) {
	rows := make([]DataRow, 0, len(klines))
	for _, kline := range klines {
		openTime := formatKlineTime(kline.OpenTime)
		closeTime := formatKlineTime(kline.CloseTime)

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

		rows = append(rows, DataRow{
			Slice: DataSlice{
				DatasetID: datasetID,
				SubjectID: symbol,
				Freq:      freq,
			},
			DataTime: openTime,
			Columns: []ColumnValue{
				stringField("candle_begin_time", openTime),
				stringField("candle_end_time", closeTime),
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

func formatKlineTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

func (c *KlineCollector) sendWriteRowsWithRetry(ctx context.Context, storageURL string, rows []DataRow) error {
	return retry.Do(
		func() error {
			return c.sendWriteRows(ctx, storageURL, rows, "kline-sync")
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

func (c *KlineCollector) sendWriteRows(ctx context.Context, storageURL string, rows []DataRow, appKey string) error {
	request := &WriteRowsRequest{
		AuthInfo: AuthInfo{
			AppID:  "data-collector",
			AppKey: appKey,
		},
		WriteMode: "WRITE_MODE_APPEND",
		Rows:      rows,
	}

	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}

	url := fmt.Sprintf("%s/trpc.storage.data.DataService/WriteRows", storageURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	var respBody bytes.Buffer
	_, _ = respBody.ReadFrom(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody.String())
	}

	var writeResp WriteRowsResponse
	if err := json.Unmarshal(respBody.Bytes(), &writeResp); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	if writeResp.RetInfo.Code != 0 {
		return fmt.Errorf("错误码 %d: %s", writeResp.RetInfo.Code, writeResp.RetInfo.Msg)
	}

	return nil
}

// WriteRowsRequest 表示 Binance K 线采集写入 Storage 的请求体。
type WriteRowsRequest struct {
	AuthInfo  AuthInfo  `json:"auth_info"`
	WriteMode string    `json:"write_mode"`
	Rows      []DataRow `json:"rows"`
}

// WriteRowsResponse 表示 Binance K 线采集写入 Storage 的响应体。
type WriteRowsResponse struct {
	RetInfo RetInfo `json:"ret_info"`
}

// DataSlice 表示一次写入请求中的数据切片。
type DataSlice struct {
	DatasetID  string            `json:"dataset_id"`
	SubjectID  string            `json:"subject_id"`
	Freq       string            `json:"freq,omitempty"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
}

// DataRow 表示采集器写入 Storage 的一行数据。
type DataRow struct {
	Slice    DataSlice         `json:"slice"`
	DataTime string            `json:"data_time,omitempty"`
	RowID    string            `json:"row_id,omitempty"`
	Columns  []ColumnValue     `json:"columns"`
	Attrs    map[string]string `json:"attrs,omitempty"`
}

// ColumnValue 表示采集器写入行中的一个列值。
type ColumnValue struct {
	ColumnName string     `json:"column_name"`
	ValueType  string     `json:"value_type"`
	Value      TypedValue `json:"value"`
}

// TypedValue 表示采集器写入列的类型化值。
type TypedValue struct {
	StringValue *string  `json:"string_value,omitempty"`
	IntValue    *int64   `json:"int_value,omitempty"`
	DoubleValue *float64 `json:"double_value,omitempty"`
}

func stringField(name, value string) ColumnValue {
	return ColumnValue{ColumnName: name, ValueType: "FIELD_VALUE_TYPE_STRING", Value: TypedValue{StringValue: &value}}
}

func intField(name string, value int64) ColumnValue {
	return ColumnValue{ColumnName: name, ValueType: "FIELD_VALUE_TYPE_INT", Value: TypedValue{IntValue: &value}}
}

func doubleField(name string, value float64) ColumnValue {
	return ColumnValue{ColumnName: name, ValueType: "FIELD_VALUE_TYPE_DOUBLE", Value: TypedValue{DoubleValue: &value}}
}
