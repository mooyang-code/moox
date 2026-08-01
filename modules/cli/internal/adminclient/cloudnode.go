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
	AccountID          string `json:"account_id"`
	AccountName        string `json:"account_name"`
	Provider           string `json:"provider"`
	CredentialSecretID string `json:"credential_secret_id"`
	AppID              string `json:"app_id"`
	COSRegion          string `json:"cos_region"`
	COSBucket          string `json:"cos_bucket"`
	IsDeleted          bool   `json:"is_deleted"`
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
	NodeID      string            `json:"node_id"`
	PackageID   string            `json:"package_id"`
	Config      map[string]string `json:"config,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

type CloudNode struct {
	NodeID         string         `json:"node_id"`
	CloudAccountID string         `json:"cloud_account_id"`
	PackageID      string         `json:"package_id"`
	Region         string         `json:"region"`
	NodeType       string         `json:"node_type"`
	BizType        string         `json:"biz_type"`
	FunctionName   string         `json:"function_name"`
	Metadata       map[string]any `json:"metadata"`
	Status         any            `json:"status"`
	LastHeartbeat  string         `json:"last_heartbeat"`
	IsDeleted      bool           `json:"is_deleted"`
}

type CloudNodeListFilter struct {
	CloudAccountID string
	Region         string
	NodeType       string
	BizType        string
}

type SubmitNodeBatchResponse struct {
	JobID      string `json:"job_id"`
	Operation  string `json:"operation"`
	TotalCount int    `json:"total_count"`
}

type NodeBatchSummary struct {
	JobID           string `json:"job_id"`
	Operation       string `json:"operation"`
	Status          string `json:"status"`
	TotalCount      int    `json:"total_count"`
	PendingCount    int    `json:"pending_count"`
	RunningCount    int    `json:"running_count"`
	SuccessCount    int    `json:"success_count"`
	FailedCount     int    `json:"failed_count"`
	ProgressPercent int    `json:"progress_percent"`
	CreatedAt       string `json:"created_at"`
	CompletedAt     string `json:"completed_at,omitempty"`
}

type NodeBatchItemResult struct {
	ItemID        string `json:"item_id"`
	NodeID        string `json:"node_id"`
	Status        string `json:"status"`
	ResultSummary string `json:"result_summary,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	CompletedAt   string `json:"completed_at,omitempty"`
}

type NodeBatchChangeResponse struct {
	Job   *NodeBatchSummary     `json:"job"`
	Items []NodeBatchItemResult `json:"items"`
}

var (
	nodeBatchOperationNames = map[int]string{
		0: "NODE_BATCH_OPERATION_UNSPECIFIED",
		1: "NODE_BATCH_OPERATION_CREATE_NODES",
		2: "NODE_BATCH_OPERATION_DEPLOY_NODES",
		3: "NODE_BATCH_OPERATION_DELETE_NODES",
	}
	nodeBatchStatusNames = map[int]string{
		0: "NODE_BATCH_STATUS_UNSPECIFIED",
		1: "NODE_BATCH_STATUS_PENDING",
		2: "NODE_BATCH_STATUS_RUNNING",
		3: "NODE_BATCH_STATUS_SUCCESS",
		4: "NODE_BATCH_STATUS_FAILED",
		5: "NODE_BATCH_STATUS_PARTIAL",
	}
	nodeBatchItemStatusNames = map[int]string{
		0: "NODE_BATCH_ITEM_STATUS_UNSPECIFIED",
		1: "NODE_BATCH_ITEM_STATUS_PENDING",
		2: "NODE_BATCH_ITEM_STATUS_RUNNING",
		3: "NODE_BATCH_ITEM_STATUS_SUCCESS",
		4: "NODE_BATCH_ITEM_STATUS_FAILED",
	}
)

func (s *NodeBatchSummary) UnmarshalJSON(data []byte) error {
	var wire struct {
		JobID           string `json:"job_id"`
		Operation       any    `json:"operation"`
		Status          any    `json:"status"`
		TotalCount      int    `json:"total_count"`
		PendingCount    int    `json:"pending_count"`
		RunningCount    int    `json:"running_count"`
		SuccessCount    int    `json:"success_count"`
		FailedCount     int    `json:"failed_count"`
		ProgressPercent int    `json:"progress_percent"`
		CreatedAt       string `json:"created_at"`
		CompletedAt     string `json:"completed_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	operation, err := normalizeProtoEnum(wire.Operation, nodeBatchOperationNames)
	if err != nil {
		return fmt.Errorf("node batch operation: %w", err)
	}
	status, err := normalizeProtoEnum(wire.Status, nodeBatchStatusNames)
	if err != nil {
		return fmt.Errorf("node batch status: %w", err)
	}
	*s = NodeBatchSummary{
		JobID: wire.JobID, Operation: operation, Status: status,
		TotalCount: wire.TotalCount, PendingCount: wire.PendingCount,
		RunningCount: wire.RunningCount, SuccessCount: wire.SuccessCount,
		FailedCount: wire.FailedCount, ProgressPercent: wire.ProgressPercent,
		CreatedAt: wire.CreatedAt, CompletedAt: wire.CompletedAt,
	}
	return nil
}

func (item *NodeBatchItemResult) UnmarshalJSON(data []byte) error {
	var wire struct {
		ItemID        string `json:"item_id"`
		NodeID        string `json:"node_id"`
		Status        any    `json:"status"`
		ResultSummary string `json:"result_summary"`
		ErrorMessage  string `json:"error_message"`
		StartedAt     string `json:"started_at"`
		CompletedAt   string `json:"completed_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	status, err := normalizeProtoEnum(wire.Status, nodeBatchItemStatusNames)
	if err != nil {
		return fmt.Errorf("node batch item status: %w", err)
	}
	*item = NodeBatchItemResult{
		ItemID: wire.ItemID, NodeID: wire.NodeID, Status: status,
		ResultSummary: wire.ResultSummary, ErrorMessage: wire.ErrorMessage,
		StartedAt: wire.StartedAt, CompletedAt: wire.CompletedAt,
	}
	return nil
}

func normalizeProtoEnum(value any, names map[int]string) (string, error) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", nil
		}
		return typed, nil
	case float64:
		number := int(typed)
		if typed != float64(number) {
			return "", fmt.Errorf("invalid numeric enum %v", typed)
		}
		name, ok := names[number]
		if !ok {
			return "", fmt.Errorf("unknown numeric enum %d", number)
		}
		return name, nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("unsupported enum value %T", value)
	}
}

