package provider

import (
	"context"
	"io"
	"time"
)

// CloudProvider 云厂商接口
type CloudProvider interface {
	// 云函数管理
	CreateFunction(ctx context.Context, req *CreateFunctionRequest) (*FunctionInfo, error)
	UpdateFunction(ctx context.Context, req *UpdateFunctionRequest) error
	DeleteFunction(ctx context.Context, functionName, namespace string) error
	GetFunction(ctx context.Context, functionName, namespace string) (*FunctionInfo, error)
	ListFunctions(ctx context.Context, namespace string) ([]*FunctionInfo, error)

	// 命名空间管理
	CreateNamespace(ctx context.Context, namespace, description string) error
	DeleteNamespace(ctx context.Context, namespace string) error
	ListNamespaces(ctx context.Context) ([]*NamespaceInfo, error)

	// 触发器管理
	CreateTrigger(ctx context.Context, req *CreateTriggerRequest) error
	DeleteTrigger(ctx context.Context, functionName, triggerName, namespace string) error
	ListTriggers(ctx context.Context, functionName, namespace string) ([]*TriggerInfo, error)

	// 云API调用
	InvokeFunction(ctx context.Context, req *InvokeFunctionRequest) (*InvokeFunctionResponse, error)
}

// CreateFunctionRequest 创建函数请求
type CreateFunctionRequest struct {
	FunctionName string            // 函数名称
	Runtime      string            // 运行时环境
	Namespace    string            // 命名空间
	Description  string            // 函数描述
	ZipFile      string            // 代码包（base64编码）
	MemorySize   int64             // 内存大小（MB）
	Timeout      int64             // 超时时间（秒）
	Environment  map[string]string // 环境变量
}

// UpdateFunctionRequest 更新函数请求
type UpdateFunctionRequest struct {
	FunctionName string            // 函数名称
	Namespace    string            // 命名空间
	ZipFile      string            // 代码包（base64编码）
	Description  *string           // 函数描述
	MemorySize   *int64            // 内存大小（MB）
	Timeout      *int64            // 超时时间（秒）
	Environment  map[string]string // 环境变量
}

// FunctionInfo 函数信息
type FunctionInfo struct {
	FunctionName string            // 函数名称
	FunctionID   string            // 函数ID
	Runtime      string            // 运行时环境
	Namespace    string            // 命名空间
	Description  string            // 函数描述
	Status       string            // 状态
	StatusDesc   string            // 状态描述
	CreateTime   string            // 创建时间
	UpdateTime   string            // 更新时间
	MemorySize   int64             // 内存大小（MB）
	Timeout      int64             // 超时时间（秒）
	Environment  map[string]string // 环境变量
}

// NamespaceInfo 命名空间信息
type NamespaceInfo struct {
	Name        string // 命名空间名称
	Description string // 描述
	CreateTime  string // 创建时间
	UpdateTime  string // 更新时间
}

// CreateTriggerRequest 创建触发器请求
type CreateTriggerRequest struct {
	FunctionName string // 函数名称
	TriggerName  string // 触发器名称
	TriggerType  string // 触发器类型：timer, cos, apigw, ckafka, cmq
	TriggerDesc  string // 触发器配置（如cron表达式）
	Namespace    string // 命名空间
	Enable       bool   // 是否启用
	Description  string // 触发器描述
}

// TriggerInfo 触发器信息
type TriggerInfo struct {
	TriggerName string // 触发器名称
	TriggerType string // 触发器类型
	TriggerDesc string // 触发器配置
	Enable      bool   // 是否启用
	CreateTime  string // 创建时间
	UpdateTime  string // 更新时间
}

// InvokeFunctionRequest 调用函数请求
type InvokeFunctionRequest struct {
	FunctionName string                 // 函数名称
	Namespace    string                 // 命名空间
	Qualifier    string                 // 版本号或别名
	EventData    interface{}            // 事件数据
	InvokeType   string                 // 调用类型：RequestResponse（同步）, Event（异步）
	Headers      map[string]string      // 请求头
}

// InvokeFunctionResponse 调用函数响应
type InvokeFunctionResponse struct {
	RequestID      string // 请求ID
	Result         string // 执行结果（base64编码）
	Duration       int64  // 执行时长（毫秒）
	BillDuration   int64  // 计费时长（毫秒）
	MemoryUsage    int64  // 内存使用（字节）
	StatusCode     int    // 状态码
	ErrorMessage   string // 错误信息
	ErrorType      string // 错误类型
	ReturnResult   string // 函数返回结果
}

// 函数状态常量
const (
	FunctionStatusCreating     = "Creating"     // 创建中
	FunctionStatusCreateFailed = "CreateFailed" // 创建失败
	FunctionStatusActive       = "Active"       // 正常
	FunctionStatusUpdating     = "Updating"     // 更新中
	FunctionStatusUpdateFailed = "UpdateFailed" // 更新失败
)

// 触发器类型常量
const (
	TriggerTypeTimer = "timer" // 定时触发器
	TriggerTypeCOS   = "cos"   // 对象存储触发器
	TriggerTypeAPIGW = "apigw" // API网关触发器
)

// 调用类型常量
const (
	InvokeTypeSync  = "RequestResponse" // 同步调用
	InvokeTypeAsync = "Event"           // 异步调用
)

// COSProvider COS对象存储接口
type COSProvider interface {
	// UploadCOS 上传文件到COS
	UploadCOS(ctx context.Context, req *UploadCOSRequest) (*UploadCOSResponse, error)
	
	// UploadCOSWithReader 使用Reader上传文件到COS
	UploadCOSWithReader(ctx context.Context, bucket, key string, reader io.Reader, contentType string) (*UploadCOSResponse, error)
	
	// DeleteCOSObject 删除COS中的对象
	DeleteCOSObject(ctx context.Context, bucket, key string) error
	
	// GetCOSObjectURL 获取COS对象的访问URL
	GetCOSObjectURL(ctx context.Context, bucket, key string, expire time.Duration) (string, error)
}

// UploadCOSRequest COS文件上传请求
type UploadCOSRequest struct {
	Bucket       string // COS桶名
	Key          string // 文件在COS中的路径
	Content      []byte // 文件内容
	ContentType  string // 文件类型，可选
}

// UploadCOSResponse COS文件上传响应
type UploadCOSResponse struct {
	Location string // 上传后的文件访问URL
	ETag     string // 文件的ETag
}

// CloudProviderWithCOS 同时支持云函数和COS的Provider接口
type CloudProviderWithCOS interface {
	CloudProvider
	COSProvider
}
