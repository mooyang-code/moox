package binance

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/jobcontext"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// batchSize 每批最多上报的标的数量
	batchSize = 25
	// maxConcurrency 最大并发请求数。
	// RegisterDataSubject 会触发 metadata snapshot refresh，串行上报避免并发刷新冲突。
	maxConcurrency = 1
)

// SymbolCollector 标的同步采集器
type SymbolCollector struct {
	client          *binanceapi.Client
	spotAPI         *binanceapi.SpotAPI
	swapAPI         *binanceapi.SwapAPI
	storage         symbolStorage
	fetchSymbolPage func(context.Context, *sources.CollectParams) ([]*exchange.SymbolInfo, error)
}

type symbolStorage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error
	ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
	BindDatasetSubject(context.Context, *storagepb.DatasetSubject) error
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
			DataType("symbol", "标的").
			Description("币安交易所标的同步采集器").
			Collector(c).
			Register()
		if err != nil {
			log.Errorf("注册币安标的采集器失败: %v", err)
		}
	}
}

// Collect 执行标的同步采集
func (c *SymbolCollector) Collect(ctx context.Context, params *sources.CollectParams) error {
	_, err := c.CollectWithResult(ctx, params)
	return err
}

// CollectWithResult executes a symbol snapshot and reports current-invocation writes.
func (c *SymbolCollector) CollectWithResult(
	ctx context.Context,
	params *sources.CollectParams,
) (sources.CollectResult, error) {
	if params == nil {
		return sources.CollectResult{}, fmt.Errorf("标的采集参数不能为空")
	}
	spaceID := strings.TrimSpace(params.SpaceID)
	if spaceID == "" {
		return sources.CollectResult{}, fmt.Errorf("space_id 不能为空")
	}
	datasetID := strings.TrimSpace(params.DatasetID)
	if datasetID == "" {
		return sources.CollectResult{}, fmt.Errorf("dataset_id 不能为空")
	}
	log.InfoContextf(ctx, "[SymbolCollector] 开始采集标的, space_id=%s, dataset_id=%s, InstType=%s",
		spaceID, datasetID, params.InstType)

	// 根据产品类型获取标的列表
	symbols, err := c.fetchSymbols(ctx, params)
	if err != nil {
		log.ErrorContextf(ctx, "[SymbolCollector] 获取标的失败: %v", err)
		return sources.CollectResult{}, err
	}

	log.InfoContextf(ctx, "[SymbolCollector] 获取标的成功（过滤前）, count=%d, InstType=%s",
		len(symbols), params.InstType)

	// 过滤标的：仅保留 QuoteAsset 为 USDT 且 Status 为 active 的数据
	filteredSymbols := c.filterSymbols(symbols)
	log.InfoContextf(ctx, "[SymbolCollector] 过滤后标的数量, count=%d (过滤前: %d), InstType=%s",
		len(filteredSymbols), len(symbols), params.InstType)
	if len(filteredSymbols) == 0 {
		return sources.CollectResult{}, fmt.Errorf("Binance active USDT symbol snapshot is empty")
	}

	// 上报标的到 Server
	snapshotVersion, err := c.reportSymbols(ctx, spaceID, datasetID, params.InstType, filteredSymbols)
	if err != nil {
		log.ErrorContextf(ctx, "[SymbolCollector] 上报标的失败: %v", err)
		return sources.CollectResult{}, err
	}

	log.InfoContextf(
		ctx,
		"[SymbolCollector] 标的采集完成, space_id=%s, dataset_id=%s, InstType=%s, job_item_id=%q",
		spaceID, datasetID, params.InstType, jobcontext.JobItemID(ctx),
	)
	result := sources.CollectResult{
		RowsWritten:     uint64(len(filteredSymbols)),
		SnapshotVersion: snapshotVersion,
	}
	return result, nil
}

