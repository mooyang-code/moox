package store

import "time"

type CloudNode struct {
	ID             int       `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID        string    `gorm:"column:c_space_id"`
	NodeID         string    `gorm:"column:c_node_id"`
	Provider       string    `gorm:"column:c_provider"`
	CloudAccountID string    `gorm:"column:c_cloud_account_id"`
	PackageID      string    `gorm:"column:c_package_id"`
	PackageVersion string    `gorm:"column:c_package_version"`
	DeploymentID   string    `gorm:"column:c_deployment_id"`
	NodeType       string    `gorm:"column:c_node_type"`
	Region         string    `gorm:"column:c_region"`
	Namespace      string    `gorm:"column:c_namespace"`
	FunctionName   string    `gorm:"column:c_function_name"`
	Metadata       string    `gorm:"column:c_metadata"`
	Status         string    `gorm:"column:c_status"`
	IsDeleted      bool      `gorm:"column:c_is_deleted"`
	CreateTime     time.Time `gorm:"column:c_ctime"`
	ModifyTime     time.Time `gorm:"column:c_mtime"`
}

func (*CloudNode) TableName() string { return "t_cloud_nodes" }

type CloudAccount struct {
	ID                 int       `gorm:"column:c_id;primaryKey;autoIncrement"`
	AccountID          string    `gorm:"column:c_account_id"`
	AccountName        string    `gorm:"column:c_account_name"`
	Provider           string    `gorm:"column:c_provider"`
	CredentialSecretID string    `gorm:"column:c_credential_secret_id"`
	AppID              string    `gorm:"column:c_app_id"`
	COSRegion          string    `gorm:"column:c_cos_region"`
	COSBucket          string    `gorm:"column:c_cos_bucket"`
	ExtraConfig        string    `gorm:"column:c_extra_config"`
	IsDeleted          bool      `gorm:"column:c_is_deleted"`
	CreateTime         time.Time `gorm:"column:c_ctime"`
	ModifyTime         time.Time `gorm:"column:c_mtime"`
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

type NodeBatch struct {
	ID          int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID     string     `gorm:"column:c_space_id"`
	JobID       string     `gorm:"column:c_job_id"`
	Operation   string     `gorm:"column:c_operation"`
	Status      string     `gorm:"column:c_status"`
	TotalCount  int        `gorm:"column:c_total_count"`
	CompletedAt *time.Time `gorm:"column:c_completed_at"`
	CreateTime  time.Time  `gorm:"column:c_ctime"`
	ModifyTime  time.Time  `gorm:"column:c_mtime"`
}

func (*NodeBatch) TableName() string { return "t_cloud_node_batches" }

type NodeBatchItem struct {
	ID            int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID       string     `gorm:"column:c_space_id"`
	JobID         string     `gorm:"column:c_job_id"`
	ItemID        string     `gorm:"column:c_item_id"`
	ItemIndex     int        `gorm:"column:c_item_index"`
	NodeID        string     `gorm:"column:c_node_id"`
	Status        string     `gorm:"column:c_status"`
	RequestJSON   string     `gorm:"column:c_request_json"`
	ResultSummary string     `gorm:"column:c_result_summary"`
	ErrorMessage  string     `gorm:"column:c_error_message"`
	StartedAt     *time.Time `gorm:"column:c_started_at"`
	CompletedAt   *time.Time `gorm:"column:c_completed_at"`
	CreateTime    time.Time  `gorm:"column:c_ctime"`
	ModifyTime    time.Time  `gorm:"column:c_mtime"`
}

func (*NodeBatchItem) TableName() string { return "t_cloud_node_batch_items" }
