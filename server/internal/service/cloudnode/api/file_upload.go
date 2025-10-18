package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/server/internal/common"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"

	"trpc.group/trpc-go/trpc-go/log"
)

// FileUploadHandler 文件上传处理器
type FileUploadHandler struct {
	scfNodeService cloudnodelogic.SCFNodeService
}

// NewFileUploadHandler 创建文件上传处理器
func NewFileUploadHandler(scfNodeService cloudnodelogic.SCFNodeService) *FileUploadHandler {
	return &FileUploadHandler{
		scfNodeService: scfNodeService,
	}
}

// FunctionUploadRequest 函数上传请求结构
type FunctionUploadRequest struct {
	NodeID        string `json:"node_id"`
	ZipFileBase64 string `json:"zip_file_base64"`
	FileName      string `json:"file_name"`
}

// HandleFunctionUpload 处理云函数代码上传（支持base64格式）
func (h *FileUploadHandler) HandleFunctionUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 只接受POST请求
	if r.Method != http.MethodPost {
		writeErrorResponse(w, 405, fmt.Errorf("method not allowed"))
		return
	}

	// 解析JSON请求体
	var req FunctionUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, 400, fmt.Errorf("invalid request body: %w", err))
		return
	}

	// 验证参数
	if req.NodeID == "" {
		writeErrorResponse(w, 400, fmt.Errorf("node_id is required"))
		return
	}
	if req.ZipFileBase64 == "" {
		writeErrorResponse(w, 400, fmt.Errorf("zip_file_base64 is required"))
		return
	}

	// 解码base64数据
	zipData, err := base64.StdEncoding.DecodeString(req.ZipFileBase64)
	if err != nil {
		writeErrorResponse(w, 400, fmt.Errorf("invalid base64 data: %w", err))
		return
	}

	// 创建临时文件
	tempDir := os.TempDir()
	timestamp := time.Now().UnixNano()
	fileName := req.FileName
	if fileName == "" {
		fileName = "upload.zip"
	}
	tempFilePath := filepath.Join(tempDir, fmt.Sprintf("scf_upload_%s_%d_%s", req.NodeID, timestamp, fileName))

	// 写入临时文件
	if err := os.WriteFile(tempFilePath, zipData, 0644); err != nil {
		writeErrorResponse(w, 500, fmt.Errorf("failed to write temp file: %w", err))
		return
	}

	// 延迟删除临时文件
	go func() {
		time.Sleep(5 * time.Minute)
		os.Remove(tempFilePath)
	}()

	// 调用服务层更新云函数
	err = h.scfNodeService.UpdateNodeFunction(ctx, req.NodeID, tempFilePath)
	if err != nil {
		writeErrorResponse(w, 500, err)
		return
	}

	log.InfoContextf(ctx, "Successfully enqueued function update for node %s with base64 file", req.NodeID)

	// 返回成功响应
	response := &common.UnifiedAPIResponse{
		Code:    200,
		Message: "Function update enqueued successfully",
		Data: []any{map[string]interface{}{
			"node_id":   req.NodeID,
			"file_name": fileName,
			"file_size": len(zipData),
		}},
	}
	writeJSONResponse(w, http.StatusOK, response)
}

// writeJSONResponse 写入JSON响应
func writeJSONResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Errorf("Failed to write JSON response: %v", err)
	}
}

// writeErrorResponse 写入错误响应
func writeErrorResponse(w http.ResponseWriter, statusCode int, err error) {
	response := &common.UnifiedAPIResponse{
		Code:    statusCode,
		Message: err.Error(),
		Data:    []any{},
	}
	writeJSONResponse(w, statusCode, response)
}
