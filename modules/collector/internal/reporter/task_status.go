package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskStatus 任务状态常量
const (
	StatusPending = 1 // 待执行
	StatusSuccess = 2 // 成功
	StatusFailed  = 3 // 失败
)

// ReportTaskStatusRequest 上报任务状态请求
type ReportTaskStatusRequest struct {
	SpaceID   string         `json:"space_id"`
	TaskID    string         `json:"task_id"`
	JobItemID string         `json:"job_item_id"`
	NodeID    string         `json:"node_id"`
	Status    int            `json:"status"`
	Result    map[string]any `json:"result"`
}

// TaskStatusServerResponse 服务端响应结构
type TaskStatusServerResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    []any  `json:"data"`
	RetInfo *struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	} `json:"ret_info"`
}

// ReportTaskStatus 上报任务状态到服务端
func ReportTaskStatus(
	ctx context.Context,
	spaceID string,
	taskID string,
	jobItemID string,
	deliveryCount uint64,
	status int,
	result string,
) (reportErr error) {
	nodeID, _ := runtimeapp.GetNodeInfo()
	reportOutcome := "success"
	defer func() {
		if reportErr != nil {
			reportOutcome = "failed"
		}
		line := taskStatusLogLine(
			spaceID, taskID, jobItemID, nodeID, deliveryCount, status, reportOutcome, reportErr,
		)
		if reportErr != nil {
			log.ErrorContextf(ctx, "%s", line)
			return
		}
		log.InfoContextf(ctx, "%s", line)
	}()

	if taskID == "" {
		return fmt.Errorf("task_id is required for task status report")
	}

	if spaceID == "" {
		return fmt.Errorf("space_id is required for task status report")
	}

	if jobItemID == "" {
		return fmt.Errorf("job_item_id is required for task status report")
	}

	serviceGatewayTarget := runtimeapp.GetServiceGatewayTarget()

	// 检查服务端配置
	if serviceGatewayTarget == "" {
		reportOutcome = "skipped"
		log.DebugContextf(ctx, "service gateway target 未配置，跳过任务状态上报: taskID=%s jobItemID=%s",
			taskID, jobItemID)
		return nil
	}

	if nodeID == "" {
		return fmt.Errorf("node_id is required for task status report")
	}

	log.DebugContextf(ctx, "开始上报任务状态: spaceID=%s, taskID=%s, jobItemID=%s, nodeID=%s, status=%d, service_gateway_target=%s",
		spaceID, taskID, jobItemID, nodeID, status, serviceGatewayTarget)

	return executeTaskStatusReport(ctx, spaceID, taskID, jobItemID, nodeID, status, result, serviceGatewayTarget)
}

func taskStatusLogLine(
	spaceID string,
	taskID string,
	jobItemID string,
	nodeID string,
	deliveryCount uint64,
	taskStatus int,
	reportStatus string,
	err error,
) string {
	errorCode := ""
	if err != nil {
		errorCode = "TASK_INSTANCE_REPORT_FAILED"
	}
	return fmt.Sprintf(
		"event=%s space_id=%s job_item_id=%s task_id=%s runtime_code_package_id=%s "+
			"node_id=%s delivery_count=%d task_status=%d status=%s error_code=%s error=%s",
		strconv.Quote("collector_job_instance_reported"),
		strconv.Quote(strings.TrimSpace(spaceID)),
		strconv.Quote(strings.TrimSpace(jobItemID)),
		strconv.Quote(strings.TrimSpace(taskID)),
		strconv.Quote(strings.TrimSpace(os.Getenv("MOOX_CODE_PACKAGE_ID"))),
		strconv.Quote(strings.TrimSpace(nodeID)),
		deliveryCount,
		taskStatus,
		strconv.Quote(strings.TrimSpace(reportStatus)),
		strconv.Quote(errorCode),
		strconv.Quote(taskStatusError(err)),
	)
}

func taskStatusError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Join(strings.Fields(err.Error()), " ")
	const maxErrorBytes = 256
	if len(value) > maxErrorBytes {
		value = value[:maxErrorBytes]
	}
	return value
}

// executeTaskStatusReport 执行上报请求
func executeTaskStatusReport(
	ctx context.Context,
	spaceID string,
	taskID string,
	jobItemID string,
	nodeID string,
	status int,
	result string,
	serviceGatewayTarget string,
) error {
	url := runtimeapp.ServiceURL(serviceGatewayTarget, "collectmgr", "ReportTaskStatus")

	// 构建请求体
	reqBody := &ReportTaskStatusRequest{
		SpaceID:   spaceID,
		TaskID:    taskID,
		JobItemID: jobItemID,
		NodeID:    nodeID,
		Status:    status,
		Result:    resultMap(result),
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建 HTTP 客户端
	httpClient, err := runtimeapp.NewGatewayHTTPClient(5*time.Second, runtimeapp.DefaultAuthConfig())
	if err != nil {
		return err
	}

	// 使用重试机制发送请求
	err = retry.Do(
		func() error {
			return sendRequest(ctx, url, data, httpClient)
		},
		retry.Attempts(3),
		retry.Delay(500*time.Millisecond),
		retry.DelayType(retry.BackOffDelay),
		retry.LastErrorOnly(true),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "重试任务状态上报, attempt: %d, taskID: %s, error: %v", n+1, taskID, err)
		}),
		retry.Context(ctx),
	)

	if err != nil {
		return fmt.Errorf("上报任务状态失败: %w", err)
	}

	log.DebugContextf(ctx, "任务状态上报成功: taskID=%s, status=%d", taskID, status)
	return nil
}

func resultMap(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err == nil && out != nil {
		return out
	}
	return map[string]any{"message": raw}
}

// sendRequest 发送单次请求
func sendRequest(ctx context.Context, url string, data []byte, httpClient *http.Client) error {
	req, err := runtimeapp.NewSignedRequestWithContext(ctx, "POST", url, data, runtimeapp.DefaultAuthConfig())
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("请求失败, status: %d", resp.StatusCode)
	}

	// 解析响应验证是否成功
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}

	if len(respData) == 0 {
		return nil
	}

	var serverResp TaskStatusServerResponse
	if err := json.Unmarshal(respData, &serverResp); err != nil {
		return fmt.Errorf("解析服务端响应失败: %w", err)
	}

	if serverResp.RetInfo != nil {
		if serverResp.RetInfo.Code != 0 {
			return fmt.Errorf("服务端返回错误: code=%d, message=%s", serverResp.RetInfo.Code, serverResp.RetInfo.Msg)
		}
		return nil
	}

	if serverResp.Code != 200 {
		return fmt.Errorf("服务端返回错误: code=%d, message=%s", serverResp.Code, serverResp.Message)
	}

	return nil
}
