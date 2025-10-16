package model

import (
	"time"
)

// FunctionPackage 云函数代码包模型
type FunctionPackage struct {
	ID               int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	PackageName      string    `gorm:"column:c_package_name;not null" json:"package_name"`
	Version          string    `gorm:"column:c_version;not null" json:"version"`
	Description      string    `gorm:"column:c_description;default:''" json:"description"`
	Runtime          string    `gorm:"column:c_runtime;not null" json:"runtime"`
	PackageType      string    `gorm:"column:c_package_type;not null;default:'data_collector'" json:"package_type"`
	
	// 文件信息
	OriginalFilename string `gorm:"column:c_original_filename;not null" json:"original_filename"`
	FileSize         int64  `gorm:"column:c_file_size;not null" json:"file_size"`
	FileMD5          string `gorm:"column:c_file_md5;not null" json:"file_md5"`
	
	// COS存储信息
	COSBucket string `gorm:"column:c_cos_bucket;not null" json:"cos_bucket"`
	COSPath   string `gorm:"column:c_cos_path;not null" json:"cos_path"`
	COSURL    string `gorm:"column:c_cos_url;default:''" json:"cos_url"`
	
	// 状态管理
	Status         int    `gorm:"column:c_status;not null;default:0" json:"status"`
	UploadProgress int    `gorm:"column:c_upload_progress;default:0" json:"upload_progress"`
	ErrorMessage   string `gorm:"column:c_error_message;default:''" json:"error_message"`
	
	// 使用统计
	LastDeployTime *time.Time `gorm:"column:c_last_deploy_time" json:"last_deploy_time"`
	
	// 审计字段
	CreatedBy string    `gorm:"column:c_created_by;not null" json:"created_by"`
	Invalid   int       `gorm:"column:c_invalid;not null;default:0" json:"invalid"`
	CTime     time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	MTime     time.Time `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName 指定表名
func (FunctionPackage) TableName() string {
	return "t_function_packages"
}

// FunctionDeployment 云函数部署记录模型
type FunctionDeployment struct {
	ID              int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	PackageID       int64     `gorm:"column:c_package_id;not null" json:"package_id"`
	CloudAccountID  int64     `gorm:"column:c_cloud_account_id;not null" json:"cloud_account_id"`
	FunctionName    string    `gorm:"column:c_function_name;not null" json:"function_name"`
	Namespace       string    `gorm:"column:c_namespace;default:'default'" json:"namespace"`
	
	// 部署配置
	Environment string `gorm:"column:c_environment;default:'{}'" json:"environment"`
	
	// 部署状态
	DeployStatus  int    `gorm:"column:c_deploy_status;default:0" json:"deploy_status"`
	DeployMessage string `gorm:"column:c_deploy_message;default:''" json:"deploy_message"`
	
	Invalid int       `gorm:"column:c_invalid;not null;default:0" json:"invalid"`
	CTime   time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	MTime   time.Time `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName 指定表名
func (FunctionDeployment) TableName() string {
	return "t_function_deployments"
}

// 状态常量
const (
	// 代码包状态
	PackageStatusUploading = 0 // 上传中
	PackageStatusAvailable = 1 // 可用
	PackageStatusDeleted   = 2 // 已删除
	PackageStatusFailed    = 3 // 上传失败
	
	// 部署状态
	DeployStatusInProgress = 0 // 进行中
	DeployStatusSuccess    = 1 // 成功
	DeployStatusFailed     = 2 // 失败
)

// 函数包类型常量
const (
	PackageTypeDataCollector    = "data_collector"    // 数据采集类型
	PackageTypeFactorCalculator = "factor_calculator" // 因子计算类型
)

// 运行时环境常量
const (
	RuntimeGo1        = "Go1"
	RuntimePython37   = "Python3.7"
	RuntimePython39   = "Python3.9"
	RuntimeNodejs14   = "Nodejs14.18"
	RuntimeNodejs16   = "Nodejs16.13"
)

// GetPackageTypeDisplayName 获取函数包类型显示名称
func GetPackageTypeDisplayName(packageType string) string {
	switch packageType {
	case PackageTypeDataCollector:
		return "数据采集类型"
	case PackageTypeFactorCalculator:
		return "因子计算类型"
	default:
		return "未知类型"
	}
}

// GetStatusDisplayName 获取状态显示名称
func GetStatusDisplayName(status int) string {
	switch status {
	case PackageStatusUploading:
		return "上传中"
	case PackageStatusAvailable:
		return "可用"
	case PackageStatusDeleted:
		return "已删除"
	case PackageStatusFailed:
		return "上传失败"
	default:
		return "未知状态"
	}
}

// GetDeployStatusDisplayName 获取部署状态显示名称
func GetDeployStatusDisplayName(status int) string {
	switch status {
	case DeployStatusInProgress:
		return "进行中"
	case DeployStatusSuccess:
		return "成功"
	case DeployStatusFailed:
		return "失败"
	default:
		return "未知状态"
	}
}