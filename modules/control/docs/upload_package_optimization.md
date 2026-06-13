# 代码包上传优化 - 避免在数据库中存储大文件

## 问题描述

在之前的实现中，上传代码包时会将 base64 编码的文件内容（`file_content`）写入到 `t_async_jobs` 表的 `c_request_params` 字段中。这会导致：

1. 数据库存储了大量的文件数据（每个文件可能几MB到几十MB）
2. 数据库查询性能下降
3. 日志中包含大量的 base64 编码内容，难以阅读

示例日志：
```
[1242.322ms] [rows:1] INSERT INTO `t_async_jobs` (`c_job_id`,`c_request_params`,...) 
VALUES ("386a3e8f-58bc-4335-ab0c-57c09eb42e98",
"[{\"task_type\":\"UPLOAD_FILE_TO_COS\",\"request_params\":
\"{\\\"file_content\\\":\\\"UEsDBBQAAAAIACh0KlynhMwpVmrHAFfRgAEEABwAbWFpblVU...(非常长的base64字符串)\"}]")
```

## 解决方案

### 核心思路

1. **在提交异步任务前**：先将文件内容保存到本地文件系统
2. **在数据库中**：只记录文件的本地路径，不再包含文件内容
3. **在执行任务时**：从本地路径读取文件内容
4. **任务完成后**：清理临时文件

### 修改的文件

#### 1. `impl_package_service.go`

##### 新增方法：`saveUploadFileToTemp`

```go
// saveUploadFileToTemp 将上传的文件内容保存到本地临时文件
func (s *ServiceImpl) saveUploadFileToTemp(ctx context.Context, req *UploadPackageRequest) (string, error) {
    // 解码base64文件内容
    fileContent, err := base64.StdEncoding.DecodeString(req.FileContent)
    if err != nil {
        return "", fmt.Errorf("解码base64文件内容失败: %w", err)
    }

    // 生成临时文件名（基于包名、版本和时间戳）
    timestamp := time.Now().Unix()
    packageID := model.GeneratePackageID()
    filename := fmt.Sprintf("upload_%s_%s_%d_%s.zip", req.PackageName, req.Version, timestamp, packageID)
    
    // 使用 constants 提供的路径方法
    filePath := constants.GetPackageStorageFilePath(filename)
    
    // 确保目录存在
    dir := filepath.Dir(filePath)
    if err := os.MkdirAll(dir, 0755); err != nil {
        return "", fmt.Errorf("创建目录失败: %w", err)
    }

    // 写入文件
    if err := os.WriteFile(filePath, fileContent, 0644); err != nil {
        return "", fmt.Errorf("写入文件失败: %w", err)
    }

    return filePath, nil
}
```

##### 修改方法：`UploadPackage`

```go
func (s *ServiceImpl) UploadPackage(ctx context.Context, req *UploadPackageRequest) (*UploadPackageResponse, error) {
    // 1. 先将文件内容保存到本地临时文件
    filePath, err := s.saveUploadFileToTemp(ctx, req)
    if err != nil {
        return nil, fmt.Errorf("保存上传文件失败: %w", err)
    }

    // 2. 构建异步任务请求参数（不包含文件内容，只包含文件路径）
    uploadFileReq := UploadPackageExecutorRequest{
        PackageName:    req.PackageName,
        Version:        req.Version,
        Description:    req.Description,
        Runtime:        req.Runtime,
        PackageType:    req.PackageType,
        CloudAccountID: req.CloudAccountID,
        FilePath:       filePath, // 使用文件路径替代文件内容
    }
    
    // ... 其余代码不变
}
```

#### 2. `executor_upload_package.go`

##### 修改结构体：`UploadPackageExecutorRequest`

```go
type UploadPackageExecutorRequest struct {
    PackageName    string `json:"package_name"`
    Version        string `json:"version"`
    Description    string `json:"description"`
    Runtime        string `json:"runtime"`
    PackageType    string `json:"package_type"`
    CloudAccountID string `json:"cloud_account_id"`
    FilePath       string `json:"file_path"`    // 本地文件路径（新增）
    FileContent    string `json:"file_content"` // base64编码（保留用于向后兼容）
}
```

