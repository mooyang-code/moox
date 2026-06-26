package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/controlapi"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskStatus 任务状态常量
const (
	StatusPending = 0 // 待执行
	StatusRunning = 1 // 执行中
	StatusSuccess = 2 // 成功
	StatusPartial = 3 // 部分失败
	StatusFailed  = 4 // 失败
)

// ReportTaskStatusRequest 上报任务状态请求
type ReportTaskStatusRequest struct {
	ID     string `json:"id"`      // 任务实例ID（TaskID）
	NodeID string `json:"node_id"` // 执行节点ID
	Status int    `json:"status"`  // 状态码
	Result string `json:"result"`  // 执行结果（可选）
}

// ServerResponse 服务端响应结构
type ServerResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    []any  `json:"data"`
}

// ReportTaskStatusAsync 异步上报任务状态（失败只记录日志，不影响主流程）
func ReportTaskStatusAsync(ctx context.Context, taskID string, status int, result string) {
	go func() {
		if err := ReportTaskStatus(ctx, taskID, status, result); err != nil {
			log.WarnContextf(ctx, "任务状态上报失败: taskID=%s, status=%d, error=%v", taskID, status, err)
		}
	}()
}

// ReportTaskStatus 上报任务状态到服务端
func ReportTaskStatus(ctx context.Context, taskID string, status int, result string) error {
	serverIP, serverPort := config.GetServerInfo()
	nodeID, _ := config.GetNodeInfo()

	// 检查服务端配置
	if serverIP == "" || serverPort <= 0 {
		log.DebugContextf(ctx, "服务端地址未配置，跳过任务状态上报: taskID=%s", taskID)
		return nil
	}

	// 检查 TaskID
	if taskID == "" {
		log.WarnContextf(ctx, "TaskID 为空，跳过任务状态上报")
		return nil
	}

	if nodeID == "" {
		return fmt.Errorf("node_id is required for task status report")
	}

	log.DebugContextf(ctx, "开始上报任务状态: taskID=%s, nodeID=%s, status=%d, serverIP=%s:%d",
		taskID, nodeID, status, serverIP, serverPort)

	return executeReport(ctx, taskID, nodeID, status, result, serverIP, serverPort)
}

// executeReport 执行上报请求
func executeReport(ctx context.Context, taskID string, nodeID string, status int, result string, serverIP string, serverPort int) error {
	url := controlapi.URL(serverIP, serverPort, "collectmgr", "ReportTaskStatus")

	// 构建请求体
	reqBody := &ReportTaskStatusRequest{
		ID:     taskID,
		NodeID: nodeID,
		Status: status,
		Result: result,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建 HTTP 客户端
	httpClient := &http.Client{Timeout: 5 * time.Second}

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

// sendRequest 发送单次请求
func sendRequest(ctx context.Context, url string, data []byte, httpClient *http.Client) error {
	req, err := controlapi.NewSignedRequestWithContext(ctx, "POST", url, data, controlapi.DefaultAuthConfig())
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("请求失败, status: %d, response: %s", resp.StatusCode, string(respData))
	}

	// 解析响应验证是否成功
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}

	var serverResp ServerResponse
	if err := json.Unmarshal(respData, &serverResp); err != nil {
		log.WarnContextf(ctx, "解析服务端响应失败: %v", err)
		return nil // 不影响上报结果
	}

	if serverResp.Code != 200 {
		return fmt.Errorf("服务端返回错误: code=%d, message=%s", serverResp.Code, serverResp.Message)
	}

	return nil
}
