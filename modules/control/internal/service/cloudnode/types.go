package cloudnode

// 云函数调用类型常量
const (
	InvokeTypeSync  = "RequestResponse" // 同步调用
	InvokeTypeAsync = "Event"           // 异步调用
)

// FunctionCodeConfig 云函数代码配置
type FunctionCodeConfig struct {
	Runtime       string            // 运行时环境（如 Go1, Python3.6 等）
	Handler       string            // 函数处理入口（如 main/bootstrap）
	Environment   map[string]string // 函数环境变量
	Version       string            // 代码版本号（用于描述信息）
	ZipFileBase64 string            // Base64编码的ZIP文件
	COSBucket     string            // COS桶名（优先使用COS）
	COSPath       string            // COS对象路径
	COSRegion     string            // COS区域
}

// InvokeFunctionResponse 云函数调用响应
type InvokeFunctionResponse struct {
	RequestID    string // 请求ID
	Result       string // 执行结果（base64编码）
	Duration     int64  // 执行时长（毫秒）
	StatusCode   int    // 状态码
	ErrorMessage string // 错误信息
	ErrorType    string // 错误类型
	ReturnResult string // 函数返回结果
}

// COSAccountInfo COS账户信息（对外暴露的简化结构，只包含必要字段）
type COSAccountInfo struct {
	Provider  string // 云厂商
	SecretID  string // 密钥ID
	SecretKey string // 密钥
	AppID     string // 应用ID
	COSRegion string // COS区域
	COSBucket string // COS桶名
}
