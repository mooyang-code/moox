package adminclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	TaskTypeUploadFileToCOS = "UPLOAD_FILE_TO_COS"
	TaskTypeCreateNode      = "CREATE_NODE"
	TaskTypeDeployNode      = "DEPLOY_NODE"
)

// Client calls the Control HTTP API used by collector function publishing.
type Client struct {
	BaseURL     string
	AccessToken string
	// ServiceAuth 后台服务签名鉴权配置。设置后请求走 /api/service/{service}/{method}
	// 路由并使用 HMAC Auth 头，不再依赖用户登录态 X-Access-Token。
	ServiceAuth *ServiceAuthConfig
	HTTPClient  *http.Client
}

// New creates a Control API client. baseURL should point at the Control service root.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// UploadPackageJobRequest is the async UPLOAD_FILE_TO_COS task payload.
type UploadPackageJobRequest struct {
	PackageName    string `json:"package_name"`
	Version        string `json:"version"`
	Description    string `json:"description,omitempty"`
	Runtime        string `json:"runtime"`
	PackageType    string `json:"package_type"`
	BizType        string `json:"biz_type,omitempty"`
	CloudAccountID string `json:"cloud_account_id,omitempty"`
	FileContent    string `json:"file_content"`
}

// CreateNodeJobRequest is the async CREATE_NODE task payload.
type DeployNodeJobRequest struct {
	NodeID    string `json:"node_id"`
	PackageID string `json:"package_id"`
}

type CreateNodeJobRequest struct {
	CloudAccountID string            `json:"cloud_account_id"`
	NodeType       string            `json:"node_type"`
	Runtime        string            `json:"runtime,omitempty"`
	Handler        string            `json:"handler,omitempty"`
	BizType        string            `json:"biz_type,omitempty"`
	Config         map[string]string `json:"config,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	Region         string            `json:"region"`
	PackageID      string            `json:"package_id"`
	Metadata       string            `json:"metadata,omitempty"`
}

type AsyncJobCreateRequest struct {
	Tasks []AsyncTaskRequest `json:"tasks"`
}

type AsyncTaskRequest struct {
	TaskType      string `json:"task_type"`
	RequestParams string `json:"request_params"`
}

func marshalRequestParams(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal request_params: %w", err)
	}
	return string(raw), nil
}

func buildAsyncTaskRequest(taskType string, params any) (AsyncTaskRequest, error) {
	encoded, err := marshalRequestParams(params)
	if err != nil {
		return AsyncTaskRequest{}, err
	}
	return AsyncTaskRequest{
		TaskType:      taskType,
		RequestParams: encoded,
	}, nil
}

type CreateJobResponse struct {
	JobID        string `json:"job_id"`
	TotalTaskCnt int    `json:"total_task_cnt"`
}

type JobQueryResult struct {
	JobID          string            `json:"job_id"`
	JobStatus      int               `json:"job_status"`
	JobStatusText  string            `json:"job_status_text"`
	Progress       int               `json:"progress"`
	SuccessTaskCnt int               `json:"success_task_cnt"`
	FailedTaskCnt  int               `json:"failed_task_cnt"`
	Tasks          []TaskQueryResult `json:"tasks,omitempty"`
}

type TaskQueryResult struct {
	TaskID       string `json:"task_id"`
	TaskType     string `json:"task_type"`
	TaskStatus   int    `json:"task_status"`
	ResultData   string `json:"result_data,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type unifiedResponse struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    []json.RawMessage `json:"data"`
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func isRetInfoSuccess(code int) bool {
	return code == 0 || code == 200
}

func BuildUploadPackageJobRequest(req UploadPackageJobRequest) (AsyncJobCreateRequest, error) {
	task, err := buildAsyncTaskRequest(TaskTypeUploadFileToCOS, req)
	if err != nil {
		return AsyncJobCreateRequest{}, err
	}
	return AsyncJobCreateRequest{Tasks: []AsyncTaskRequest{task}}, nil
}

func BuildCreateNodeJobRequest(req CreateNodeJobRequest) (AsyncJobCreateRequest, error) {
	task, err := buildAsyncTaskRequest(TaskTypeCreateNode, req)
	if err != nil {
		return AsyncJobCreateRequest{}, err
	}
	return AsyncJobCreateRequest{Tasks: []AsyncTaskRequest{task}}, nil
}

func BuildDeployNodeJobRequest(req DeployNodeJobRequest) (AsyncJobCreateRequest, error) {
	task, err := buildAsyncTaskRequest(TaskTypeDeployNode, req)
	if err != nil {
		return AsyncJobCreateRequest{}, err
	}
	return AsyncJobCreateRequest{Tasks: []AsyncTaskRequest{task}}, nil
}

func (c *Client) CreateUploadPackageJob(ctx context.Context, req UploadPackageJobRequest) (*CreateJobResponse, error) {
	body, err := BuildUploadPackageJobRequest(req)
	if err != nil {
		return nil, err
	}
	return c.createJob(ctx, body)
}

func (c *Client) CreateNodeJob(ctx context.Context, req CreateNodeJobRequest) (*CreateJobResponse, error) {
	body, err := BuildCreateNodeJobRequest(req)
	if err != nil {
		return nil, err
	}
	return c.createJob(ctx, body)
}

func (c *Client) CreateDeployNodeJob(ctx context.Context, req DeployNodeJobRequest) (*CreateJobResponse, error) {
	body, err := BuildDeployNodeJobRequest(req)
	if err != nil {
		return nil, err
	}
	return c.createJob(ctx, body)
}

func (c *Client) QueryJob(ctx context.Context, jobID string) (*JobQueryResult, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/asynctask/QueryAsyncJob", map[string]string{"job_id": jobID})
	if err != nil {
		return nil, err
	}
	return parseQueryJobResponse(raw, jobID)
}

func (c *Client) createJob(ctx context.Context, req AsyncJobCreateRequest) (*CreateJobResponse, error) {
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/asynctask/CreateAsyncJob", req)
	if err != nil {
		return nil, err
	}
	return parseCreateJobResponse(raw)
}

func parseCreateJobResponse(raw []byte) (*CreateJobResponse, error) {
	var envelope unifiedResponse
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Code == 200 && len(envelope.Data) > 0 {
		var result CreateJobResponse
		if err := json.Unmarshal(envelope.Data[0], &result); err == nil && result.JobID != "" {
			return &result, nil
		}
	}
	var direct struct {
		RetInfo      *retInfo `json:"ret_info"`
		JobID        string   `json:"job_id"`
		TotalTaskCnt int      `json:"total_task_cnt"`
	}
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, err
	}
	if direct.RetInfo != nil && !isRetInfoSuccess(direct.RetInfo.Code) {
		return nil, fmt.Errorf("control returned ret_info code %d: %s", direct.RetInfo.Code, direct.RetInfo.Msg)
	}
	if direct.JobID == "" {
		return nil, fmt.Errorf("control response did not include job_id")
	}
	return &CreateJobResponse{
		JobID:        direct.JobID,
		TotalTaskCnt: direct.TotalTaskCnt,
	}, nil
}

