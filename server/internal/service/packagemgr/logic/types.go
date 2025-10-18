package logic

import "time"

// UploadPackageRequest 上传代码包请求
type UploadPackageRequest struct {
	PackageName    string `json:"package_name" binding:"required"`
	Version        string `json:"version" binding:"required"`
	Description    string `json:"description"`
	Runtime        string `json:"runtime" binding:"required"`
	PackageType    string `json:"package_type" binding:"required"`
	FileContent    string `json:"file_content" binding:"required"` // base64编码的zip文件内容
	CloudAccountID string `json:"cloud_account_id"`                // 云账户ID，可选，用于COS配置
	CreatedBy      string `json:"-"`                               // 从JWT中获取
}

// StorageConfig 存储配置
type StorageConfig struct {
	UseCOS bool
	Bucket string
	Path   string
}

// PackageListRequest 代码包列表请求
type PackageListRequest struct {
	Page        int    `form:"page,default=1"`
	PageSize    int    `form:"page_size,default=20"`
	PackageName string `form:"package_name"`
	Runtime     string `form:"runtime"`
	PackageType string `form:"package_type"`
	Status      *int   `form:"status"`
}

// PackageListResponse 代码包列表响应
type PackageListResponse struct {
	Total int64              `json:"total"`
	Items []*PackageListItem `json:"items"`
}

// PackageListItem 代码包列表项
type PackageListItem struct {
	ID               int64      `json:"id"`
	PackageName      string     `json:"package_name"`
	Version          string     `json:"version"`
	Description      string     `json:"description"`
	Runtime          string     `json:"runtime"`
	PackageType      string     `json:"package_type"`
	PackageTypeLabel string     `json:"package_type_label"`
	FileSize         int64      `json:"file_size"`
	FileMD5          string     `json:"file_md5"`
	CloudAccountID   string     `json:"cloud_account_id"`
	COSRegion        string     `json:"cos_region"`
	Status           int        `json:"status"`
	StatusLabel      string     `json:"status_label"`
	LastDeployTime   *time.Time `json:"last_deploy_time"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
}

// PackageDetail 代码包详情视图对象，包含完整的字段信息
type PackageDetail struct {
	ID               int64  `json:"id"`
	PackageName      string `json:"package_name"`
	Version          string `json:"version"`
	Description      string `json:"description"`
	Runtime          string `json:"runtime"`
	PackageType      string `json:"package_type"`
	PackageTypeLabel string `json:"package_type_label"`

	// 文件信息
	OriginalFilename string `json:"original_filename"`
	FileSize         int64  `json:"file_size"`
	FileMD5          string `json:"file_md5"`

	// COS存储信息
	CloudAccountID string `json:"cloud_account_id"`
	COSRegion      string `json:"cos_region"`
	COSBucket      string `json:"cos_bucket"`
	COSPath        string `json:"cos_path"`
	COSURL         string `json:"cos_url"`

	// 状态管理
	Status         int    `json:"status"`
	StatusLabel    string `json:"status_label"`
	UploadProgress int    `json:"upload_progress"`
	ErrorMessage   string `json:"error_message"`

	// 使用统计
	LastDeployTime *time.Time `json:"last_deploy_time"`

	// 审计字段
	CreatedBy string    `json:"created_by"`
	Invalid   int       `json:"invalid"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PackageDownloadURL 代码包下载URL
type PackageDownloadURL struct {
	ID          int64  `json:"id"`
	PackageName string `json:"package_name"`
	Version     string `json:"version"`
	Filename    string `json:"filename"`
	DownloadURL string `json:"download_url"`
	FileSize    int64  `json:"file_size"`
	FileMD5     string `json:"file_md5"`
}

// UploadPackageAsyncResponse 异步上传代码包响应
type UploadPackageAsyncResponse struct {
	TaskID      string `json:"task_id"`      // 任务ID
	PackageID   int64  `json:"package_id"`   // 包ID
	PackageName string `json:"package_name"` // 包名
	Version     string `json:"version"`      // 版本
	Status      int    `json:"status"`       // 状态
	IsAsync     bool   `json:"is_async"`     // 是否异步处理
}

// UploadTaskStatusResponse 上传任务状态响应
type UploadTaskStatusResponse struct {
	TaskID      string `json:"task_id"`
	Status      string `json:"status"` // pending, processing, success, failed, cancelled
	Message     string `json:"message"`
	PackageID   int64  `json:"package_id,omitempty"`
	PackageName string `json:"package_name,omitempty"`
	Version     string `json:"version,omitempty"`
	Progress    int    `json:"progress"` // 0-100
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}
