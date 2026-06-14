package binance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/collector"
	"github.com/mooyang-code/moox/modules/collector/internal/exchange"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// batchSize 每批最多上报的标的数量
	batchSize = 25
	// maxConcurrency 最大并发请求数
	maxConcurrency = 20
)

// SymbolCollector 标的同步采集器
type SymbolCollector struct {
	client  *binanceapi.Client
	spotAPI *binanceapi.SpotAPI
	swapAPI *binanceapi.SwapAPI
}

// Source 返回数据源标识
func (c *SymbolCollector) Source() string {
	return "binance"
}

// DataType 返回数据类型标识
func (c *SymbolCollector) DataType() string {
	return "symbol"
}

func init() {
	client := binanceapi.NewClient()
	c := &SymbolCollector{
		client:  client,
		spotAPI: binanceapi.NewSpotAPI(client),
		swapAPI: binanceapi.NewSwapAPI(client),
	}

	// 注册到全局注册中心
	err := collector.NewBuilder().
		Source("binance", "币安").
		DataType("symbol", "标的").
		Description("币安交易所标的同步采集器").
		Collector(c).
		Register()

	if err != nil {
		log.Errorf("注册币安标的采集器失败: %v", err)
	}
}

// Collect 执行标的同步采集
func (c *SymbolCollector) Collect(ctx context.Context, params *collector.CollectParams) error {
	log.InfoContextf(ctx, "[SymbolCollector] 开始采集标的, InstType=%s", params.InstType)

	// 根据产品类型获取标的列表
	symbols, err := c.fetchSymbols(ctx, params)
	if err != nil {
		log.ErrorContextf(ctx, "[SymbolCollector] 获取标的失败: %v", err)
		return err
	}

	log.InfoContextf(ctx, "[SymbolCollector] 获取标的成功（过滤前）, count=%d, InstType=%s",
		len(symbols), params.InstType)

	// 过滤标的：仅保留 QuoteAsset 为 USDT 且 Status 为 active 的数据
	filteredSymbols := c.filterSymbols(symbols)
	log.InfoContextf(ctx, "[SymbolCollector] 过滤后标的数量, count=%d (过滤前: %d), InstType=%s",
		len(filteredSymbols), len(symbols), params.InstType)

	// 上报标的到 Server
	if err := c.reportSymbols(ctx, params.InstType, filteredSymbols); err != nil {
		log.ErrorContextf(ctx, "[SymbolCollector] 上报标的失败: %v", err)
		return err
	}

	log.InfoContextf(ctx, "[SymbolCollector] 标的采集完成, InstType=%s", params.InstType)
	return nil
}

// fetchSymbols 获取标的列表
func (c *SymbolCollector) fetchSymbols(ctx context.Context, params *collector.CollectParams) ([]*exchange.SymbolInfo, error) {
	switch params.InstType {
	case InstTypeSPOT:
		return c.spotAPI.GetExchangeInfo(ctx)
	case InstTypeSWAP:
		return c.swapAPI.GetExchangeInfo(ctx)
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", params.InstType)
	}
}

// filterSymbols 过滤标的列表，仅保留 QuoteAsset 为 USDT 且 Status 为 active 的数据
func (c *SymbolCollector) filterSymbols(symbols []*exchange.SymbolInfo) []*exchange.SymbolInfo {
	filtered := make([]*exchange.SymbolInfo, 0, len(symbols))

	for _, symbol := range symbols {
		// 仅保留 QuoteAsset 为 USDT 且 Status 为 active 的标的
		if symbol.QuoteAsset == "USDT" && symbol.Status == "active" {
			filtered = append(filtered, symbol)
		}
	}

	return filtered
}

