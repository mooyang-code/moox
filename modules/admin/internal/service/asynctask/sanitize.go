package asynctask

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/constants"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

var uploadNameCleaner = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeTaskRequests(ctx context.Context, tasks []*pb.TaskRequestItem) ([]*pb.TaskRequestItem, error) {
	sanitized := make([]*pb.TaskRequestItem, len(tasks))
	for i, task := range tasks {
		updated, err := sanitizeTaskRequest(ctx, task)
		if err != nil {
			return nil, err
		}
		sanitized[i] = updated
	}
	return sanitized, nil
}

func sanitizeTaskRequest(ctx context.Context, task *pb.TaskRequestItem) (*pb.TaskRequestItem, error) {
	if task.GetTaskType() != TaskTypeUploadFileToCOS {
		return task, nil
	}
	if strings.TrimSpace(task.GetRequestParams()) == "" {
		return task, nil
	}

	var params map[string]interface{}
	if err := json.Unmarshal([]byte(task.GetRequestParams()), &params); err != nil {
		return task, fmt.Errorf("invalid request_params for %s: %w", task.GetTaskType(), err)
	}

	if filePath, ok := params["file_path"].(string); ok && strings.TrimSpace(filePath) != "" {
		if _, exists := params["file_content"]; exists {
			delete(params, "file_content")
			updatedJSON, err := json.Marshal(params)
			if err != nil {
				return task, fmt.Errorf("failed to marshal sanitized params: %w", err)
			}
			task.RequestParams = string(updatedJSON)
		}
		return task, nil
	}

	rawContent, ok := params["file_content"]
	if !ok {
		return task, nil
	}
	contentStr, ok := rawContent.(string)
	if !ok || strings.TrimSpace(contentStr) == "" {
		delete(params, "file_content")
		updatedJSON, err := json.Marshal(params)
		if err != nil {
			return task, fmt.Errorf("failed to marshal sanitized params: %w", err)
		}
		task.RequestParams = string(updatedJSON)
		return task, nil
	}

	content, err := base64.StdEncoding.DecodeString(contentStr)
	if err != nil {
		return task, fmt.Errorf("invalid file_content for %s: %w", task.GetTaskType(), err)
	}

	filePath, err := saveUploadContentToFile(params, content)
	if err != nil {
		return task, err
	}
	params["file_path"] = filePath
	delete(params, "file_content")

	updatedJSON, err := json.Marshal(params)
	if err != nil {
		return task, fmt.Errorf("failed to marshal sanitized params: %w", err)
	}
	task.RequestParams = string(updatedJSON)
	log.InfoContextf(ctx, "[AsyncTask] Stored upload file content to local path: %s", filePath)
	return task, nil
}

func saveUploadContentToFile(params map[string]interface{}, content []byte) (string, error) {
	filename := buildUploadFilename(params)
	filePath := constants.GetPackageStorageFilePath(filename)
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return "", fmt.Errorf("create upload dir failed: %w", err)
	}
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		return "", fmt.Errorf("write upload file failed: %w", err)
	}
	return filePath, nil
}

func buildUploadFilename(params map[string]interface{}) string {
	packageName := sanitizeUploadName(getParamString(params, "package_name"))
	version := sanitizeUploadName(getParamString(params, "version"))
	if packageName == "" {
		packageName = "package"
	}
	if version == "" {
		version = "unknown"
	}
	return fmt.Sprintf("upload_%s_%s_%d_%s.zip", packageName, version, time.Now().UnixNano(), uuid.NewString())
}

func getParamString(params map[string]interface{}, key string) string {
	raw, ok := params[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func sanitizeUploadName(value string) string {
	if value == "" {
		return ""
	}
	cleaned := uploadNameCleaner.ReplaceAllString(value, "_")
	return strings.Trim(cleaned, "_")
}
