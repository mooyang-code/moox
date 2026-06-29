package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"trpc.group/trpc-go/trpc-go/log"
)

const defaultSymbolSpaceID = "crypto"

// StorageSymbolProvider 从 storage Metadata 服务获取标的列表。
type StorageSymbolProvider struct {
	metadataURL string
	spaceID     string
}

// NewStorageSymbolProvider 创建存储标的提供者。
func NewStorageSymbolProvider() *StorageSymbolProvider {
	return &StorageSymbolProvider{
		metadataURL: config.GetMetadataURL(),
		spaceID:     defaultSymbolSpaceID,
	}
}

// GetSymbols 获取指定数据源和产品类型的所有标的。
// 通过 Metadata ListDatasetSubjects 查询 K 线数据集绑定的 subject_id。
func (p *StorageSymbolProvider) GetSymbols(ctx context.Context, dataSource string, instType ...string) ([]string, error) {
	log.DebugContextf(ctx, "[StorageSymbolProvider] GetSymbols enter (dataSource=%s, instType=%v)",
		dataSource, instType)

	if p.metadataURL == "" {
		log.WarnContextf(ctx, "[StorageSymbolProvider] metadata URL not configured, returning empty list (dataSource=%s)",
			dataSource)
		return []string{}, nil
	}

	targetInstType := "SPOT"
	if len(instType) > 0 && instType[0] != "" {
		targetInstType = strings.ToUpper(instType[0])
	}

	datasetID := symbolKlineDatasetID(targetInstType)
	log.DebugContextf(ctx, "[StorageSymbolProvider] Using datasetID=%s (dataSource=%s, instType=%s)",
		datasetID, dataSource, targetInstType)

	symbols, err := p.fetchSymbolsFromMetadata(ctx, datasetID)
	if err != nil {
		log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to fetch symbols from metadata (dataSource=%s, instType=%s, datasetID=%s): %v",
			dataSource, targetInstType, datasetID, err)
		return []string{}, err
	}

	log.InfoContextf(ctx, "[StorageSymbolProvider] Fetched %d symbols (dataSource=%s, instType=%s, datasetID=%s)",
		len(symbols), dataSource, targetInstType, datasetID)
	return symbols, nil
}

func symbolKlineDatasetID(instType string) string {
	switch instType {
	case "SWAP":
		return "binance_swap_kline"
	default:
		return "binance_spot_kline"
	}
}

func (p *StorageSymbolProvider) fetchSymbolsFromMetadata(ctx context.Context, datasetID string) ([]string, error) {
	const pageSize = 200
	var allSymbols []string
	cursor := ""

	for {
		request := &ListDatasetSubjectsRequest{
			AuthInfo: AuthInfo{
				AppID:  "moox-admin",
				AppKey: "symbol-provider",
			},
			SpaceID:   p.spaceID,
			DatasetID: datasetID,
			Page: Page{
				Size:   pageSize,
				Cursor: cursor,
			},
		}

		data, err := json.Marshal(request)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}

		url := fmt.Sprintf("%s/trpc.moox.storage.Metadata/ListDatasetSubjects", p.metadataURL)
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(data))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		var respBody bytes.Buffer
		_, _ = respBody.ReadFrom(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP error: %d, body: %s", resp.StatusCode, respBody.String())
		}

		var listResp ListDatasetSubjectsResponse
		if err := json.Unmarshal(respBody.Bytes(), &listResp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		if listResp.RetInfo.Code != 0 {
			return nil, fmt.Errorf("API error: code=%d, msg=%s", listResp.RetInfo.Code, listResp.RetInfo.Msg)
		}

		pageCount := 0
		for _, item := range listResp.DatasetSubjects {
			if item.SubjectID != "" {
				allSymbols = append(allSymbols, item.SubjectID)
				pageCount++
			}
		}

		log.DebugContextf(ctx, "[StorageSymbolProvider] Fetched page cursor=%q: got %d symbols (datasetID=%s, total=%d)",
			cursor, pageCount, datasetID, listResp.PageResult.Total)

		if !listResp.PageResult.HasMore || listResp.PageResult.NextCursor == "" {
			break
		}
		cursor = listResp.PageResult.NextCursor
	}

	uniqueSymbols := make([]string, 0, len(allSymbols))
	seen := make(map[string]struct{}, len(allSymbols))
	for _, symbol := range allSymbols {
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		uniqueSymbols = append(uniqueSymbols, symbol)
	}
	return uniqueSymbols, nil
}

// ListDatasetSubjectsRequest Metadata ListDatasetSubjects 请求。
type ListDatasetSubjectsRequest struct {
	AuthInfo  AuthInfo `json:"auth_info"`
	SpaceID   string   `json:"space_id"`
	DatasetID string   `json:"dataset_id"`
	Page      Page     `json:"page"`
}

// ListDatasetSubjectsResponse Metadata ListDatasetSubjects 响应。
type ListDatasetSubjectsResponse struct {
	RetInfo         RetInfo          `json:"ret_info"`
	DatasetSubjects []DatasetSubject `json:"dataset_subjects"`
	PageResult      PageResult       `json:"page_result"`
}

// DatasetSubject 数据集与 subject 绑定关系。
type DatasetSubject struct {
	SubjectID string `json:"subject_id"`
	Status    string `json:"status"`
}

// Page 分页信息。
type Page struct {
	Page   uint32 `json:"page,omitempty"`
	Size   uint32 `json:"size"`
	Cursor string `json:"cursor,omitempty"`
}

// PageResult 分页结果。
type PageResult struct {
	Total      uint32 `json:"total"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// AuthInfo 鉴权信息。
type AuthInfo struct {
	AppID  string `json:"app_id"`
	AppKey string `json:"app_key"`
}

// RetInfo 返回信息。
type RetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}
