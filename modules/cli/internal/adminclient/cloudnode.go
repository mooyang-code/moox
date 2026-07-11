package adminclient

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CloudAccount 云账户（脱敏，仅用于列举与取 account_id）。
type CloudAccount struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Provider    string `json:"provider"`
	AppID       string `json:"app_id"`
	COSRegion   string `json:"cos_region"`
	COSBucket   string `json:"cos_bucket"`
	IsDeleted   bool   `json:"is_deleted"`
}

// COSAccountInfo 云账户凭证信息（reveal=true 时含明文 secret_id/secret_key）。
type COSAccountInfo struct {
	AccountID string `json:"account_id"`
	Provider  string `json:"provider"`
	AppID     string `json:"app_id"`
	COSRegion string `json:"cos_region"`
	COSBucket string `json:"cos_bucket"`
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
}

type UploadPackageRequest struct {
	PackageName      string `json:"package_name"`
	Version          string `json:"version"`
	Description      string `json:"description,omitempty"`
	Runtime          string `json:"runtime"`
	PackageType      int    `json:"package_type"`
	BizType          string `json:"biz_type,omitempty"`
	OriginalFilename string `json:"original_filename,omitempty"`
	CloudAccountID   string `json:"cloud_account_id"`
}

// ResolvePackageType maps CLI-friendly names to proto PackageType enum values.
func ResolvePackageType(raw string) int {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "collector", "data_collector", "package_type_collector":
		return 1
	case "2", "factor", "factor_calculator", "package_type_factor":
		return 2
	case "3", "custom", "package_type_custom":
		return 3
	default:
		return 1
	}
}

type UploadPackageResponse struct {
	PackageID string `json:"package_id"`
}

type NodeCreateItem struct {
	CloudAccountID string            `json:"cloud_account_id"`
	NodeType       string            `json:"node_type"`
	Runtime        string            `json:"runtime,omitempty"`
	Handler        string            `json:"handler,omitempty"`
	Config         map[string]string `json:"config,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	Region         string            `json:"region"`
	Namespace      string            `json:"namespace,omitempty"`
	PackageID      string            `json:"package_id"`
	DeploymentID   string            `json:"deployment_id,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
}

type NodeDeployItem struct {
	NodeID    string `json:"node_id"`
	PackageID string `json:"package_id"`
}

type BatchChangeResponse struct {
	BatchID        string
	ProcessedCount int
}

type CloudNode struct {
	SpaceID      string         `json:"space_id"`
	NodeID       string         `json:"node_id"`
	FunctionName string         `json:"function_name"`
	Runtime      string         `json:"runtime"`
	Handler      string         `json:"handler"`
	Metadata     map[string]any `json:"metadata"`
	Status       int            `json:"status"`
}

type JobItem struct {
	SpaceID   string `json:"space_id"`
	JobItemID string `json:"job_item_id"`
	Status    int    `json:"status"`
}

// ListCloudAccounts 调 cloudnode/ListCloudAccounts，返回脱敏账户列表。
func (c *Client) ListCloudAccounts(ctx context.Context, provider string) ([]CloudAccount, error) {
	body := map[string]string{}
	if provider != "" {
		body["provider"] = provider
	}
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/ListCloudAccounts", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		RetInfo  *retInfo       `json:"ret_info"`
		Accounts []CloudAccount `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.RetInfo != nil && !isRetInfoSuccess(resp.RetInfo.Code) {
		return nil, fmt.Errorf("ListCloudAccounts: code %d: %s", resp.RetInfo.Code, resp.RetInfo.Msg)
	}
	return resp.Accounts, nil
}

// UploadPackage 两阶段上传：InitPackageUpload -> COS PUT -> CompletePackageUpload。
func (c *Client) UploadPackage(ctx context.Context, req UploadPackageRequest, data []byte) (*UploadPackageResponse, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/InitPackageUpload", req)
	if err != nil {
		return nil, err
	}
	var initResp struct {
		RetInfo   *retInfo `json:"ret_info"`
		PackageID string   `json:"package_id"`
		UploadURL string   `json:"upload_url"`
	}
	if err := json.Unmarshal(raw, &initResp); err != nil {
		return nil, err
	}
	if initResp.RetInfo != nil && !isRetInfoSuccess(initResp.RetInfo.Code) {
		return nil, fmt.Errorf("InitPackageUpload: code %d: %s", initResp.RetInfo.Code, initResp.RetInfo.Msg)
	}
	if initResp.PackageID == "" || initResp.UploadURL == "" {
		return nil, fmt.Errorf("InitPackageUpload: empty package_id or upload_url")
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, initResp.UploadURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return nil, fmt.Errorf("COS upload: %w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(putResp.Body, 512))
		return nil, fmt.Errorf("COS upload: status %d: %s", putResp.StatusCode, string(body))
	}

	sum := md5.Sum(data)
	completeBody := map[string]any{
		"package_id": initResp.PackageID,
		"file_md5":   hex.EncodeToString(sum[:]),
		"file_size":  len(data),
	}
	raw, err = c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/CompletePackageUpload", completeBody)
	if err != nil {
		return nil, err
	}
	var completeResp struct {
		RetInfo *retInfo `json:"ret_info"`
	}
	if err := json.Unmarshal(raw, &completeResp); err != nil {
		return nil, err
	}
	if completeResp.RetInfo != nil && !isRetInfoSuccess(completeResp.RetInfo.Code) {
		return nil, fmt.Errorf("CompletePackageUpload: code %d: %s", completeResp.RetInfo.Code, completeResp.RetInfo.Msg)
	}
	return &UploadPackageResponse{PackageID: initResp.PackageID}, nil
}

func (c *Client) BatchCreateNodes(ctx context.Context, nodes []NodeCreateItem) (*BatchChangeResponse, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/BatchCreateNodes", map[string]any{"space_id": c.SpaceID, "nodes": nodes})
	if err != nil {
		return nil, err
	}
	return parseBatchChangeResponse(raw, "BatchCreateNodes")
}

func (c *Client) BatchDeployNodes(ctx context.Context, deployments []NodeDeployItem) (*BatchChangeResponse, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/BatchDeployNodes", map[string]any{"space_id": c.SpaceID, "deployments": deployments})
	if err != nil {
		return nil, err
	}
	return parseBatchChangeResponse(raw, "BatchDeployNodes")
}

func (c *Client) ListNodes(ctx context.Context, page, size int) ([]CloudNode, bool, error) {
	return c.ListNodesWithTag(ctx, page, size, "")
}

func (c *Client) ListNodesWithTag(ctx context.Context, page, size int, tag string) ([]CloudNode, bool, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/GetNodeList", map[string]any{
		"space_id": c.SpaceID,
		"page":     map[string]int{"page": page, "size": size},
		"tag":      tag,
	})
	if err != nil {
		return nil, false, err
	}
	var resp struct {
		RetInfo *retInfo    `json:"ret_info"`
		Items   []CloudNode `json:"items"`
		Page    struct {
			HasMore bool `json:"has_more"`
		} `json:"page"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false, err
	}
	if resp.RetInfo != nil && !isRetInfoSuccess(resp.RetInfo.Code) {
		return nil, false, fmt.Errorf("GetNodeList: code %d: %s", resp.RetInfo.Code, resp.RetInfo.Msg)
	}
	return resp.Items, resp.Page.HasMore, nil
}

