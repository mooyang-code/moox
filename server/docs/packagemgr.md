# PackageMgr 模块文档

## 1. 模块概述

PackageMgr（代码包管理）模块负责云函数代码包的上传、存储、下载和管理，支持本地存储和腾讯云 COS 对象存储。

### 1.1 核心功能

- **代码包上传**：支持 ZIP 格式的代码包上传
- **存储管理**：本地文件系统 + COS 双重存储
- **异步处理**：大文件上传通过异步任务处理
- **版本管理**：支持代码包多版本管理
- **下载服务**：提供代码包下载 URL
- **缓存优化**：本地缓存热门代码包

### 1.2 存储策略

- **本地存储**：所有代码包保存到本地文件系统
- **COS 存储**：可选上传到腾讯云 COS（配置后生效）
- **优先级**：部署时优先使用 COS，fallback 到本地

## 2. 架构设计

### 2.1 模块结构

```
packagemgr/
├── interface.go            # 对外接口定义
├── impl/                   # 业务逻辑实现
│   └── service_impl.go     # Service 实现
├── dao/                    # 数据访问层
│   └── function_package.go # 代码包 DAO
├── model/                  # 数据模型
│   └── function_package.go # 代码包实体
├── executors/              # 异步任务执行器
│   ├── upload_file.go      # 上传文件执行器
│   └── register.go         # 执行器注册
└── api/                    # HTTP API
    ├── handler.go          # API 处理器
    ├── router.go           # 路由注册
    └── types.go            # API 类型定义
```

### 2.2 核心组件

| 组件 | 职责 | 说明 |
|------|------|------|
| **Service** | 服务接口 | 对外暴露的核心接口 |
| **ServiceImpl** | 服务实现 | 实现上传、下载、查询等逻辑 |
| **DAO** | 数据访问 | 代码包的数据库操作 |
| **UploadFileExecutor** | 上传执行器 | 异步处理文件上传 |
| **StorageManager** | 存储管理 | 管理本地和 COS 存储 |

### 2.3 存储架构

```
┌──────────────────────────────────────────────┐
│             Upload Request                   │
└───────────────┬──────────────────────────────┘
                │
                ▼
┌───────────────────────────────────────────────┐
│          Create AsyncTask Job                 │
└───────────────┬───────────────────────────────┘
                │
                ▼
┌───────────────────────────────────────────────┐
│       UploadFileExecutor (Worker)             │
│  ┌─────────────────────────────────────────┐ │
│  │ 1. Save to local: /data/packages/xxx    │ │
│  │ 2. Upload to COS (if configured)        │ │
│  │ 3. Save metadata to DB                  │ │
│  │ 4. Clean temp files                     │ │
│  └─────────────────────────────────────────┘ │
└───────────────┬───────────────────────────────┘
                │
                ▼
┌───────────────────────────────────────────────┐
│           Storage Layout                      │
│                                               │
│  Local:  /data/packages/<package_id>.zip     │
│  COS:    moox-packages/packages/<id>.zip     │
│  DB:     t_function_packages                  │
└───────────────────────────────────────────────┘
```

## 3. 核心接口

### 3.1 Service 接口

```go
package packagemgr

type Service interface {
    // GetPackageList 获取代码包列表
    GetPackageList(ctx context.Context, req *PackageListRequest) (*PackageListResponse, error)

    // GetPackageDetail 获取代码包详情
    GetPackageDetail(ctx context.Context, id int64) (*PackageDetail, error)

    // GetPackageDetailModel 获取代码包详情（返回 model）
    GetPackageDetailModel(ctx context.Context, id int64) (*model.FunctionPackage, error)

    // DeletePackage 删除代码包
    DeletePackage(ctx context.Context, id int64) error

    // UploadPackage 上传代码包
    UploadPackage(ctx context.Context, req *UploadPackageRequest) (*UploadPackageResponse, error)

    // GetPackageDownloadURL 获取代码包下载 URL
    GetPackageDownloadURL(ctx context.Context, id int64) (*PackageDownloadURL, error)
}

// NewService 创建服务实例
func NewService(dbManager *database.Manager, asyncTask asynctask.Service) Service
```