// fetchSymbols 获取标的列表
func (c *SymbolCollector) fetchSymbols(ctx context.Context, params *sources.CollectParams) ([]*exchange.SymbolInfo, error) {
	if c.fetchSymbolPage != nil {
		return c.fetchSymbolPage(ctx, params)
	}
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
// 每批最多 25 条；当前串行发送，避免并发刷新 metadata snapshot。
func (c *SymbolCollector) reportSymbols(
	ctx context.Context,
	spaceID string,
	datasetID string,
	instType string,
	symbols []*exchange.SymbolInfo,
) (string, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return "", err
	}

	writer := c.storage
	if writer == nil {
		target := runtimeapp.GetStorageRPCGatewayTarget()
		if target == "" {
			return "", fmt.Errorf("未配置存储服务 tRPC 地址")
		}
		writer = newStorageWriter(target, target, storageAuthInfo(binding))
	}
	memberships, err := writer.ListDatasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return "", fmt.Errorf("list symbol memberships for dataset %s: %w", datasetID, err)
	}
	existingActive := make(map[string]struct{}, len(memberships))
	for _, membership := range memberships {
		if membership != nil && strings.EqualFold(strings.TrimSpace(membership.GetStatus()), "active") {
			existingActive[strings.TrimSpace(membership.GetSubjectId())] = struct{}{}
		}
	}

	// 构建所有对象行
	allRows, err := buildSymbolRecordRows(symbols, spaceID, datasetID)
	if err != nil {
		return "", err
	}
	totalRows := len(allRows)
	if totalRows == 0 {
		log.InfoContextf(ctx, "[SymbolCollector] 无标的需要上报")
		return "", nil
	}
	snapshotVersion := allRows[0].GetKey().GetRecord().GetVersion()

	// 分批（一个请求最多25行，分N次请求）
	var rowBatches [][]*storagepb.RowFieldUpsert
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
			rows := rowBatches[j]
			batchSymbols := symbolBatches[j]
			handlers = append(handlers, func() error {
				err := c.sendSymbolBatch(
					ctx, writer, spaceID, datasetID, binding, existingActive, batchSymbols, rows,
				)
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
		if err := trpc.GoAndWait(handlers...); err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = fmt.Errorf("symbol batch worker failed: %w", err)
			}
			mu.Unlock()
			log.ErrorContextf(ctx, "[SymbolCollector] 批次 worker 异常: %v", err)
		}
	}

	if firstErr != nil {
		log.ErrorContextf(ctx, "[SymbolCollector] 上报完成，成功=%d, 失败=%d, 首个错误: %v",
			successCount, totalRows-successCount, firstErr)
		return "", fmt.Errorf("部分批次上报失败: %w", firstErr)
	}

	if err := reconcileInactiveSymbolMemberships(ctx, writer, spaceID, datasetID, symbols, memberships); err != nil {
		return "", err
	}

	log.InfoContextf(ctx, "[SymbolCollector] 上报标的成功, count=%d, datasetID=%s", totalRows, datasetID)
	return snapshotVersion, nil
}

func reconcileInactiveSymbolMemberships(
	ctx context.Context,
	writer symbolStorage,
	spaceID string,
	datasetID string,
	activeSymbols []*exchange.SymbolInfo,
	memberships []*storagepb.DatasetSubject,
) error {
	if len(activeSymbols) == 0 {
		return nil
	}
	activeSubjectIDs := make(map[string]struct{}, len(activeSymbols))
	for _, symbol := range activeSymbols {
		subjectID := strings.TrimSpace(normalizedSubjectID(symbol))
		if subjectID != "" {
			activeSubjectIDs[subjectID] = struct{}{}
		}
	}
	if len(activeSubjectIDs) == 0 {
		return nil
	}

	inactiveMemberships := make([]*storagepb.DatasetSubject, 0)
	inactiveSymbols := make([]*exchange.SymbolInfo, 0)
	for _, membership := range memberships {
		if membership == nil ||
			strings.TrimSpace(membership.GetSpaceId()) != spaceID ||
			strings.TrimSpace(membership.GetDatasetId()) != datasetID ||
			!strings.EqualFold(strings.TrimSpace(membership.GetStatus()), "active") {
			continue
		}
		if _, ok := activeSubjectIDs[strings.TrimSpace(membership.GetSubjectId())]; ok {
			continue
		}
		inactive := proto.Clone(membership).(*storagepb.DatasetSubject)
		inactive.Status = "disabled"
		inactiveMemberships = append(inactiveMemberships, inactive)
		inactiveSymbols = append(inactiveSymbols, inactiveSymbolInfo(inactive.GetSubjectId()))
	}
	inactiveRows, err := buildSymbolRecordRows(inactiveSymbols, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("build inactive symbol rows for dataset %s: %w", datasetID, err)
	}
	for start := 0; start < len(inactiveRows); start += batchSize {
		end := start + batchSize
		if end > len(inactiveRows) {
			end = len(inactiveRows)
		}
		if err := writer.UpsertFields(ctx, inactiveRows[start:end]); err != nil {
			return fmt.Errorf("write inactive symbol rows for dataset %s: %w", datasetID, err)
		}
	}
	for _, inactive := range inactiveMemberships {
		if err := writer.BindDatasetSubject(ctx, inactive); err != nil {
			return fmt.Errorf(
				"deactivate symbol membership %s/%s/%s: %w",
				spaceID, datasetID, inactive.GetSubjectId(), err,
			)
		}
		log.InfoContextf(
			ctx,
			"[SymbolCollector] 停用已下架标的 membership, space_id=%s, dataset_id=%s, subject_id=%s",
			spaceID, datasetID, inactive.GetSubjectId(),
		)
	}
	return nil
}