##### 修改方法：`parseAndPreprocess`

```go
func (e *UploadPackageExecutor) parseAndPreprocess(ctx context.Context, taskID, requestParams string) (*UploadPackageExecutorRequest, []byte, error) {
    var req UploadPackageExecutorRequest
    if err := json.Unmarshal([]byte(requestParams), &req); err != nil {
        return nil, nil, fmt.Errorf("failed to parse request params: %w", err)
    }

    var fileContent []byte
    var err error

    // 优先从本地文件路径读取
    if req.FilePath != "" {
        fileContent, err = os.ReadFile(req.FilePath)
        if err != nil {
            return nil, nil, fmt.Errorf("读取本地文件失败: %w", err)
        }
    } else if req.FileContent != "" {
        // 向后兼容：如果没有FilePath，尝试从FileContent解码
        fileContent, err = base64.StdEncoding.DecodeString(req.FileContent)
        if err != nil {
            return nil, nil, fmt.Errorf("解码base64文件内容失败: %w", err)
        }
    } else {
        return nil, nil, fmt.Errorf("file_path or file_content is required")
    }

    return &req, fileContent, nil
}
```

##### 修改方法：`Execute` - 添加临时文件清理

```go
func (e *UploadPackageExecutor) Execute(ctx context.Context, taskID string, requestParams string) (string, error) {
    // 1. 解析和预处理
    req, fileContent, err := e.parseAndPreprocess(ctx, taskID, requestParams)
    if err != nil {
        return "", err
    }

    // 如果使用了临时文件，在任务完成后清理
    if req.FilePath != "" {
        defer func() {
            if err := os.Remove(req.FilePath); err != nil {
                log.WarnContextf(ctx, "[UploadFileExecutor] Failed to remove temp file %s: %v", req.FilePath, err)
            } else {
                log.InfoContextf(ctx, "[UploadFileExecutor] Removed temp file: %s", req.FilePath)
            }
        }()
    }

    // ... 其余代码不变
}
```

## 优化效果

### 优化前

- `t_async_jobs` 表的 `c_request_params` 字段包含完整的 base64 编码文件内容
- 单条记录可能占用几MB到几十MB空间
- 日志输出包含大量不可读的 base64 字符串

### 优化后

- `t_async_jobs` 表的 `c_request_params` 字段只包含文件路径（约几十字节）
- 大大减少数据库存储空间
- 日志更加清晰易读
- 临时文件在任务完成后自动清理

示例 `c_request_params` 内容：
```json
[{
  "task_type": "UPLOAD_FILE_TO_COS",
  "request_params": "{\"package_name\":\"my-package\",\"version\":\"1.0.0\",\"file_path\":\"/tmp/moox/upload_my-package_1.0.0_1736478925_abc123def45.zip\"}"
}]
```

## 向后兼容性

- 保留了 `FileContent` 字段，旧的任务仍然可以执行
- `parseAndPreprocess` 方法优先使用 `FilePath`，如果不存在则回退到 `FileContent`
- 不影响已存在的异步任务

## 文件存储路径

临时文件使用统一的存储路径：
- 目录：`/tmp/moox/`（通过 `constants.GetPackageStorageDir()` 获取）
- 文件名格式：`upload_{PackageName}_{Version}_{Timestamp}_{PackageID}.zip`
- 示例：`/tmp/moox/upload_collector_1.0.0_1736478925_abc123def45.zip`

## 清理机制

1. **正常流程**：任务执行完成后，通过 `defer` 自动清理临时文件
2. **异常流程**：如果任务执行失败，`defer` 仍然会执行清理
3. **建议**：可以添加定时任务清理超过24小时的临时文件（作为保险措施）

## 测试验证

编译验证通过：
```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/control
go build ./internal/service/cloudnode/...
# Exit code: 0 ✓
```

## 后续优化建议

1. 添加定时清理任务，清理超过24小时的临时文件
2. 添加磁盘空间监控，避免临时文件占用过多空间
3. 考虑使用对象存储直接上传，避免临时文件落地
