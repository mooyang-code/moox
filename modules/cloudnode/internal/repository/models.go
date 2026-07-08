package repository

import "time"

type CloudNode struct {
	ID                 int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID            string     `gorm:"column:c_space_id"`
	NodeID             string     `gorm:"column:c_node_id"`
	Provider           string     `gorm:"column:c_provider"`
	CloudAccountID     string     `gorm:"column:c_cloud_account_id"`
	PackageID          string     `gorm:"column:c_package_id"`
	PackageVersion     string     `gorm:"column:c_package_version"`
	DeploymentID       string     `gorm:"column:c_deployment_id"`
	NodeType           string     `gorm:"column:c_node_type"`
	Region             string     `gorm:"column:c_region"`
	Namespace          string     `gorm:"column:c_namespace"`
	FunctionName       string     `gorm:"column:c_function_name"`
	RunningVersion     string     `gorm:"column:c_running_version"`
	SupportedWorkloads string     `gorm:"column:c_supported_workloads"`
	Metadata           string     `gorm:"column:c_metadata"`
	Status             string     `gorm:"column:c_status"`
	LastHeartbeatAt    *time.Time `gorm:"column:c_last_heartbeat_at"`
	IsDeleted          bool       `gorm:"column:c_is_deleted"`
	CreateTime         time.Time  `gorm:"column:c_ctime"`
	ModifyTime         time.Time  `gorm:"column:c_mtime"`
}

func (*CloudNode) TableName() string { return "t_cloud_nodes" }

type CloudAccount struct {
	ID          int       `gorm:"column:c_id;primaryKey;autoIncrement"`
	AccountID   string    `gorm:"column:c_account_id"`
	AccountName string    `gorm:"column:c_account_name"`
	Provider    string    `gorm:"column:c_provider"`
	SecretID    string    `gorm:"column:c_secret_id"`
	SecretKey   string    `gorm:"column:c_secret_key"`
	AppID       string    `gorm:"column:c_app_id"`
	COSRegion   string    `gorm:"column:c_cos_region"`
	COSBucket   string    `gorm:"column:c_cos_bucket"`
	ExtraConfig string    `gorm:"column:c_extra_config"`
	IsDeleted   bool      `gorm:"column:c_is_deleted"`
	CreateTime  time.Time `gorm:"column:c_ctime"`
	ModifyTime  time.Time `gorm:"column:c_mtime"`
}

func (*CloudAccount) TableName() string { return "t_cloud_accounts" }

type FunctionPackage struct {
	ID             int       `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID        string    `gorm:"column:c_space_id"`
	PackageID      string    `gorm:"column:c_package_id"`
	PackageName    string    `gorm:"column:c_package_name"`
	Version        string    `gorm:"column:c_version"`
	Description    string    `gorm:"column:c_description"`
	Runtime        string    `gorm:"column:c_runtime"`
	PackageType    string    `gorm:"column:c_package_type"`
	WorkloadType   string    `gorm:"column:c_workload_type"`
	OriginalName   string    `gorm:"column:c_original_filename"`
	FileSize       int64     `gorm:"column:c_file_size"`
	FileMD5        string    `gorm:"column:c_file_md5"`
	CloudAccountID string    `gorm:"column:c_cloud_account_id"`
	COSRegion      string    `gorm:"column:c_cos_region"`
	COSBucket      string    `gorm:"column:c_cos_bucket"`
	COSPath        string    `gorm:"column:c_cos_path"`
	Status         string    `gorm:"column:c_status"`
	ErrorMessage   string    `gorm:"column:c_error_message"`
	IsDeleted      bool      `gorm:"column:c_is_deleted"`
	CreateTime     time.Time `gorm:"column:c_ctime"`
	ModifyTime     time.Time `gorm:"column:c_mtime"`
}

func (*FunctionPackage) TableName() string { return "t_cloud_function_packages" }