func inactiveSymbolInfo(subjectID string) *exchange.SymbolInfo {
	subjectID = strings.TrimSpace(subjectID)
	baseAsset, quoteAsset, _ := strings.Cut(subjectID, "-")
	return &exchange.SymbolInfo{
		Symbol:     subjectID,
		BaseAsset:  baseAsset,
		QuoteAsset: quoteAsset,
		Status:     "inactive",
	}
}

func (c *SymbolCollector) sendSymbolBatch(
	ctx context.Context,
	writer symbolStorage,
	spaceID string,
	datasetID string,
	binding StorageBinding,
	existingActive map[string]struct{},
	symbols []*exchange.SymbolInfo,
	rows []*storagepb.RowFieldUpsert,
) error {
	if err := writer.UpsertFields(ctx, rows); err != nil {
		return err
	}
	for _, symbol := range symbols {
		if _, exists := existingActive[normalizedSubjectID(symbol)]; exists {
			continue
		}
		if err := writer.RegisterDataSubject(ctx, buildSymbolRegisterRequest(symbol, spaceID, datasetID, binding)); err != nil {
			return err
		}
	}
	return nil
}

func buildSymbolRegisterRequest(
	symbol *exchange.SymbolInfo,
	spaceID string,
	targetDatasetID string,
	binding StorageBinding,
) *storagepb.RegisterDataSubjectReq {
	subjectID := normalizedSubjectID(symbol)
	externalSymbol := binanceapi.FormatSymbol(subjectID)
	return &storagepb.RegisterDataSubjectReq{
		SpaceId:        spaceID,
		DataSourceId:   binding.DataSourceID,
		ExternalSymbol: externalSymbol,
		Subject: &storagepb.Subject{
			SpaceId:     spaceID,
			SubjectId:   subjectID,
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
		DatasetBindings: []*storagepb.DatasetSubject{{
			SpaceId:     spaceID,
			DatasetId:   targetDatasetID,
			SubjectId:   subjectID,
			SubjectRole: "normal",
			Status:      "active",
		}},
	}
}

// buildSymbolRecordRows 构建标的结构化记录行列表。
func buildSymbolRecordRows(symbols []*exchange.SymbolInfo, spaceID string, datasetID string) ([]*storagepb.RowFieldUpsert, error) {
	rows := make([]*storagepb.RowFieldUpsert, 0, len(symbols))
	version := time.Now().UTC().Format(time.RFC3339Nano)
	for _, s := range symbols {
		subjectID := normalizedSubjectID(s)
		fields := []*storagepb.FieldValue{
			stringField("symbol", subjectID),
			stringField("external_symbol", binanceapi.FormatSymbol(subjectID)),
			stringField("base_asset", s.BaseAsset),
			stringField("quote_asset", s.QuoteAsset),
			stringField("status", s.Status),
		}
		var err error
		fields, err = appendOptionalDoubleField(fields, "min_qty", s.MinQty)
		if err != nil {
			return nil, fmt.Errorf("%s min_qty: %w", subjectID, err)
		}
		fields, err = appendOptionalDoubleField(fields, "max_qty", s.MaxQty)
		if err != nil {
			return nil, fmt.Errorf("%s max_qty: %w", subjectID, err)
		}
		fields, err = appendOptionalDoubleField(fields, "tick_size", s.TickSize)
		if err != nil {
			return nil, fmt.Errorf("%s tick_size: %w", subjectID, err)
		}
		fields, err = appendOptionalDoubleField(fields, "lot_size", s.LotSize)
		if err != nil {
			return nil, fmt.Errorf("%s lot_size: %w", subjectID, err)
		}

		row := &storagepb.RowFieldUpsert{
			Key: &storagepb.RowKey{
				SpaceId: spaceID, DatasetId: datasetID,
				Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: subjectID, Version: version}},
			},
			Fields: fields,
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

func appendOptionalDoubleField(fields []*storagepb.FieldValue, name, raw string) ([]*storagepb.FieldValue, error) {
	if raw == "" {
		return fields, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fields, err
	}
	return append(fields, doubleField(name, value)), nil
}