func parseQueryJobResponse(raw []byte, jobID string) (*JobQueryResult, error) {
	var envelope unifiedResponse
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Code == 200 && len(envelope.Data) > 0 {
		var result JobQueryResult
		if err := json.Unmarshal(envelope.Data[0], &result); err == nil {
			return &result, nil
		}
	}
	var direct struct {
		RetInfo        *retInfo          `json:"ret_info"`
		JobID          string            `json:"job_id"`
		JobStatus      int               `json:"job_status"`
		JobStatusText  string            `json:"job_status_text"`
		Progress       int               `json:"progress"`
		SuccessTaskCnt int               `json:"success_task_cnt"`
		FailedTaskCnt  int               `json:"failed_task_cnt"`
		Tasks          []TaskQueryResult `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, err
	}
	if direct.RetInfo != nil && !isRetInfoSuccess(direct.RetInfo.Code) {
		return nil, fmt.Errorf("control returned ret_info code %d: %s", direct.RetInfo.Code, direct.RetInfo.Msg)
	}
	if direct.JobID == "" && jobID != "" {
		direct.JobID = jobID
	}
	if direct.JobID == "" {
		return nil, fmt.Errorf("control response for job %s did not include data", jobID)
	}
	return &JobQueryResult{
		JobID:          direct.JobID,
		JobStatus:      direct.JobStatus,
		JobStatusText:  direct.JobStatusText,
		Progress:       direct.Progress,
		SuccessTaskCnt: direct.SuccessTaskCnt,
		FailedTaskCnt:  direct.FailedTaskCnt,
		Tasks:          direct.Tasks,
	}, nil
}
func (c *Client) postJSON(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("control url is required")
	}
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	// 若配置了后台服务签名鉴权，则改走 /api/service/{service}/{method} 路由，
	// 并对原始请求体做 HMAC 签名放进 Auth 头，不再依赖用户登录态。
	finalPath := path
	var authHeader string
	if c.ServiceAuth != nil {
		finalPath = rewriteToServiceRoute(path)
		rawBody, _ := io.ReadAll(reader)
		reader = bytes.NewReader(rawBody)
		header, err := c.ServiceAuth.BuildAuthHeader(rawBody, time.Now())
		if err != nil {
			return nil, err
		}
		authHeader = header
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+finalPath, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Auth", authHeader)
	}
	if c.AccessToken != "" {
		req.Header.Set("X-Access-Token", c.AccessToken)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	httpResp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("control returned HTTP %s", httpResp.Status)
	}
	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	if trpcRet := httpResp.Header.Get("trpc-ret"); trpcRet != "" && trpcRet != "0" {
		msg := httpResp.Header.Get("trpc-func-ret")
		if msg == "" {
			msg = string(raw)
		}
		return nil, fmt.Errorf("control returned trpc-ret=%s: %s", trpcRet, msg)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("control returned empty response body")
	}
	return raw, nil
}

// rewriteToServiceRoute 将 /api/admin/{service}/{method} 改写为 /api/service/{service}/{method}。
// 仅识别 /api/admin/ 前缀；非该前缀的路径原样返回。
func rewriteToServiceRoute(path string) string {
	const controlPrefix = "/api/admin/"
	if strings.HasPrefix(path, controlPrefix) {
		return "/api/service/" + strings.TrimPrefix(path, controlPrefix)
	}
	return path
}