## 4. 数据模型

### 4.1 FunctionPackage（代码包）

```go
type FunctionPackage struct {
    ID             int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
    Name           string    `gorm:"column:c_name"`
    Version        string    `gorm:"column:c_version"`
    Runtime        string    `gorm:"column:c_runtime"`        // Go1, Python3.6
    Description    string    `gorm:"column:c_description"`
    StoragePath    string    `gorm:"column:c_storage_path"`   // 本地路径
    COSBucket      string    `gorm:"column:c_cos_bucket"`     // COS 桶名
    COSPath        string    `gorm:"column:c_cos_path"`       // COS 对象路径
    COSRegion      string    `gorm:"column:c_cos_region"`     // COS 区域
    FileSize       int64     `gorm:"column:c_file_size"`      // 文件大小（字节）
    FileMD5        string    `gorm:"column:c_file_md5"`       // 文件 MD5
    UploadStatus   int       `gorm:"column:c_upload_status"`  // 0-上传中, 1-成功, 2-失败
    IsDeleted      bool      `gorm:"column:c_is_deleted"`
    CreateTime     time.Time `gorm:"column:c_create_time;autoCreateTime"`
    UpdateTime     time.Time `gorm:"column:c_update_time;autoUpdateTime"`
}
```

**字段说明**：
- `StoragePath`：本地文件系统路径（必填）
- `COSBucket/COSPath`：COS 存储路径（可选）
- `UploadStatus`：0-上传中, 1-成功, 2-失败

## 5. 核心流程

### 5.1 上传代码包流程

```
┌─────────────┐
│ 1. API请求  │
│  POST /upload│
│  - 文件数据 │
│  - 元数据   │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 2. 创建Job  │
│  - UPLOAD_  │
│    FILE Task│
│  - 保存临时 │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 3. Worker   │
│    执行     │
│ UploadFile  │
│  Executor   │
└──────┬──────┘
       │
       ├─> 保存到本地
       │   /data/packages/123.zip
       │
       ├─> 上传到COS（可选）
       │   moox-packages/packages/123.zip
       │
       ├─> 保存记录到DB
       │   - storage_path
       │   - cos_path
       │   - file_size
       │   - file_md5
       │
       └─> 清理临时文件
       │
       ▼
┌─────────────┐
│ 4. 返回结果 │
│  - packageID│
│  - jobID    │
└─────────────┘
```

### 5.2 下载代码包流程

```
┌─────────────┐
│ 1. API请求  │
│  GET /download│
│  ?id=123    │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 2. 查询DB   │
│  - 获取包信息│
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 3. 生成URL  │
│  优先级:    │
│  COS > Local│
└──────┬──────┘
       │
       ├─> COS URL
       │   https://moox-packages.cos.ap-guangzhou.myqcloud.com/...
       │
       └─> Local URL
           http://localhost:8080/download/123
       │
       ▼
┌─────────────┐
│ 4. 返回URL  │
└─────────────┘
```

### 5.3 删除代码包流程

```
┌─────────────┐
│ 1. API请求  │
│  DELETE /   │
│  package/123│
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 2. 检查引用 │
│  - 是否被   │
│    节点使用 │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 3. 软删除   │
│  - 标记删除 │
│  - 不删文件 │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 4. 异步清理 │
│  - 定期任务 │
│  - 删除文件 │
└─────────────┘
```

## 6. 使用指南

### 6.1 创建 Service

```go
import (
    "github.com/mooyang-code/moox/server/internal/service/packagemgr"
    "github.com/mooyang-code/moox/server/internal/service/asynctask"
    "github.com/mooyang-code/moox/server/internal/service/database"
)

// 创建 AsyncTask 服务
asyncService := asynctask.NewService(dbManager)

// 创建 PackageMgr 服务
packageService := packagemgr.NewService(dbManager, asyncService)
```

### 6.2 上传代码包（API）

