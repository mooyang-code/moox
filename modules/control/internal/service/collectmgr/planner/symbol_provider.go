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

	"github.com/mooyang-code/moox/modules/control/internal/config"
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
// 从 xData 存储服务的 FetchObject 接口获取
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

	// 根据产品类型确定 datasetID
	// SWAP = 100, SPOT = 101
	var datasetID int32
	switch targetInstType {
	case "SWAP":
		datasetID = 100
	case "SPOT":
		datasetID = 101
	default:
		datasetID = 101 // 默认 SPOT
	}
	log.DebugContextf(ctx, "[StorageSymbolProvider] Mapped to datasetID=%d (dataSource=%s, instType=%s)",
		datasetID, dataSource, targetInstType)

	// 调用 xData FetchObject 接口
	symbols, err := p.fetchSymbolsFromStorage(ctx, datasetID)
	if err != nil {
		log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to fetch symbols from storage (dataSource=%s, instType=%s, datasetID=%d): %v",
			dataSource, targetInstType, datasetID, err)
		return []string{}, err
	}

	log.InfoContextf(ctx, "[StorageSymbolProvider] Fetched %d symbols (dataSource=%s, instType=%s, datasetID=%d)",
		len(symbols), dataSource, targetInstType, datasetID)
	return symbols, nil
}

// fetchSymbolsFromStorage 从存储服务获取标的列表
// 使用 QueryObject 接口翻页查询，每页 200 条
func (p *StorageSymbolProvider) fetchSymbolsFromStorage(ctx context.Context, datasetID int32) ([]string, error) {
	log.DebugContextf(ctx, "[StorageSymbolProvider] fetchSymbolsFromStorage enter (datasetID=%d)", datasetID)

	const pageSize = 200
	var allSymbols []string
	pageIdx := uint32(1)

	for {
		// 构建 QueryObject 请求
		request := &QueryObjectRequest{
			AuthInfo: AuthInfo{
				AppID:  "moox-server",
				AppKey: "symbol-provider",
			},
			ProjectID: 1,
			DatasetID: datasetID,
			PageInfo: PageInfo{
				PageIdx: pageIdx,
				Size:    pageSize,
			},
		}

		// 序列化为 JSON
		data, err := json.Marshal(request)
		if err != nil {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to marshal request (datasetID=%d, page=%d): %v",
				datasetID, pageIdx, err)
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}

		// 发送 HTTP POST 请求
		url := fmt.Sprintf("%s/trpc.storage.access.Access/QueryObject", p.xdataURL)
		log.DebugContextf(ctx, "[StorageSymbolProvider] Sending request to %s (datasetID=%d, page=%d)", url, datasetID, pageIdx)

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(data))
		if err != nil {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to create request (datasetID=%d, page=%d): %v",
				datasetID, pageIdx, err)
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to send request (datasetID=%d, page=%d, url=%s): %v",
				datasetID, pageIdx, url, err)
			return nil, fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		// 读取响应
		var respBody bytes.Buffer
		_, _ = respBody.ReadFrom(resp.Body)

		if resp.StatusCode != http.StatusOK {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] HTTP error (datasetID=%d, page=%d, status=%d, body=%s)",
				datasetID, pageIdx, resp.StatusCode, respBody.String())
			return nil, fmt.Errorf("HTTP error: %d, body: %s", resp.StatusCode, respBody.String())
		}

		// 解析响应
		var queryResp QueryObjectResponse
		if err := json.Unmarshal(respBody.Bytes(), &queryResp); err != nil {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to parse response (datasetID=%d, page=%d): %v",
				datasetID, pageIdx, err)
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		if queryResp.RetInfo.Code != 0 {
			log.ErrorContextf(ctx, "[StorageSymbolProvider] API error (datasetID=%d, page=%d, code=%d, msg=%s)",
				datasetID, pageIdx, queryResp.RetInfo.Code, queryResp.RetInfo.Msg)
			return nil, fmt.Errorf("API error: code=%d, msg=%s", queryResp.RetInfo.Code, queryResp.RetInfo.Msg)
		}

		// 提取当前页的 symbol 列表（object_id 就是 symbol）
		pageCount := 0
		for _, row := range queryResp.ObjectRows {
			if row.ObjectID != "" {
				allSymbols = append(allSymbols, row.ObjectID)
				pageCount++
			}
		}

		log.DebugContextf(ctx, "[StorageSymbolProvider] Fetched page %d: got %d symbols (datasetID=%d, total=%d)",
			pageIdx, pageCount, datasetID, uint64(queryResp.Total))

		// 检查是否已经获取完所有数据
		if uint64(len(allSymbols)) >= uint64(queryResp.Total) {
			break
		}

		// 如果这一页的数据少于页大小，说明已经是最后一页
		if pageCount < pageSize {
			break
		}

		// 继续下一页
		pageIdx++
	}

	log.InfoContextf(ctx, "[StorageSymbolProvider] Successfully fetched %d symbols (datasetID=%d)",
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
		log.WarnContextf(ctx, "[StorageSymbolProvider] Deduped %d duplicate symbols (datasetID=%d)",
			dupCount, datasetID)
	}
	return uniqueSymbols, nil
}

// QueryObjectRequest QueryObject 请求结构
type QueryObjectRequest struct {
	AuthInfo  AuthInfo  `json:"auth_info"`
	ProjectID int32     `json:"project_id"`
	DatasetID int32     `json:"dataset_id"`
	PageInfo  PageInfo  `json:"page_info"`
}

// PageInfo 分页信息
type PageInfo struct {
	PageIdx uint32 `json:"page_idx"` // 页数(从1开始计数)
	Size    uint32 `json:"size"`     // 页大小
}

// QueryObjectResponse QueryObject 响应结构
type QueryObjectResponse struct {
	RetInfo    RetInfo     `json:"ret_info"`
	Total      Uint64Value `json:"total"`
	ObjectRows []ObjectRow `json:"object_rows"`
}

// Uint64Value supports numeric or string totals.
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

// AuthInfo 鉴权信息
type AuthInfo struct {
	AppID  string `json:"app_id"`
	AppKey string `json:"app_key"`
}

// ObjectRow 对象行
type ObjectRow struct {
	ObjectID string                 `json:"object_id"`
	Fields   map[string]interface{} `json:"fields,omitempty"`
}

// RetInfo 返回信息
type RetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}
