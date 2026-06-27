package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"trpc.group/trpc-go/trpc-go/log"
)

// StorageSymbolProvider 从 xData 存储服务获取标的列表
type StorageSymbolProvider struct {
	xdataURL string
}

// NewStorageSymbolProvider 创建存储标的提供者
func NewStorageSymbolProvider() *StorageSymbolProvider {
	return &StorageSymbolProvider{
		xdataURL: config.GetXDataURL(),
	}
}

// GetSymbols 获取指定数据源和产品类型的所有标的
// 从存储服务的 QueryRecords 接口获取。
func (p *StorageSymbolProvider) GetSymbols(ctx context.Context, dataSource string, instType ...string) ([]string, error) {
	log.DebugContextf(ctx, "[StorageSymbolProvider] GetSymbols enter (dataSource=%s, instType=%v)",
		dataSource, instType)

	if p.xdataURL == "" {
		log.WarnContextf(ctx, "[StorageSymbolProvider] xData URL not configured, returning empty list (dataSource=%s)",
			dataSource)
		return []string{}, nil
	}

	// 确定产品类型（默认为 SPOT）
	var targetInstType string
	if len(instType) > 0 && instType[0] != "" {
		targetInstType = strings.ToUpper(instType[0])
	} else {
		targetInstType = "SPOT"
	}
	log.DebugContextf(ctx, "[StorageSymbolProvider] Using instType: %s (dataSource=%s)",
		targetInstType, dataSource)

	datasetID := symbolDataSetID(targetInstType)
	log.DebugContextf(ctx, "[StorageSymbolProvider] Mapped to datasetID=%s (dataSource=%s, instType=%s)",
		datasetID, dataSource, targetInstType)

	// 调用 xData FetchObject 接口
	symbols, err := p.fetchSymbolsFromStorage(ctx, datasetID)
	if err != nil {
		log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to fetch symbols from storage (dataSource=%s, instType=%s, datasetID=%s): %v",
			dataSource, targetInstType, datasetID, err)
		return []string{}, err
	}

	log.InfoContextf(ctx, "[StorageSymbolProvider] Fetched %d symbols (dataSource=%s, instType=%s, datasetID=%s)",
		len(symbols), dataSource, targetInstType, datasetID)
	return symbols, nil
}

func symbolDataSetID(instType string) string {
	switch instType {
	case "SWAP":
		return "binance_swap_symbols"
	case "SPOT":
		return "binance_spot_symbols"
	default:
		return "binance_spot_symbols"
	}
}