```http
POST /api/package/upload
Content-Type: multipart/form-data

------WebKitFormBoundary
Content-Disposition: form-data; name="file"; filename="function.zip"
Content-Type: application/zip

<binary data>
------WebKitFormBoundary
Content-Disposition: form-data; name="name"

my-function
------WebKitFormBoundary
Content-Disposition: form-data; name="version"

v1.0.0
------WebKitFormBoundary
Content-Disposition: form-data; name="runtime"

Go1
------WebKitFormBoundary--
```

**响应**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "package_id": 123,
    "job_id": "uuid-xxx"
  }
}
```

### 6.3 查询代码包列表

```http
GET /api/package/list?page=1&page_size=10&runtime=Go1
```

**响应**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "packages": [
      {
        "id": 123,
        "name": "my-function",
        "version": "v1.0.0",
        "runtime": "Go1",
        "file_size": 1024000,
        "upload_status": 1,
        "create_time": "2023-10-23T10:00:00Z"
      }
    ],
    "total": 1
  }
}
```

### 6.4 获取下载 URL

```http
GET /api/package/download/123
```

**响应**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "download_url": "https://moox-packages.cos.ap-guangzhou.myqcloud.com/packages/123.zip",
    "expires_at": "2023-10-23T11:00:00Z"
  }
}
```

### 6.5 删除代码包

```http
DELETE /api/package/123
```

## 7. 存储配置

### 7.1 配置文件

```yaml
storage:
  cos_bucket: moox-packages          # COS 桶名
  cos_region: ap-guangzhou           # COS 区域
  local_path: ./data/packages        # 本地存储路径
  cache_size: 100                    # 缓存文件数量
  cache_expiry: 30                   # 缓存过期时间（分钟）
```

### 7.2 环境变量

```bash
export COS_BUCKET="moox-packages"
export COS_REGION="ap-guangzhou"
export LOCAL_STORAGE_PATH="./data/packages"
```

### 7.3 存储路径规则

**本地存储**：
```
<local_path>/<package_id>.zip
例如：./data/packages/123.zip
```

**COS 存储**：
```
<cos_bucket>/packages/<package_id>.zip
例如：moox-packages/packages/123.zip
```

## 8. 依赖关系

### 8.1 内部依赖

```
packagemgr (package level)
├── interface.go → impl/
├── impl/ → dao/, model/
├── dao/ → model/
├── executors/ → dao/
└── api/ → interface.go (Service)
```

### 8.2 外部依赖

```
packagemgr
├── database.Manager        # 数据库管理器
├── asynctask.Service       # 异步任务服务
├── COS SDK                 # 腾讯云 COS SDK
└── filesystem              # 本地文件系统
```

### 8.3 被依赖关系

```
packagemgr.Service (被以下模块依赖)
├── cloudnode               # 云节点创建/部署时获取代码包
└── cloudnode/executors     # 异步任务中使用
```

## 9. 异步任务集成

### 9.1 注册执行器

```go
// executors/register.go
func RegisterHandlers(
    asyncService asynctask.Service,
    dbManager *database.Manager,
) {
    // 创建 DAO
    packageDAO := dao.NewFunctionPackageDAO(dbManager.GetDB())

    // 创建执行器
    uploadExecutor := NewUploadFile(packageDAO)

    // 注册到 AsyncTask
    asyncService.RegisterHandler(
        "UPLOAD_PACKAGE_TO_COS",
        uploadExecutor.Execute,
        "上传代码包到COS",
    )
}
```

### 9.2 执行器实现

```go
type UploadFileExecutor struct {
    packageDAO dao.FunctionPackageDAO
}

