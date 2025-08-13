package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudFunctionInvokeService 云函数调用服务
type CloudFunctionInvokeService struct {
	db            *gorm.DB
	nodeDAO       dao.SCFNodeDAO
	provider provider.CloudProvider
}

// NewCloudFunctionInvokeService 创建云函数调用服务
func NewCloudFunctionInvokeService(db *gorm.DB) *CloudFunctionInvokeService {
	return &CloudFunctionInvokeService{
		db:            db,
		nodeDAO:       dao.NewSCFNodeDAO(db),
		provider: nil, // 需要通过SetCloudProvider设置
	}
}

// SetCloudProvider 设置云提供商
func (s *CloudFunctionInvokeService) SetCloudProvider(provider provider.CloudProvider) {
	s.provider = provider
}

// InvokeFunctionRequest 调用云函数的请求结构
type InvokeFunctionRequest struct {
	NodeID     string                 `json:"node_id"`     // 节点ID（必填）
	FunctionName string               `json:"function_name"` // 函数名称（可选，如果不提供则使用NodeID）
	Namespace  string                 `json:"namespace"`   // 命名空间（可选，如果不提供则使用节点的命名空间）
	EventData  interface{}            `json:"event_data"`  // 事件数据
	InvokeType string                 `json:"invoke_type"` // 调用类型：sync/async，默认sync
	Qualifier  string                 `json:"qualifier"`   // 版本号或别名
}

// InvokeFunctionResponse 调用云函数的响应结构
type InvokeFunctionResponse struct {
	Code         int         `json:"code"`          // 状态码
	Message      string      `json:"message"`       // 消息
	RequestID    string      `json:"request_id"`    // 请求ID
	Result       interface{} `json:"result"`        // 函数返回结果
	Duration     int64       `json:"duration"`      // 执行时长（毫秒）
	BillDuration int64       `json:"bill_duration"` // 计费时长（毫秒）
	MemoryUsage  int64       `json:"memory_usage"`  // 内存使用（字节）
}

// RegisterCloudFunctionInvokeRoutes 注册云函数调用相关的HTTP路由
func (s *CloudFunctionInvokeService) RegisterCloudFunctionInvokeRoutes(mux *http.ServeMux) {
	// POST /api/v1/cloud-function/invoke - 调用云函数
	mux.HandleFunc("/api/v1/cloud-function/invoke", s.handleInvokeFunction)
	
	log.Info("[CloudFunctionInvoke] 云函数调用路由注册完成")
}

// handleInvokeFunction 处理调用云函数的请求
func (s *CloudFunctionInvokeService) handleInvokeFunction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeInvokeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	ctx := r.Context()

	// 解析请求体
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeInvokeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Failed to read request body: %v", err))
		return
	}
	defer r.Body.Close()

	var req InvokeFunctionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeInvokeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Failed to parse request: %v", err))
		return
	}

	// 验证必填字段
	if req.NodeID == "" && req.FunctionName == "" {
		writeInvokeJSONError(w, http.StatusBadRequest, "Either node_id or function_name is required")
		return
	}

	// 调用云函数
	resp, err := s.invokeCloudFunction(ctx, &req)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to invoke cloud function: %v", err)
		writeInvokeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to invoke cloud function: %v", err))
		return
	}

	// 返回响应
	writeInvokeJSONResponse(w, http.StatusOK, resp)
}

// invokeCloudFunction 调用云函数的核心逻辑
func (s *CloudFunctionInvokeService) invokeCloudFunction(ctx context.Context, req *InvokeFunctionRequest) (*InvokeFunctionResponse, error) {
	// 检查云提供商是否配置
	if s.provider == nil {
		return nil, fmt.Errorf("cloud provider not configured")
	}

	var functionName, namespace string
	
	// 如果提供了NodeID，从数据库获取节点信息
	if req.NodeID != "" {
		node, err := s.nodeDAO.GetSCFNode(ctx, req.NodeID)
		if err != nil {
			return nil, fmt.Errorf("failed to get node: %w", err)
		}
		if node == nil {
			return nil, fmt.Errorf("node not found: %s", req.NodeID)
		}
		
		// 使用节点的信息作为默认值
		functionName = node.NodeID // 假设NodeID就是函数名
		namespace = node.Namespace
	}
	
	// 如果请求中明确指定了函数名和命名空间，则覆盖默认值
	if req.FunctionName != "" {
		functionName = req.FunctionName
	}
	if req.Namespace != "" {
		namespace = req.Namespace
	}
	
	// 确保有函数名
	if functionName == "" {
		return nil, fmt.Errorf("function name cannot be determined")
	}
	
	// 设置默认调用类型
	invokeType := req.InvokeType
	if invokeType == "" || (invokeType != "sync" && invokeType != "async") {
		invokeType = "sync"
	}
	
	// 转换调用类型
	var cloudInvokeType string
	if invokeType == "sync" {
		cloudInvokeType = provider.InvokeTypeSync
	} else {
		cloudInvokeType = provider.InvokeTypeAsync
	}
	
	log.InfoContextf(ctx, "Invoking cloud function: %s in namespace: %s", functionName, namespace)
	
	// 构建调用请求
	invokeReq := &provider.InvokeFunctionRequest{
		FunctionName: functionName,
		Namespace:    namespace,
		Qualifier:    req.Qualifier,
		EventData:    req.EventData,
		InvokeType:   cloudInvokeType,
	}
	
	// 记录开始时间
	startTime := time.Now()
	
	// 调用云函数
	cloudResp, err := s.provider.InvokeFunction(ctx, invokeReq)
	if err != nil {
		return nil, fmt.Errorf("failed to invoke cloud function: %w", err)
	}
	
	// 计算执行时间
	executionTime := time.Since(startTime).Milliseconds()
	
	// 构建响应
	resp := &InvokeFunctionResponse{
		Code:         0,
		Message:      "Success",
		RequestID:    cloudResp.RequestID,
		Duration:     cloudResp.Duration,
		BillDuration: cloudResp.BillDuration,
		MemoryUsage:  cloudResp.MemoryUsage,
	}
	
	// 处理错误情况
	if cloudResp.StatusCode != 0 && cloudResp.StatusCode != 200 {
		resp.Code = cloudResp.StatusCode
		resp.Message = cloudResp.ErrorMessage
		if cloudResp.ErrorType != "" {
			resp.Message = fmt.Sprintf("%s: %s", cloudResp.ErrorType, cloudResp.ErrorMessage)
		}
	}
	
	// 处理返回结果
	if cloudResp.Result != "" {
		// 尝试将结果解析为JSON
		var resultData interface{}
		if err := json.Unmarshal([]byte(cloudResp.Result), &resultData); err != nil {
			// 如果不是JSON，则直接返回字符串
			resp.Result = cloudResp.Result
		} else {
			resp.Result = resultData
		}
	}
	
	log.InfoContextf(ctx, "Cloud function invoked successfully - RequestID: %s, Duration: %dms, ExecutionTime: %dms",
		resp.RequestID, resp.Duration, executionTime)
	
	return resp, nil
}

// writeInvokeJSONResponse 写入JSON响应
func writeInvokeJSONResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Errorf("Failed to write JSON response: %v", err)
	}
}

// writeInvokeJSONError 写入JSON错误响应
func writeInvokeJSONError(w http.ResponseWriter, statusCode int, message string) {
	resp := InvokeFunctionResponse{
		Code:    statusCode,
		Message: message,
	}
	writeInvokeJSONResponse(w, statusCode, resp)
}