func (c *Client) BatchDeleteNodes(ctx context.Context, nodeIDs []string) (*BatchChangeResponse, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/BatchDeleteNodes", map[string]any{"space_id": c.SpaceID, "node_ids": nodeIDs})
	if err != nil {
		return nil, err
	}
	return parseBatchChangeResponse(raw, "BatchDeleteNodes")
}

func (c *Client) ListJobItems(ctx context.Context, status, page, size int) ([]JobItem, bool, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/ListJobItems", map[string]any{
		"space_id": c.SpaceID,
		"status":   status,
		"page":     map[string]int{"page": page, "size": size},
	})
	if err != nil {
		return nil, false, err
	}
	var resp struct {
		RetInfo *retInfo  `json:"ret_info"`
		Items   []JobItem `json:"items"`
		Page    struct {
			HasMore bool `json:"has_more"`
		} `json:"page"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false, err
	}
	if resp.RetInfo != nil && !isRetInfoSuccess(resp.RetInfo.Code) {
		return nil, false, fmt.Errorf("ListJobItems: code %d: %s", resp.RetInfo.Code, resp.RetInfo.Msg)
	}
	return resp.Items, resp.Page.HasMore, nil
}

func (c *Client) CancelJobItem(ctx context.Context, jobItemID string) error {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/CancelJobItem", map[string]string{"space_id": c.SpaceID, "job_item_id": jobItemID})
	if err != nil {
		return err
	}
	var resp struct {
		RetInfo *retInfo `json:"ret_info"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return err
	}
	if resp.RetInfo != nil && !isRetInfoSuccess(resp.RetInfo.Code) {
		return fmt.Errorf("CancelJobItem: code %d: %s", resp.RetInfo.Code, resp.RetInfo.Msg)
	}
	return nil
}

func parseBatchChangeResponse(raw []byte, method string) (*BatchChangeResponse, error) {
	var resp struct {
		RetInfo        *retInfo `json:"ret_info"`
		BatchID        string   `json:"batch_id"`
		ProcessedCount int      `json:"processed_count"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.RetInfo != nil && !isRetInfoSuccess(resp.RetInfo.Code) {
		return nil, fmt.Errorf("%s: code %d: %s", method, resp.RetInfo.Code, resp.RetInfo.Msg)
	}
	if resp.BatchID == "" {
		return nil, fmt.Errorf("%s: empty batch_id", method)
	}
	return &BatchChangeResponse{BatchID: resp.BatchID, ProcessedCount: resp.ProcessedCount}, nil
}

// GetCOSAccountInfo 调 cloudnode/GetCOSAccountInfo（reveal=true），返回明文凭证。
func (c *Client) GetCOSAccountInfo(ctx context.Context, accountID string) (*COSAccountInfo, error) {
	body := map[string]any{"account_id": accountID, "reveal": true}
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/GetCOSAccountInfo", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		RetInfo *retInfo        `json:"ret_info"`
		Secret  *COSAccountInfo `json:"secret"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.RetInfo != nil && !isRetInfoSuccess(resp.RetInfo.Code) {
		return nil, fmt.Errorf("GetCOSAccountInfo: code %d: %s", resp.RetInfo.Code, resp.RetInfo.Msg)
	}
	if resp.Secret == nil {
		return nil, fmt.Errorf("GetCOSAccountInfo: empty secret for %s", accountID)
	}
	return resp.Secret, nil
}