// fetchSymbolsFromStorage 从存储服务获取标的列表
// 使用 QueryRecords 接口翻页查询，每页 200 条。
func (p *StorageSymbolProvider) fetchSymbolsFromStorage(ctx context.Context, datasetID string) ([]string, error) {
	log.DebugContextf(ctx, "[StorageSymbolProvider] fetchSymbolsFromStorage enter (datasetID=%s)", datasetID)

	const pageSize = 200
	var allSymbols []string
	pageIdx := uint32(1)

	for {
		request := &QueryRecordsRequest{
			AuthInfo: AuthInfo{
				AppID:  "moox-server",
				AppKey: "symbol-provider",
			},
			DataRef: DataRef{
				WorkspaceID: "default",
				DatasetID:   datasetID,
				ExchangeID:  "BINANCE",
			},
			Page: Page{
				Page: pageIdx,
				Size: pageSize,
			},
		}

		// 序列化为 JSON
		data, err := json.Marshal(request)
		if err != nil {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to marshal request (datasetID=%s, page=%d): %v",
				datasetID, pageIdx, err)
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}

		url := fmt.Sprintf("%s/trpc.storage.data.DataService/QueryRecords", p.xdataURL)
		log.DebugContextf(ctx, "[StorageSymbolProvider] Sending request to %s (datasetID=%s, page=%d)", url, datasetID, pageIdx)

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(data))
		if err != nil {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to create request (datasetID=%s, page=%d): %v",
				datasetID, pageIdx, err)
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to send request (datasetID=%s, page=%d, url=%s): %v",
				datasetID, pageIdx, url, err)
			return nil, fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		// 读取响应
		var respBody bytes.Buffer
		_, _ = respBody.ReadFrom(resp.Body)

		if resp.StatusCode != http.StatusOK {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] HTTP error (datasetID=%s, page=%d, status=%d, body=%s)",
				datasetID, pageIdx, resp.StatusCode, respBody.String())
			return nil, fmt.Errorf("HTTP error: %d, body: %s", resp.StatusCode, respBody.String())
		}

		var queryResp QueryRecordsResponse
		if err := json.Unmarshal(respBody.Bytes(), &queryResp); err != nil {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to parse response (datasetID=%s, page=%d): %v",
				datasetID, pageIdx, err)
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		if queryResp.RetInfo.Code != 0 {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] API error (datasetID=%s, page=%d, code=%d, msg=%s)",
				datasetID, pageIdx, queryResp.RetInfo.Code, queryResp.RetInfo.Msg)
			return nil, fmt.Errorf("API error: code=%d, msg=%s", queryResp.RetInfo.Code, queryResp.RetInfo.Msg)
		}

		// 提取当前页的 symbol 列表，优先使用 record_id。
		pageCount := 0
		for _, row := range queryResp.Records {
			symbol := row.RecordID
			if symbol == "" {
				symbol = row.DataRef.InstrumentID
			}
			if symbol != "" {
				allSymbols = append(allSymbols, symbol)
				pageCount++
			}
		}

		log.DebugContextf(ctx, "[StorageSymbolProvider] Fetched page %d: got %d symbols (datasetID=%s, total=%d)",
			pageIdx, pageCount, datasetID, queryResp.PageResult.Total)

		// 检查是否已经获取完所有数据
		if !queryResp.PageResult.HasMore || uint64(len(allSymbols)) >= uint64(queryResp.PageResult.Total) {
			break
		}

		// 如果这一页的数据少于页大小，说明已经是最后一页
		if pageCount < pageSize {
			break
		}

		// 继续下一页
		pageIdx++
	}

	log.InfoContextf(ctx, "[StorageSymbolProvider] Successfully fetched %d symbols (datasetID=%s)",
		len(allSymbols), datasetID)
	uniqueSymbols := make([]string, 0, len(allSymbols))
	seen := make(map[string]struct{}, len(allSymbols))
	dupCount := 0
	for _, symbol := range allSymbols {
		if _, ok := seen[symbol]; ok {
			dupCount++
			continue
		}
		seen[symbol] = struct{}{}
		uniqueSymbols = append(uniqueSymbols, symbol)
	}
	if dupCount > 0 {
		log.WarnContextf(ctx, "[StorageSymbolProvider] Deduped %d duplicate symbols (datasetID=%s)",
			dupCount, datasetID)
	}
	return uniqueSymbols, nil
}

// QueryRecordsRequest QueryRecords 请求结构。
type QueryRecordsRequest struct {
	AuthInfo AuthInfo `json:"auth_info"`
	DataRef  DataRef  `json:"data_ref"`
	Page     Page     `json:"page"`
}

// DataRef 逻辑数据定位。
type DataRef struct {
	WorkspaceID  string `json:"workspace_id"`
	DatasetID    string `json:"dataset_id"`
	InstrumentID string `json:"instrument_id,omitempty"`
	ExchangeID   string `json:"exchange_id,omitempty"`
}

// Page 分页信息。
type Page struct {
	Page uint32 `json:"page"`
	Size uint32 `json:"size"`
}

// QueryRecordsResponse QueryRecords 响应结构。
type QueryRecordsResponse struct {
	RetInfo    RetInfo    `json:"ret_info"`
	Records    []Record   `json:"records"`
	PageResult PageResult `json:"page_result"`
}

// PageResult 分页结果。
type PageResult struct {
	Page    uint32      `json:"page"`
	Size    uint32      `json:"size"`
	Total   Uint64Value `json:"total"`
	HasMore bool        `json:"has_more"`
}

// Uint64Value 支持数字或字符串形式的 total。
type Uint64Value uint64

// UnmarshalJSON accepts numbers or quoted numbers.
func (u *Uint64Value) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*u = 0
		return nil
	}
	if data[0] == '"' && data[len(data)-1] == '"' {
		parsed, err := strconv.ParseUint(string(data[1:len(data)-1]), 10, 64)
		if err != nil {
			return err
		}
		*u = Uint64Value(parsed)
		return nil
	}
	parsed, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil {
		return err
	}
	*u = Uint64Value(parsed)
	return nil
}

// AuthInfo 鉴权信息。
type AuthInfo struct {
	AppID  string `json:"app_id"`
	AppKey string `json:"app_key"`
}

// Record 普通结构化记录。
type Record struct {
	RecordID string  `json:"record_id"`
	DataRef  DataRef `json:"data_ref"`
}

// RetInfo 返回信息。
type RetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}
