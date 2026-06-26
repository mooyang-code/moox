package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	TaskTypeUploadFileToCOS = "UPLOAD_FILE_TO_COS"
	TaskTypeCreateNode      = "CREATE_NODE"
)

// Client calls the Control HTTP API used by collector function publishing.
type Client struct {
	BaseURL     string
	AccessToken string
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
	RequestParams any    `json:"request_params"`
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

func BuildUploadPackageJobRequest(req UploadPackageJobRequest) AsyncJobCreateRequest {
	return AsyncJobCreateRequest{
		Tasks: []AsyncTaskRequest{{
			TaskType:      TaskTypeUploadFileToCOS,
			RequestParams: req,
		}},
	}
}

func BuildCreateNodeJobRequest(req CreateNodeJobRequest) AsyncJobCreateRequest {
	return AsyncJobCreateRequest{
		Tasks: []AsyncTaskRequest{{
			TaskType:      TaskTypeCreateNode,
			RequestParams: req,
		}},
	}
}

func (c *Client) CreateUploadPackageJob(ctx context.Context, req UploadPackageJobRequest) (*CreateJobResponse, error) {
	return c.createJob(ctx, BuildUploadPackageJobRequest(req))
}

func (c *Client) CreateNodeJob(ctx context.Context, req CreateNodeJobRequest) (*CreateJobResponse, error) {
	return c.createJob(ctx, BuildCreateNodeJobRequest(req))
}

func (c *Client) QueryJob(ctx context.Context, jobID string) (*JobQueryResult, error) {
	var resp unifiedResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/control/asynctask/QueryAsyncJob", map[string]string{"job_id": jobID}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("control response for job %s did not include data", jobID)
	}
	var result JobQueryResult
	if err := json.Unmarshal(resp.Data[0], &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) createJob(ctx context.Context, req AsyncJobCreateRequest) (*CreateJobResponse, error) {
	var resp unifiedResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/control/asynctask/CreateAsyncJob", req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("control response did not include job data")
	}
	var result CreateJobResponse
	if err := json.Unmarshal(resp.Data[0], &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	if c.BaseURL == "" {
		return fmt.Errorf("control url is required")
	}
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.AccessToken != "" {
		req.Header.Set("X-Access-Token", c.AccessToken)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	httpResp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("control returned HTTP %s", httpResp.Status)
	}
	if err := json.NewDecoder(httpResp.Body).Decode(out); err != nil {
		return err
	}
	if resp, ok := out.(*unifiedResponse); ok && resp.Code != 200 {
		return fmt.Errorf("control returned code %d: %s", resp.Code, resp.Message)
	}
	return nil
}
