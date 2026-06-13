package binance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

const (
	fieldTypeString = 1 // STR_FIELD
	fieldTypeInt    = 2 // INT_FIELD
	fieldTypeFloat  = 3 // FLOAT_FIELD
	updateTypeSet   = 1 // SET_UPDATE
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

	datasetID, err := datasetIDFromInstType(params.InstType)
	if err != nil {
		return err
	}

	storageURL := config.GetStorageURL()
	if storageURL == "" {
		return fmt.Errorf("未配置存储服务地址")
	}

	freq, err := normalizeFreq(params.Interval)
	if err != nil {
		return err
	}

	// 将交易对作为 rowID 传递给 buildUpdateDataRows
	dataRows, err := buildUpdateDataRows(klines, params.Symbol)
	if err != nil {
		return err
	}

	dataList := UpdateDataList{
		DataKey: DataKey{
			ProjectID: 1,
			DatasetID: datasetID,
			ObjectID:  params.Symbol,
			Freq:      freq,
		},
		DataRows: dataRows,
	}

	return c.sendSetDataWithRetry(ctx, storageURL, []UpdateDataList{dataList})
}

func datasetIDFromInstType(instType string) (int32, error) {
	switch instType {
	case InstTypeSWAP:
		return 100, nil
	case InstTypeSPOT:
		return 101, nil
	default:
		return 0, fmt.Errorf("不支持的产品类型: %s", instType)
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

func buildUpdateDataRows(klines []*market.Kline, symbol string) ([]UpdateDataRow, error) {
	rows := make([]UpdateDataRow, 0, len(klines))
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

		fields := map[string]DataUpdateField{
			"candle_begin_time": {
				FieldKey:   "candle_begin_time",
				FieldType:  fieldTypeString,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Str: stringPtr(openTime),
				},
			},
			"candle_end_time": {
				FieldKey:   "candle_end_time",
				FieldType:  fieldTypeString,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Str: stringPtr(closeTime),
				},
			},
			"open": {
				FieldKey:   "open",
				FieldType:  fieldTypeFloat,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Float: float64Ptr(openValue),
				},
			},
			"high": {
				FieldKey:   "high",
				FieldType:  fieldTypeFloat,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Float: float64Ptr(highValue),
				},
			},
			"low": {
				FieldKey:   "low",
				FieldType:  fieldTypeFloat,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Float: float64Ptr(lowValue),
				},
			},
			"close": {
				FieldKey:   "close",
				FieldType:  fieldTypeFloat,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Float: float64Ptr(closeValue),
				},
			},
			"volume": {
				FieldKey:   "volume",
				FieldType:  fieldTypeFloat,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Float: float64Ptr(volumeValue),
				},
			},
			"quote_volume": {
				FieldKey:   "quote_volume",
				FieldType:  fieldTypeFloat,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Float: float64Ptr(quoteVolumeValue),
				},
			},
			"trade_num": {
				FieldKey:   "trade_num",
				FieldType:  fieldTypeInt,
				UpdateType: updateTypeSet,
				SimpleValue: DataSimpleValue{
					Int: int64Ptr(kline.TradeCount),
				},
			},
		}

		// 使用交易对作为 rowID，时间作为 times
		rows = append(rows, UpdateDataRow{
			Times:  openTime,
			RowID:  symbol, // 使用交易对作为 rowID
			Fields: fields,
		})
	}
	return rows, nil
}

func formatKlineTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

func (c *KlineCollector) sendSetDataWithRetry(ctx context.Context, storageURL string, dataList []UpdateDataList) error {
	return retry.Do(
		func() error {
			return c.sendSetData(ctx, storageURL, dataList)
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

func (c *KlineCollector) sendSetData(ctx context.Context, storageURL string, dataList []UpdateDataList) error {
	request := &SetDataRequest{
		AuthInfo: AuthInfo{
			AppID:  "data-collector",
			AppKey: "kline-sync",
		},
		DataList: dataList,
	}

	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}

	url := fmt.Sprintf("%s/trpc.storage.access.Access/SetData", storageURL)
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

	var setResp SetDataResponse
	if err := json.Unmarshal(respBody.Bytes(), &setResp); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	if setResp.RetInfo.Code != 0 {
		return fmt.Errorf("错误码 %d: %s", setResp.RetInfo.Code, setResp.RetInfo.Msg)
	}

	return nil
}

func stringPtr(value string) *string {
	return &value
}

func float64Ptr(value float64) *float64 {
	return &value
}

func int64Ptr(value int64) *int64 {
	return &value
}

type SetDataRequest struct {
	AuthInfo AuthInfo         `json:"auth_info"`
	DataList []UpdateDataList `json:"data_list"`
}

type SetDataResponse struct {
	RetInfo RetInfo `json:"ret_info"`
}

type DataKey struct {
	ProjectID int32  `json:"project_id"`
	DatasetID int32  `json:"dataset_id"`
	ObjectID  string `json:"object_id"`
	Freq      string `json:"freq"`
}

type UpdateDataList struct {
	DataKey  DataKey         `json:"data_key"`
	DataRows []UpdateDataRow `json:"data_rows"`
}

type UpdateDataRow struct {
	Times  string                     `json:"times"`
	RowID  string                     `json:"row_id,omitempty"`
	Fields map[string]DataUpdateField `json:"fields"`
}

type DataUpdateField struct {
	FieldKey    string          `json:"field_key"`
	FieldType   int             `json:"field_type"`
	UpdateType  int             `json:"update_type"`
	SimpleValue DataSimpleValue `json:"simple_value"`
}

type DataSimpleValue struct {
	Str   *string  `json:"str,omitempty"`
	Int   *int64   `json:"int,omitempty"`
	Float *float64 `json:"float,omitempty"`
}
