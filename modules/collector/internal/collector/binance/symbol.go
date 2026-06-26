package binance

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/collector"
	"github.com/mooyang-code/moox/modules/collector/internal/exchange"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"github.com/mooyang-code/moox/modules/collector/pkg/storage"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// batchSize 每批最多上报的标的数量
	batchSize = 25
	// maxConcurrency 最大并发请求数
	maxConcurrency = 20
	// symbolRecordVersionLatest is the fixed symbol record version.
	symbolRecordVersionLatest = "latest"
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
	client := newConfiguredClient()
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

// reportSymbols 上报标的到存储服务。
// 分批并发上报，每批最多 25 条，最大并发 20
func (c *SymbolCollector) reportSymbols(ctx context.Context, instType string, symbols []*exchange.SymbolInfo) error {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return err
	}

	// 获取存储服务地址
	storageURL := config.GetStorageURL()
	if storageURL == "" {
		return fmt.Errorf("未配置存储服务地址")
	}

	// 构建所有对象行
	allRows, err := buildSymbolRecordRows(symbols, binding)
	if err != nil {
		return err
	}
	totalRows := len(allRows)
	if totalRows == 0 {
		log.InfoContextf(ctx, "[SymbolCollector] 无标的需要上报")
		return nil
	}

	// 分批（一个请求最多25行，分N次请求）
	var rowBatches [][]storage.RecordRow
	var symbolBatches [][]*exchange.SymbolInfo
	for i := 0; i < totalRows; i += batchSize {
		end := i + batchSize
		if end > totalRows {
			end = totalRows
		}
		rowBatches = append(rowBatches, allRows[i:end])
		symbolBatches = append(symbolBatches, symbols[i:end])
	}

	totalBatches := len(rowBatches)
	log.InfoContextf(ctx, "[SymbolCollector] 开始上报标的, 总数=%d, 批次数=%d, datasetID=%s",
		totalRows, totalBatches, binding.RecordDatasetID)

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
			rows := rowBatches[j]
			batchSymbols := symbolBatches[j]
			handlers = append(handlers, func() error {
				err := c.sendSymbolBatchWithRetry(ctx, storageURL, binding, batchSymbols, rows, idx, totalBatches)
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

	log.InfoContextf(ctx, "[SymbolCollector] 上报标的成功, count=%d, datasetID=%s", totalRows, binding.RecordDatasetID)
	return nil
}

// sendWithRetry 发送单个批次请求（带重试）
func (c *SymbolCollector) sendSymbolBatchWithRetry(ctx context.Context, storageURL string, binding StorageBinding, symbols []*exchange.SymbolInfo, rows []storage.RecordRow, batchIdx, totalBatches int) error {
	return retry.Do(
		func() error {
			client := storage.NewClient(storageURL, storageAuthInfo(binding))
			if err := client.WriteRecordRows(ctx, rows); err != nil {
				return err
			}
			for _, symbol := range symbols {
				if err := client.RegisterDataSubject(ctx, buildSymbolRegisterRequest(symbol, binding)); err != nil {
					return err
				}
			}
			return nil
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

func buildSymbolRegisterRequest(symbol *exchange.SymbolInfo, binding StorageBinding) storage.RegisterDataSubjectRequest {
	subjectID := normalizedSubjectID(symbol)
	externalSymbol := binanceapi.FormatSymbol(subjectID)
	bindings := make([]storage.DatasetSubject, 0, len(binding.BindDatasetIDs))
	for _, datasetID := range binding.BindDatasetIDs {
		bindings = append(bindings, storage.DatasetSubject{
			SpaceID:     binding.SpaceID,
			DatasetID:   datasetID,
			SubjectID:   subjectID,
			SubjectRole: "normal",
			Status:      "active",
		})
	}
	return storage.RegisterDataSubjectRequest{
		SpaceID:        binding.SpaceID,
		DataSourceID:   binding.DataSourceID,
		ExternalSymbol: externalSymbol,
		Subject: storage.Subject{
			SpaceID:     binding.SpaceID,
			SubjectID:   subjectID,
			SubjectType: binding.SubjectType,
			Name:        subjectID,
			Market:      binding.SubjectMarket,
			Currency:    symbol.QuoteAsset,
			Status:      symbol.Status,
			Attributes: map[string]string{
				"base_asset":      symbol.BaseAsset,
				"quote_asset":     symbol.QuoteAsset,
				"external_symbol": externalSymbol,
			},
		},
		DatasetBindings: bindings,
	}
}

// buildSymbolRecordRows 构建标的结构化记录行列表。
func buildSymbolRecordRows(symbols []*exchange.SymbolInfo, binding StorageBinding) ([]storage.RecordRow, error) {
	rows := make([]storage.RecordRow, 0, len(symbols))
	for _, s := range symbols {
		subjectID := normalizedSubjectID(s)
		columns := []storage.ColumnValue{
			storage.StringField("symbol", subjectID),
			storage.StringField("external_symbol", binanceapi.FormatSymbol(subjectID)),
			storage.StringField("base_asset", s.BaseAsset),
			storage.StringField("quote_asset", s.QuoteAsset),
			storage.StringField("status", s.Status),
		}
		var err error
		columns, err = appendOptionalDoubleField(columns, "min_qty", s.MinQty)
		if err != nil {
			return nil, fmt.Errorf("%s min_qty: %w", subjectID, err)
		}
		columns, err = appendOptionalDoubleField(columns, "max_qty", s.MaxQty)
		if err != nil {
			return nil, fmt.Errorf("%s max_qty: %w", subjectID, err)
		}
		columns, err = appendOptionalDoubleField(columns, "tick_size", s.TickSize)
		if err != nil {
			return nil, fmt.Errorf("%s tick_size: %w", subjectID, err)
		}
		columns, err = appendOptionalDoubleField(columns, "lot_size", s.LotSize)
		if err != nil {
			return nil, fmt.Errorf("%s lot_size: %w", subjectID, err)
		}

		row := storage.RecordRow{
			Key: storage.RecordKey{
				SpaceID:   binding.SpaceID,
				DatasetID: binding.RecordDatasetID,
				RecordID:  subjectID,
				Version:   symbolRecordVersionLatest,
			},
			Columns: columns,
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func normalizedSubjectID(symbol *exchange.SymbolInfo) string {
	if symbol.Symbol != "" && containsHyphen(symbol.Symbol) {
		return symbol.Symbol
	}
	if symbol.BaseAsset != "" && symbol.QuoteAsset != "" {
		return fmt.Sprintf("%s-%s", symbol.BaseAsset, symbol.QuoteAsset)
	}
	return binanceapi.ParseSymbol(symbol.Symbol, symbol.QuoteAsset)
}

func containsHyphen(value string) bool {
	for _, r := range value {
		if r == '-' {
			return true
		}
	}
	return false
}

func appendOptionalDoubleField(columns []storage.ColumnValue, name, raw string) ([]storage.ColumnValue, error) {
	if raw == "" {
		return columns, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return columns, err
	}
	return append(columns, storage.DoubleField(name, value)), nil
}

func storageAuthInfo(binding StorageBinding) storage.AuthInfo {
	return storage.AuthInfo{
		AppID:     binding.AuthInfo.AppID,
		AppKey:    binding.AuthInfo.AppKey,
		Operator:  binding.AuthInfo.Operator,
		RequestID: binding.AuthInfo.RequestID,
	}
}
