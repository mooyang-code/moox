package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/server/internal/config"
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
	if p.xdataURL == "" {
		log.WarnContext(ctx, "[StorageSymbolProvider] xData URL not configured, returning empty list")
		return []string{}, nil
	}

	// 确定产品类型（默认为 SPOT）
	var targetInstType string
	if len(instType) > 0 && instType[0] != "" {
		targetInstType = strings.ToUpper(instType[0])
	} else {
		targetInstType = "SPOT"
	}

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

	// 调用 xData FetchObject 接口
	symbols, err := p.fetchSymbolsFromStorage(ctx, datasetID)
	if err != nil {
		log.ErrorContextf(ctx, "[StorageSymbolProvider] Failed to fetch symbols from storage: %v", err)
		return []string{}, err
	}

	log.InfoContextf(ctx, "[StorageSymbolProvider] Fetched %d symbols for %s/%s", len(symbols), dataSource, targetInstType)
	return symbols, nil
}

// fetchSymbolsFromStorage 从存储服务获取标的列表
func (p *StorageSymbolProvider) fetchSymbolsFromStorage(ctx context.Context, datasetID int32) ([]string, error) {
	// 构建 FetchObject 请求
	request := &FetchObjectRequest{
		AuthInfo: AuthInfo{
			AppID:  "moox-server",
			AppKey: "symbol-provider",
		},
		ProjectID: 1,
		DatasetID: datasetID,
		FieldKeys: []string{"symbol"}, // 只需要 symbol 字段
	}

	// 序列化为 JSON
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 发送 HTTP POST 请求
	url := fmt.Sprintf("%s/trpc.storage.access.Access/FetchObject", p.xdataURL)
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

	// 读取响应
	var respBody bytes.Buffer
	_, _ = respBody.ReadFrom(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %d, body: %s", resp.StatusCode, respBody.String())
	}

	// 解析响应
	var fetchResp FetchObjectResponse
	if err := json.Unmarshal(respBody.Bytes(), &fetchResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if fetchResp.RetInfo.Code != 0 {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", fetchResp.RetInfo.Code, fetchResp.RetInfo.Msg)
	}

	// 提取 symbol 列表（object_id 就是 symbol）
	var symbols []string
	for _, row := range fetchResp.ObjectRows {
		if row.ObjectID != "" {
			symbols = append(symbols, row.ObjectID)
		}
	}

	return symbols, nil
}

// FetchObjectRequest FetchObject 请求结构
type FetchObjectRequest struct {
	AuthInfo  AuthInfo `json:"auth_info"`
	ProjectID int32    `json:"project_id"`
	DatasetID int32    `json:"dataset_id"`
	FieldKeys []string `json:"field_keys,omitempty"`
}

// AuthInfo 鉴权信息
type AuthInfo struct {
	AppID  string `json:"app_id"`
	AppKey string `json:"app_key"`
}

// FetchObjectResponse FetchObject 响应结构
type FetchObjectResponse struct {
	RetInfo    RetInfo     `json:"ret_info"`
	ObjectRows []ObjectRow `json:"object_rows"`
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