func (e *UploadFileExecutor) Execute(ctx context.Context, taskID string, requestParams string) (string, error) {
    var req UploadRequest
    json.Unmarshal([]byte(requestParams), &req)

    // 1. 保存到本地
    localPath := fmt.Sprintf("./data/packages/%d.zip", req.PackageID)
    ioutil.WriteFile(localPath, req.FileData, 0644)

    // 2. 上传到 COS（如果配置）
    var cosPath string
    if config.COSBucket != "" {
        cosPath, err = uploadToCOS(req.FileData, req.PackageID)
        if err != nil {
            return "", err
        }
    }

    // 3. 更新数据库
    pkg := &model.FunctionPackage{
        ID:           req.PackageID,
        StoragePath:  localPath,
        COSPath:      cosPath,
        UploadStatus: 1,
    }
    e.packageDAO.Update(ctx, pkg)

    // 4. 返回结果
    result := UploadResult{
        PackageID:   req.PackageID,
        StoragePath: localPath,
        COSPath:     cosPath,
    }

    resultJSON, _ := json.Marshal(result)
    return string(resultJSON), nil
}
```

## 10. 最佳实践

### 10.1 文件命名规范

```go
// 使用 package_id 作为文件名
func GetPackageFileName(packageID int64) string {
    return fmt.Sprintf("%d.zip", packageID)
}

// 完整路径
func GetPackageFullPath(packageID int64) string {
    return filepath.Join(config.LocalPath, GetPackageFileName(packageID))
}
```

### 10.2 文件完整性校验

```go
// 上传时计算 MD5
func CalculateMD5(data []byte) string {
    hash := md5.Sum(data)
    return hex.EncodeToString(hash[:])
}

// 下载时验证 MD5
func VerifyFileMD5(filePath, expectedMD5 string) bool {
    data, _ := ioutil.ReadFile(filePath)
    actualMD5 := CalculateMD5(data)
    return actualMD5 == expectedMD5
}
```

### 10.3 大文件处理

```go
// 分块上传到 COS
func UploadLargeFile(filePath string, packageID int64) error {
    file, _ := os.Open(filePath)
    defer file.Close()

    // 使用 COS 分块上传
    opt := &cos.InitiateMultipartUploadOptions{
        // ...
    }
    // 分块上传逻辑
}
```

### 10.4 缓存策略

```go
// 本地缓存热门代码包
type PackageCache struct {
    cache *lru.Cache
}

func (c *PackageCache) Get(packageID int64) ([]byte, bool) {
    if data, ok := c.cache.Get(packageID); ok {
        return data.([]byte), true
    }
    return nil, false
}

func (c *PackageCache) Set(packageID int64, data []byte) {
    c.cache.Add(packageID, data)
}
```

## 11. 性能优化

### 11.1 并发上传

```go
// 使用 goroutine 并发上传到 COS
func ConcurrentUpload(files []File) error {
    var wg sync.WaitGroup
    errCh := make(chan error, len(files))

    for _, file := range files {
        wg.Add(1)
        go func(f File) {
            defer wg.Done()
            if err := uploadToCOS(f); err != nil {
                errCh <- err
            }
        }(file)
    }

    wg.Wait()
    close(errCh)

    // 检查错误
    for err := range errCh {
        if err != nil {
            return err
        }
    }

    return nil
}
```

### 11.2 流式上传

```go
// 避免一次性加载整个文件到内存
func StreamUpload(reader io.Reader, packageID int64) error {
    // 边读边上传
    opt := &cos.ObjectPutOptions{
        // ...
    }
    _, err := cosClient.Object.Put(ctx, key, reader, opt)
    return err
}
```

### 11.3 断点续传

```go
// 保存上传进度
type UploadProgress struct {
    PackageID   int64
    UploadedParts []int
}

// 从断点继续上传
func ResumeUpload(progress UploadProgress) error {
    // 读取已上传的分片
    // 继续上传未完成的分片
}
```

## 12. 监控和日志

### 12.1 关键日志

```go
log.InfoContextf(ctx, "[PackageMgr] Uploading package: %d", packageID)
log.InfoContextf(ctx, "[PackageMgr] Package uploaded successfully: %d, size: %d", packageID, fileSize)
log.ErrorContextf(ctx, "[PackageMgr] Failed to upload package %d: %v", packageID, err)
log.WarnContextf(ctx, "[PackageMgr] COS upload failed, using local only: %v", err)
```

### 12.2 监控指标

- 上传成功率
- 平均上传时间
- 存储空间使用率
- COS 流量消耗
- 缓存命中率

### 12.3 告警规则

```yaml
alerts:
  - name: HighUploadFailureRate
    expr: upload_failure_rate > 0.1
    message: "代码包上传失败率超过 10%"

  - name: StorageSpaceLow
    expr: storage_usage > 0.9
    message: "存储空间使用率超过 90%"