// ListCloudNodes returns every catalog node matching the server-side fleet filters.
func (c *Client) ListCloudNodes(ctx context.Context, filter CloudNodeListFilter) ([]CloudNode, error) {
	const pageSize = 500
	var nodes []CloudNode
	for page := 1; ; page++ {
		raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/GetNodeList", map[string]any{
			"cloud_account_id": filter.CloudAccountID,
			"region":           filter.Region,
			"node_type":        filter.NodeType,
			"biz_type":         filter.BizType,
			"page":             map[string]int{"page": page, "size": pageSize},
		})
		if err != nil {
			return nil, err
		}
		var resp struct {
			RetInfo *retInfo    `json:"ret_info"`
			Items   []CloudNode `json:"items"`
			Page    struct {
				HasMore bool `json:"has_more"`
			} `json:"page"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		if resp.RetInfo == nil || !isRetInfoSuccess(resp.RetInfo.Code) {
			return nil, fmt.Errorf("GetNodeList rejected")
		}
		nodes = append(nodes, resp.Items...)
		if !resp.Page.HasMore {
			return nodes, nil
		}
	}
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

func (c *Client) SubmitCreateNodes(ctx context.Context, nodes []NodeCreateItem) (*SubmitNodeBatchResponse, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/SubmitCreateNodes", map[string]any{"nodes": nodes})
	if err != nil {
		return nil, err
	}
	return parseSubmitNodeBatchResponse(raw, "SubmitCreateNodes")
}

func (c *Client) SubmitDeployNodes(ctx context.Context, deployments []NodeDeployItem) (*SubmitNodeBatchResponse, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/SubmitDeployNodes", map[string]any{"deployments": deployments})
	if err != nil {
		return nil, err
	}
	return parseSubmitNodeBatchResponse(raw, "SubmitDeployNodes")
}

// SubmitDeleteNodes submits durable remote SCF deletion tasks for catalog nodes.
func (c *Client) SubmitDeleteNodes(ctx context.Context, nodeIDs []string) (*SubmitNodeBatchResponse, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/SubmitDeleteNodes", map[string]any{"node_ids": nodeIDs})
	if err != nil {
		return nil, err
	}
	return parseSubmitNodeBatchResponse(raw, "SubmitDeleteNodes")
}

func (c *Client) GetNodeBatchChange(ctx context.Context, jobID string) (*NodeBatchChangeResponse, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("GetNodeBatchChange: job_id is required")
	}
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/GetNodeBatchChange", map[string]any{"job_id": jobID})
	if err != nil {
		return nil, err
	}
	var resp struct {
		RetInfo *retInfo              `json:"ret_info"`
		Job     *NodeBatchSummary     `json:"job"`
		Items   []NodeBatchItemResult `json:"items"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if err := validateCloudNodeRetInfo(resp.RetInfo, "GetNodeBatchChange"); err != nil {
		return nil, err
	}
	if resp.Job == nil || strings.TrimSpace(resp.Job.JobID) == "" {
		return nil, fmt.Errorf("GetNodeBatchChange: empty job")
	}
	return &NodeBatchChangeResponse{Job: resp.Job, Items: resp.Items}, nil
}

func parseSubmitNodeBatchResponse(raw []byte, method string) (*SubmitNodeBatchResponse, error) {
	var resp struct {
		RetInfo    *retInfo `json:"ret_info"`
		JobID      string   `json:"job_id"`
		Operation  any      `json:"operation"`
		TotalCount int      `json:"total_count"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if err := validateCloudNodeRetInfo(resp.RetInfo, method); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.JobID) == "" {
		return nil, fmt.Errorf("%s: empty job_id", method)
	}
	operation, err := normalizeProtoEnum(resp.Operation, nodeBatchOperationNames)
	if err != nil {
		return nil, fmt.Errorf("%s: operation: %w", method, err)
	}
	return &SubmitNodeBatchResponse{
		JobID:      resp.JobID,
		Operation:  operation,
		TotalCount: resp.TotalCount,
	}, nil
}

func validateCloudNodeRetInfo(info *retInfo, method string) error {
	if info == nil {
		return fmt.Errorf("%s: missing ret_info", method)
	}
	if !isRetInfoSuccess(info.Code) {
		return fmt.Errorf("%s: code %d: %s", method, info.Code, info.Msg)
	}
	return nil
}
