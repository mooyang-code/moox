package provider

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// Client 云厂商客户端接口
type Client interface {
	// ========== 云函数管理 ==========

	// CreateFunction 创建云函数
	CreateFunction(ctx context.Context, req *CreateFunctionRequest) (*FunctionInfo, error)

	// UpdateFunction 更新云函数
	UpdateFunction(ctx context.Context, req *UpdateFunctionRequest) error

	// DeleteFunction 删除云函数
	DeleteFunction(ctx context.Context, functionName, namespace string) error

	// GetFunction 获取云函数详情
	GetFunction(ctx context.Context, functionName, namespace string) (*FunctionInfo, error)

	// ListFunctions 列出指定命名空间下的云函数
	ListFunctions(ctx context.Context, namespace string) ([]*FunctionInfo, error)

	// ========== 命名空间管理 ==========

	// CreateNamespace 创建命名空间
	CreateNamespace(ctx context.Context, namespace, description string) error

	// DeleteNamespace 删除命名空间
	DeleteNamespace(ctx context.Context, namespace string) error

	// ListNamespaces 列出所有命名空间
	ListNamespaces(ctx context.Context) ([]*NamespaceInfo, error)

	// ========== 触发器管理 ==========

	// CreateTrigger 创建触发器
	CreateTrigger(ctx context.Context, req *CreateTriggerRequest) error

	// DeleteTrigger 删除触发器
	DeleteTrigger(ctx context.Context, functionName, triggerName, namespace string) error

	// ListTriggers 列出指定云函数的触发器
	ListTriggers(ctx context.Context, functionName, namespace string) ([]*TriggerInfo, error)

	// ========== 云函数调用 ==========

	// InvokeFunction 调用云函数
	InvokeFunction(ctx context.Context, req *InvokeFunctionRequest) (*InvokeFunctionResponse, error)

	// ========== COS对象存储 ==========

	// UploadCOS 上传文件到COS
	UploadCOS(ctx context.Context, req *UploadCOSRequest) (*UploadCOSResponse, error)

	// UploadCOSWithReader 使用Reader上传文件到COS
	UploadCOSWithReader(ctx context.Context, bucket, key string, reader io.Reader, contentType string) (*UploadCOSResponse, error)

	// DeleteCOSObject 删除COS中的对象
	DeleteCOSObject(ctx context.Context, bucket, key string) error

	// GetCOSObjectURL 获取COS对象的访问URL
	GetCOSObjectURL(ctx context.Context, bucket, key string, expire time.Duration) (string, error)

	// DownloadCOSToFile 从COS下载文件到本地路径
	DownloadCOSToFile(ctx context.Context, key string, localPath string) error
}

// NewClient 创建云平台客户端实例
func NewClient(config *Config) (Client, error) {
	if config == nil {
		return nil, fmt.Errorf("cloud config is nil")
	}

	constructor, exists := GetConstructor(config.Provider)
	if !exists {
		return nil, fmt.Errorf("unsupported cloud platform: %s", config.Provider)
	}

	return constructor(config)
}

// CreateFunctionRequest 创建函数请求
type CreateFunctionRequest struct {
	FunctionName string            // 函数名称
	Runtime      string            // 运行时环境
	Namespace    string            // 命名空间
	Description  string            // 函数描述
	ZipFile      string            // 代码包（base64编码）
	COSBucket    string            // COS桶名，优先使用COS部署
	COSPath      string            // COS对象路径，优先使用COS部署
	COSRegion    string            // COS区域，COS部署时必需
	MemorySize   int64             // 内存大小（MB）
	Timeout      int64             // 超时时间（秒）
	Environment  map[string]string // 环境变量
}

// UpdateFunctionRequest 更新函数请求
type UpdateFunctionRequest struct {
	FunctionName string            // 函数名称
	Namespace    string            // 命名空间
	ZipFile      string            // 代码包（base64编码）
	COSBucket    string            // COS桶名，用于COS方式更新
	COSPath      string            // COS对象路径，用于COS方式更新
	COSRegion    string            // COS区域，COS更新时必需
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
	FunctionName string            // 函数名称
	Namespace    string            // 命名空间
	Qualifier    string            // 版本号或别名
	EventData    interface{}       // 事件数据
	InvokeType   string            // 调用类型：RequestResponse（同步）, Event（异步）
	Headers      map[string]string // 请求头
}

// InvokeFunctionResponse 调用函数响应
type InvokeFunctionResponse struct {
	RequestID    string // 请求ID
	Result       string // 执行结果（base64编码）
	Duration     int64  // 执行时长（毫秒）
	BillDuration int64  // 计费时长（毫秒）
	MemoryUsage  int64  // 内存使用（字节）
	StatusCode   int    // 状态码
	ErrorMessage string // 错误信息
	ErrorType    string // 错误类型
	ReturnResult string // 函数返回结果
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

// Constructor 云平台构造函数类型
type Constructor func(config *Config) (Client, error)

// 全局变量：云平台构造函数注册表
var (
	registryMu sync.RWMutex
	registry   = make(map[CloudPlatform]Constructor) // key为CloudPlatform
)

// ========== 云平台构造函数注册表全局函数 ==========

// RegisterCloudPlatform 注册云平台构造函数
func RegisterCloudPlatform(platform CloudPlatform, constructor Constructor) error {
	if constructor == nil {
		return fmt.Errorf("constructor cannot be nil")
	}

	if platform == "" {
		return fmt.Errorf("platform cannot be empty")
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	registry[platform] = constructor
	return nil
}

// GetConstructor 获取云平台构造函数
func GetConstructor(platform CloudPlatform) (Constructor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	constructor, exists := registry[platform]
	return constructor, exists
}

// HasConstructor 检查是否注册了指定平台的构造函数
func HasConstructor(platform CloudPlatform) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()

	_, exists := registry[platform]
	return exists
}

// ListConstructors 列出所有已注册的构造函数
func ListConstructors() map[CloudPlatform]Constructor {
	registryMu.RLock()
	defer registryMu.RUnlock()

	result := make(map[CloudPlatform]Constructor)
	for platform, constructor := range registry {
		result[platform] = constructor
	}
	return result
}

// ParseCloudPlatform 将字符串转换为CloudPlatform类型
func ParseCloudPlatform(platformStr string) (CloudPlatform, error) {
	switch platformStr {
	case "tencent":
		return Tencent, nil
	case "aliyun":
		return Aliyun, nil
	case "aws":
		return AWS, nil
	default:
		return "", fmt.Errorf("unsupported provider: %s", platformStr)
	}
}