```

## 13. 故障处理

### 13.1 常见问题

| 问题 | 可能原因 | 解决方法 |
|------|----------|----------|
| 上传失败 | 文件过大 | 检查文件大小限制 |
| COS 上传失败 | 网络问题 | 使用本地存储 fallback |
| 下载慢 | 带宽不足 | 使用 COS CDN 加速 |
| 磁盘满 | 清理不及时 | 执行清理任务 |

### 13.2 清理策略

```go
// 定期清理被删除的代码包
func CleanupDeletedPackages() error {
    // 查询软删除的包（is_deleted=true 且超过 30 天）
    packages := dao.GetDeletedPackages(30 * 24 * time.Hour)

    for _, pkg := range packages {
        // 删除本地文件
        os.Remove(pkg.StoragePath)

        // 删除 COS 对象
        if pkg.COSPath != "" {
            deleteCOSObject(pkg.COSPath)
        }

        // 删除数据库记录
        dao.HardDelete(pkg.ID)
    }

    return nil
}
```

### 13.3 数据恢复

```go
// 从 COS 恢复本地文件
func RecoverFromCOS(packageID int64) error {
    pkg := dao.GetPackage(packageID)
    if pkg.COSPath == "" {
        return errors.New("no COS backup")
    }

    // 从 COS 下载
    data := downloadFromCOS(pkg.COSPath)

    // 保存到本地
    ioutil.WriteFile(pkg.StoragePath, data, 0644)

    return nil
}
```

## 14. 安全考虑

### 14.1 文件验证

```go
// 验证 ZIP 文件格式
func ValidateZipFile(data []byte) error {
    reader := bytes.NewReader(data)
    _, err := zip.NewReader(reader, int64(len(data)))
    if err != nil {
        return fmt.Errorf("invalid zip file: %w", err)
    }
    return nil
}

// 检查危险文件
func CheckDangerousFiles(zipPath string) error {
    // 检查是否包含可执行文件、脚本等
}
```

### 14.2 访问控制

```go
// 检查用户是否有权限下载
func CheckDownloadPermission(userID string, packageID int64) bool {
    pkg := dao.GetPackage(packageID)
    return pkg.OwnerID == userID || isAdmin(userID)
}
```

### 14.3 签名 URL

```go
// 生成临时签名 URL（1 小时有效）
func GenerateSignedURL(packageID int64) (string, error) {
    presignedURL, err := cosClient.Object.GetPresignedURL(
        ctx,
        http.MethodGet,
        key,
        secretID,
        secretKey,
        time.Hour,
        nil,
    )
    return presignedURL.String(), err
}
```

## 15. 扩展开发

### 15.1 支持更多存储后端

```go
// 定义存储接口
type StorageBackend interface {
    Upload(ctx context.Context, key string, data []byte) error
    Download(ctx context.Context, key string) ([]byte, error)
    Delete(ctx context.Context, key string) error
    GetURL(ctx context.Context, key string) (string, error)
}

// 实现 S3 存储
type S3Storage struct {
    client *s3.Client
}

// 实现阿里云 OSS 存储
type OSSStorage struct {
    client *oss.Client
}
```

### 15.2 压缩优化

```go
// 自动压缩代码包
func CompressPackage(inputPath, outputPath string) error {
    // 使用 gzip 或其他压缩算法
}

// 解压代码包
func DecompressPackage(inputPath, outputPath string) error {
    // 解压逻辑
}
```

### 15.3 版本管理

```go
// 支持代码包版本控制
type PackageVersion struct {
    PackageID int64
    Version   string
    FilePath  string
    CreateTime time.Time
}

// 回滚到历史版本
func RollbackToVersion(packageID int64, version string) error {
    // 切换到指定版本
}
```

## 16. 相关文档

- [架构文档](./architecture.md)
- [AsyncTask 模块](./asynctask.md)
- [CloudNode 模块](./cloudnode.md)
- [Database 模块](./database.md)
