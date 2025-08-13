package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// AsyncTaskHandler 异步任务处理器
type AsyncTaskHandler struct {
	service logic.AsyncTaskService
}

// NewAsyncTaskHandler 创建异步任务处理器
func NewAsyncTaskHandler(db *gorm.DB) *AsyncTaskHandler {
	return &AsyncTaskHandler{
		service: logic.NewAsyncTaskService(db),
	}
}

// NewAsyncTaskHandlerWithService 使用指定的服务创建异步任务处理器
func NewAsyncTaskHandlerWithService(service logic.AsyncTaskService) *AsyncTaskHandler {
	return &AsyncTaskHandler{
		service: service,
	}
}

// AsyncTaskCreateRequest 异步任务创建请求
type AsyncTaskCreateRequest struct {
	TaskType      string                 `json:"task_type"`
	RequestParams map[string]interface{} `json:"request_params"`
}

// AsyncTaskResponse 异步任务响应
type AsyncTaskResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// AsyncTaskStatusResponse 任务状态响应
type AsyncTaskStatusResponse struct {
	TaskID        string                `json:"task_id"`
	TaskType      string                `json:"task_type"`
	TaskStatus    int                   `json:"task_status"`
	TotalCount    int                   `json:"total_count"`
	SuccessCount  int                   `json:"success_count"`
	FailedCount   int                   `json:"failed_count"`
	Progress      int                   `json:"progress"`
	ErrorMessage  string                `json:"error_message,omitempty"`
	CreatedAt     string                `json:"created_at"`
	CompletedTime string                `json:"completed_time,omitempty"`
	FailedItems   []AsyncTaskDetailItem `json:"failed_items,omitempty"`
}

// AsyncTaskDetailItem 任务详情项
type AsyncTaskDetailItem struct {
	ItemID       string `json:"item_id"`
	ItemName     string `json:"item_name"`
	Status       int    `json:"status"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// HandleCreate 处理任务创建请求
func (h *AsyncTaskHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		h.writeErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析请求
	var req AsyncTaskCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}

	// 验证请求参数
	if req.TaskType == "" {
		h.writeErrorResponse(w, http.StatusBadRequest, "task_type is required")
		return
	}

	// 生成唯一的任务ID
	taskID := uuid.New().String()

	// 根据任务类型获取总数
	totalCount := h.getTotalCountFromParams(req.TaskType, req.RequestParams)
	if totalCount <= 0 {
		h.writeErrorResponse(w, http.StatusBadRequest, "invalid task parameters")
		return
	}

	// 创建并执行任务
	if err := h.service.CreateAndExecuteTask(ctx, taskID, req.TaskType, totalCount, req.RequestParams); err != nil {
		log.ErrorContextf(ctx, "Failed to create and execute task: %v", err)
		h.writeErrorResponse(w, http.StatusInternalServerError, fmt.Sprintf("failed to execute task: %v", err))
		return
	}

	h.writeJSONResponse(w, http.StatusOK, AsyncTaskResponse{
		Code: 200,
		Message: "Task created and executing",
		Data: map[string]string{"task_id": taskID},
	})
}

// HandleQuery 处理任务查询请求
func (h *AsyncTaskHandler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		h.writeErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 获取任务ID
	var taskID string
	if r.Method == http.MethodGet {
		taskID = r.URL.Query().Get("task_id")
	} else {
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.writeErrorResponse(w, http.StatusBadRequest, "invalid request body")
			return
		}
		taskID = req["task_id"]
	}

	if taskID == "" {
		h.writeErrorResponse(w, http.StatusBadRequest, "task_id is required")
		return
	}

	// 查询任务状态
	task, err := h.service.GetTaskStatus(ctx, taskID)
	if err != nil {
		// 对于不存在的任务，返回一个初始状态的响应
		response := AsyncTaskStatusResponse{
			TaskID:       taskID,
			TaskType:     "",
			TaskStatus:   0,  // 未知状态
			TotalCount:   0,
			SuccessCount: 0,
			FailedCount:  0,
			Progress:     0,
			ErrorMessage: "",
			CreatedAt:    "",
			FailedItems:  []AsyncTaskDetailItem{},
		}
		
		h.writeJSONResponse(w, http.StatusOK, AsyncTaskResponse{
			Code: 200,
			Data: response,
		})
		return
	}

	// 构造响应
	response := AsyncTaskStatusResponse{
		TaskID:       task.TaskID,
		TaskType:     task.TaskType,
		TaskStatus:   task.TaskStatus,
		TotalCount:   task.TotalCount,
		SuccessCount: task.SuccessCount,
		FailedCount:  task.FailedCount,
		Progress:     task.GetProgress(),
		ErrorMessage: task.ErrorMessage,
		CreatedAt:    task.CreatedAt.Format("2006-01-02 15:04:05"),
		FailedItems:  []AsyncTaskDetailItem{},
	}

	if task.CompletedTime != nil {
		response.CompletedTime = task.CompletedTime.Format("2006-01-02 15:04:05")
	}

	// 如果任务已完成且有失败项，查询失败详情
	if task.IsCompleted() && task.FailedCount > 0 {
		failedItems, err := h.service.GetTaskDetails(ctx, taskID, model.TaskDetailStatusFailed)
		if err != nil {
			log.ErrorContextf(ctx, "Failed to get task failed items: %v", err)
		} else {
			// 转换失败项
			for _, detail := range failedItems {
				response.FailedItems = append(response.FailedItems, AsyncTaskDetailItem{
					ItemID:       detail.ItemID,
					ItemName:     detail.ItemName,
					Status:       detail.Status,
					ErrorMessage: detail.ErrorMessage,
				})
			}
		}
	}

	h.writeJSONResponse(w, http.StatusOK, AsyncTaskResponse{
		Code: 200,
		Data: response,
	})
}

// getTotalCountFromParams 从请求参数中获取任务总数
func (h *AsyncTaskHandler) getTotalCountFromParams(taskType string, params map[string]interface{}) int {
	switch taskType {
	case model.TaskTypeBatchCreateNode, model.TaskTypeBatchUpdateNode, model.TaskTypeBatchDeleteNode, model.TaskTypeBatchDeployNode:
		// 获取nodes数组
		if nodes, ok := params["nodes"].([]interface{}); ok {
			return len(nodes)
		}
		// 尝试解析JSON数组
		if nodesStr, ok := params["nodes"].(string); ok {
			var nodes []interface{}
			if err := json.Unmarshal([]byte(nodesStr), &nodes); err == nil {
				return len(nodes)
			}
		}
	}
	return 0
}


// RegisterRoutes 注册路由
func (h *AsyncTaskHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/moox-api/async_task/create", h.HandleCreate)
	mux.HandleFunc("/moox-api/async_task/query", h.HandleQuery)
}

// writeErrorResponse 写入错误响应
func (h *AsyncTaskHandler) writeErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	response := AsyncTaskResponse{
		Code:    statusCode,
		Message: message,
	}
	h.writeJSONResponse(w, statusCode, response)
}

// writeSuccessResponse 写入成功响应
func (h *AsyncTaskHandler) writeSuccessResponse(w http.ResponseWriter, message string) {
	response := AsyncTaskResponse{
		Code:    200,
		Message: message,
	}
	h.writeJSONResponse(w, http.StatusOK, response)
}

// writeJSONResponse 写入JSON响应
func (h *AsyncTaskHandler) writeJSONResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Errorf("Failed to write JSON response: %v", err)
	}
}