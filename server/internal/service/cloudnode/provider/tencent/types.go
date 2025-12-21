package tencent

// 腾讯云内部使用的类型定义，避免循环导入

// CreateFunctionRequest 创建函数请求
type CreateFunctionRequest struct {
	Region       string
	FunctionName string
	Runtime      string
	Namespace    string
	Description  string
	ZipFile      string
	COSBucket    string // COS桶名，优先使用COS部署
	COSPath      string // COS对象路径，优先使用COS部署
	COSRegion    string // COS区域，COS部署时必需
	Handler      string
	MemorySize   int64
	Timeout      int64
	Environment  map[string]string
}

// UpdateFunctionRequest 更新函数请求
type UpdateFunctionRequest struct {
	Region       string
	FunctionName string
	Namespace    string
	ZipFile      string
	COSBucket    string // COS桶名，用于COS方式更新
	COSPath      string // COS对象路径，用于COS方式更新
	COSRegion    string // COS区域，COS更新时必需
	Description  *string
	Handler      *string
	MemorySize   *int64
	Timeout      *int64
	Environment  map[string]string
}

// FunctionInfo 函数信息
type FunctionInfo struct {
	FunctionName string
	FunctionID   string
	Runtime      string
	Namespace    string
	Description  string
	Handler      string
	Status       string
	StatusDesc   string
	CreateTime   string
	UpdateTime   string
	MemorySize   int64
	Timeout      int64
	Environment  map[string]string
}

// NamespaceInfo 命名空间信息
type NamespaceInfo struct {
	Name        string
	Description string
	CreateTime  string
	UpdateTime  string
}

// CreateTriggerRequest 创建触发器请求
type CreateTriggerRequest struct {
	Region       string
	FunctionName string
	TriggerName  string
	TriggerType  string
	TriggerDesc  string
	Namespace    string
	Enable       bool
	Description  string
}

// TriggerInfo 触发器信息
type TriggerInfo struct {
	TriggerName string
	TriggerType string
	TriggerDesc string
	Enable      bool
	CreateTime  string
	UpdateTime  string
}

// InvokeFunctionRequest 调用函数请求
type InvokeFunctionRequest struct {
	Region       string
	FunctionName string
	Namespace    string
	Qualifier    string
	EventData    interface{}
	InvokeType   string
	Headers      map[string]string
}

// InvokeFunctionResponse 调用函数响应
type InvokeFunctionResponse struct {
	RequestID    string
	Result       string
	Duration     int64
	BillDuration int64
	MemoryUsage  int64
	StatusCode   int
	ErrorMessage string
	ErrorType    string
	ReturnResult string
}

// UploadCOSRequest COS文件上传请求
type UploadCOSRequest struct {
	Bucket      string // COS桶名
	Key         string // 文件在COS中的路径
	Content     []byte // 文件内容
	ContentType string // 文件类型，可选
}

// UploadCOSResponse COS文件上传响应
type UploadCOSResponse struct {
	Location string // 上传后的文件访问URL
	ETag     string // 文件的ETag
}

// 函数状态常量
const (
	FunctionStatusCreating     = "Creating"
	FunctionStatusCreateFailed = "CreateFailed"
	FunctionStatusActive       = "Active"
	FunctionStatusUpdating     = "Updating"
	FunctionStatusUpdateFailed = "UpdateFailed"
)