// reportSymbols 上报标的到存储服务（调用 DataService.WriteRows 接口）
// 分批并发上报，每批最多 25 条，最大并发 20
func (c *SymbolCollector) reportSymbols(ctx context.Context, instType string, symbols []*exchange.SymbolInfo) error {
	datasetID, err := symbolDataSetID(instType)
	if err != nil {
		return err
	}

	// 获取存储服务地址
	storageURL := config.GetStorageURL()
	if storageURL == "" {
		return fmt.Errorf("未配置存储服务地址")
	}

	// 构建所有对象行
	allRows := c.buildSymbolRecordRows(symbols)
	totalRows := len(allRows)
	if totalRows == 0 {
		log.InfoContextf(ctx, "[SymbolCollector] 无标的需要上报")
		return nil
	}

	// 分批（一个请求最多25行，分N次请求）
	var batches [][]SymbolRecordRow
	for i := 0; i < totalRows; i += batchSize {
		end := i + batchSize
		if end > totalRows {
			end = totalRows
		}
		batches = append(batches, allRows[i:end])
	}

	totalBatches := len(batches)
	log.InfoContextf(ctx, "[SymbolCollector] 开始上报标的, 总数=%d, 批次数=%d, datasetID=%s",
		totalRows, totalBatches, datasetID)

	// 结果收集
	var mu sync.Mutex
	var firstErr error
	successCount := 0

	// 按 maxConcurrency 分组并发执行（一次并发请求最多 maxConcurrency 个；避免瞬时请求量过大）
	for i := 0; i < totalBatches; i += maxConcurrency {
		end := i + maxConcurrency
		if end > totalBatches {
			end = totalBatches
		}

		// 构建当前组的处理函数
		var handlers []func() error
		for j := i; j < end; j++ {
			idx := j
			rows := batches[j]
			handlers = append(handlers, func() error {
				err := c.sendWithRetry(ctx, storageURL, datasetID, rows, idx, totalBatches)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					log.ErrorContextf(ctx, "[SymbolCollector] 批次 %d/%d 上报失败: %v", idx+1, totalBatches, err)
				} else {
					successCount += len(rows)
				}
				return nil // 不中断其他并发任务
			})
		}

		// 并发执行当前组
		_ = trpc.GoAndWait(handlers...)
	}

	if firstErr != nil {
		log.ErrorContextf(ctx, "[SymbolCollector] 上报完成，成功=%d, 失败=%d, 首个错误: %v",
			successCount, totalRows-successCount, firstErr)
		return fmt.Errorf("部分批次上报失败: %w", firstErr)
	}

	log.InfoContextf(ctx, "[SymbolCollector] 上报标的成功, count=%d, datasetID=%s", totalRows, datasetID)
	return nil
}

func symbolDataSetID(instType string) (string, error) {
	switch instType {
	case InstTypeSWAP:
		return "binance_swap_symbols", nil
	case InstTypeSPOT:
		return "binance_spot_symbols", nil
	default:
		return "", fmt.Errorf("不支持的产品类型: %s", instType)
	}
}

// sendWithRetry 发送单个批次请求（带重试）
func (c *SymbolCollector) sendWithRetry(ctx context.Context, storageURL string, datasetID string, rows []SymbolRecordRow, batchIdx, totalBatches int) error {
	return retry.Do(
		func() error {
			return c.send(ctx, storageURL, datasetID, rows)
		},
		retry.Attempts(3),
		retry.Delay(500*time.Millisecond),
		retry.DelayType(retry.BackOffDelay),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "[SymbolCollector] 批次 %d/%d 重试第 %d 次: %v",
				batchIdx+1, totalBatches, n+1, err)
		}),
		retry.Context(ctx),
	)
}

// send 发送单个批次请求
func (c *SymbolCollector) send(ctx context.Context, storageURL string, datasetID string, rows []SymbolRecordRow) error {
	request := &WriteRowsRequest{
		AuthInfo: AuthInfo{
			AppID:  "data-collector",
			AppKey: "symbol-sync",
		},
		WriteMode: "WRITE_MODE_UPSERT",
		Rows:      buildSymbolDataRows(datasetID, rows),
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

// buildSymbolRecordRows 构建标的结构化记录行列表。
func (c *SymbolCollector) buildSymbolRecordRows(symbols []*exchange.SymbolInfo) []SymbolRecordRow {
	rows := make([]SymbolRecordRow, 0, len(symbols))
	for _, s := range symbols {
		// 标的 ID 格式：BaseAsset-QuoteAsset (如 BTC-USDT)
		instrumentID := fmt.Sprintf("%s-%s", s.BaseAsset, s.QuoteAsset)

		row := SymbolRecordRow{
			RecordID: instrumentID,
			Columns: []ColumnValue{
				stringField("symbol", instrumentID),
				stringField("base_asset", s.BaseAsset),
				stringField("quote_asset", s.QuoteAsset),
				stringField("external_symbol", s.Symbol),
				stringField("status", s.Status),
				stringField("unshelve_time", "2099-01-01 00:00:00"),
			},
		}
		rows = append(rows, row)
	}
	return rows
}

func buildSymbolDataRows(datasetID string, rows []SymbolRecordRow) []DataRow {
	out := make([]DataRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, DataRow{
			Slice: DataSlice{
				DatasetID: datasetID,
				SubjectID: row.RecordID,
			},
			RowID:   row.RecordID,
			Columns: row.Columns,
		})
	}
	return out
}

// AuthInfo 鉴权信息
type AuthInfo struct {
	AppID  string `json:"app_id"`
	AppKey string `json:"app_key"`
}

// SymbolRecordRow 标的结构化记录行。
type SymbolRecordRow struct {
	RecordID string        `json:"record_id"`
	Columns  []ColumnValue `json:"columns"`
}

// RetInfo 返回信息
type RetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}
