// Package tencent 提供云服务商的包装器实现
//
// tencent_wrapper.go 的作用：
// 1. 解决循环依赖问题
//   - provider 包定义通用接口
//   - providers/tencent 包实现具体功能
//   - 如果 providers/tencent 直接实现 provider.Client 接口，
//     会造成循环依赖（providers/tencent → provider → providers/tencent）
//   - 通过在 provider 包中创建 wrapper，避免了循环依赖
//
// 2. 类型转换和适配
//   - 将通用的 CloudConfig 转换为腾讯云特定的 Config
//   - 将通用接口的请求/响应类型转换为腾讯云内部的类型
//   - 例如：CreateFunctionRequest → CreateFunctionRequest
//
// 3. 工厂模式注册
//   - wrapper 在 init 函数中自动注册到工厂
//   - 上层代码可以通过工厂模式动态创建不同云厂商的实例
//   - 使用方只需要知道 ProviderType，不需要知道具体实现
//
// 4. 隔离实现细节
//   - 上层代码只依赖 CloudProvider 接口，不需要知道腾讯云的具体实现
//   - 腾讯云的具体实现被隔离在 providers/tencent 包中
//   - wrapper 作为桥梁连接接口定义和具体实现
//
// 5. 统一的错误处理和日志
//   - wrapper 层可以添加统一的错误处理、日志记录等横切关注点
//   - 不需要修改具体的实现即可增强功能
//
// 这种设计遵循了依赖倒置原则：高层模块不应该依赖低层模块，两者都应该依赖抽象。
package tencent

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
)

// TencentWrapper 腾讯云Provider包装器
type TencentWrapper struct {
	provider *Provider
}

// init 注册腾讯云Provider
func init() {
	if err := provider.RegisterCloudPlatform(provider.Tencent, NewTencentWrapper); err != nil {
		panic(fmt.Sprintf("failed to register Tencent provider: %v", err))
	}
}

// NewTencentWrapper 创建腾讯云Provider
func NewTencentWrapper(config *provider.Config) (provider.Client, error) {
	// 转换配置
	tencentConfig := &Config{
		SecretID:    config.SecretID,
		SecretKey:   config.SecretKey,
		Region:      config.GetString("region"),
		COSBucket:   config.GetString("cos_bucket"),
		COSAppID:    config.GetString("cos_app_id"),
		ExtraConfig: config.ExtraConfig,
	}

	// 创建Provider
	provider, err := NewProvider(tencentConfig)
	if err != nil {
		return nil, err
	}

	return &TencentWrapper{
		provider: provider,
	}, nil
}

// CreateFunction 创建云函数
func (p *TencentWrapper) CreateFunction(ctx context.Context, req *provider.CreateFunctionRequest) (*provider.FunctionInfo, error) {
	// 转换请求
	tencentReq := &CreateFunctionRequest{
		Region:       req.Region,
		FunctionName: req.FunctionName,
		Runtime:      req.Runtime,
		Namespace:    req.Namespace,
		Description:  req.Description,
		FunctionType: req.FunctionType,
		ZipFile:      req.ZipFile,
		COSBucket:    req.COSBucket,
		COSPath:      req.COSPath,
		COSRegion:    req.COSRegion,
		MemorySize:   req.MemorySize,
		Timeout:      req.Timeout,
		Environment:  req.Environment,
	}

	// 调用Provider方法
	tencentResp, err := p.provider.CreateFunction(ctx, tencentReq)
	if err != nil {
		return nil, err
	}

	// 转换响应
	return &provider.FunctionInfo{
		FunctionName: tencentResp.FunctionName,
		FunctionID:   tencentResp.FunctionID,
		Runtime:      tencentResp.Runtime,
		Namespace:    tencentResp.Namespace,
		Description:  tencentResp.Description,
		Status:       tencentResp.Status,
		StatusDesc:   tencentResp.StatusDesc,
		CreateTime:   tencentResp.CreateTime,
		UpdateTime:   tencentResp.UpdateTime,
		MemorySize:   tencentResp.MemorySize,
		Timeout:      tencentResp.Timeout,
		Environment:  tencentResp.Environment,
	}, nil
}

// UpdateFunction 更新云函数
func (p *TencentWrapper) UpdateFunction(ctx context.Context, req *provider.UpdateFunctionRequest) error {
	// 转换请求
	tencentReq := &UpdateFunctionRequest{
		Region:       req.Region,
		FunctionName: req.FunctionName,
		Namespace:    req.Namespace,
		ZipFile:      req.ZipFile,
		COSBucket:    req.COSBucket,
		COSPath:      req.COSPath,
		COSRegion:    req.COSRegion,
		Description:  req.Description,
		MemorySize:   req.MemorySize,
		Timeout:      req.Timeout,
		Environment:  req.Environment,
	}

	return p.provider.UpdateFunction(ctx, tencentReq)
}

// DeleteFunction 删除云函数
func (p *TencentWrapper) DeleteFunction(ctx context.Context, functionName, namespace, region string) error {
	return p.provider.DeleteFunction(ctx, functionName, namespace, region)
}

// GetFunction 获取云函数详情
func (p *TencentWrapper) GetFunction(ctx context.Context, functionName, namespace, region string) (*provider.FunctionInfo, error) {
	resp, err := p.provider.GetFunction(ctx, functionName, namespace, region)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}

	// 转换响应
	return &provider.FunctionInfo{
		FunctionName: resp.FunctionName,
		FunctionID:   resp.FunctionID,
		Runtime:      resp.Runtime,
		Namespace:    resp.Namespace,
		Description:  resp.Description,
		Status:       resp.Status,
		StatusDesc:   resp.StatusDesc,
		CreateTime:   resp.CreateTime,
		UpdateTime:   resp.UpdateTime,
		MemorySize:   resp.MemorySize,
		Timeout:      resp.Timeout,
		Environment:  resp.Environment,
	}, nil
}

// ListFunctions 列出云函数
func (p *TencentWrapper) ListFunctions(ctx context.Context, namespace, region string) ([]*provider.FunctionInfo, error) {
	functions, err := p.provider.ListFunctions(ctx, namespace, region)
	if err != nil {
		return nil, err
	}

	// 转换响应
	var result []*provider.FunctionInfo
	for _, fn := range functions {
		result = append(result, &provider.FunctionInfo{
			FunctionName: fn.FunctionName,
			FunctionID:   fn.FunctionID,
			Runtime:      fn.Runtime,
			Namespace:    fn.Namespace,
			Description:  fn.Description,
			Status:       fn.Status,
			StatusDesc:   fn.StatusDesc,
			CreateTime:   fn.CreateTime,
			UpdateTime:   fn.UpdateTime,
			MemorySize:   fn.MemorySize,
			Timeout:      fn.Timeout,
			Environment:  fn.Environment,
		})
	}

	return result, nil
}

// CreateNamespace 创建命名空间
func (p *TencentWrapper) CreateNamespace(ctx context.Context, namespace, description, region string) error {
	return p.provider.CreateNamespace(ctx, namespace, description, region)
}

// DeleteNamespace 删除命名空间
func (p *TencentWrapper) DeleteNamespace(ctx context.Context, namespace, region string) error {
	return p.provider.DeleteNamespace(ctx, namespace, region)
}

// ListNamespaces 列出命名空间
func (p *TencentWrapper) ListNamespaces(ctx context.Context, region string) ([]*provider.NamespaceInfo, error) {
	namespaces, err := p.provider.ListNamespaces(ctx, region)
	if err != nil {
		return nil, err
	}

	// 转换响应
	var result []*provider.NamespaceInfo
	for _, ns := range namespaces {
		result = append(result, &provider.NamespaceInfo{
			Name:        ns.Name,
			Description: ns.Description,
			CreateTime:  ns.CreateTime,
			UpdateTime:  ns.UpdateTime,
		})
	}

	return result, nil
}

// CreateTrigger 创建触发器
func (p *TencentWrapper) CreateTrigger(ctx context.Context, req *provider.CreateTriggerRequest) error {
	// 转换请求
	tencentReq := &CreateTriggerRequest{
		Region:       req.Region,
		FunctionName: req.FunctionName,
		TriggerName:  req.TriggerName,
		TriggerType:  req.TriggerType,
		TriggerDesc:  req.TriggerDesc,
		Namespace:    req.Namespace,
		Enable:       req.Enable,
		Description:  req.Description,
	}

	return p.provider.CreateTrigger(ctx, tencentReq)
}

// DeleteTrigger 删除触发器
func (p *TencentWrapper) DeleteTrigger(ctx context.Context, functionName, triggerName, namespace, region string) error {
	return p.provider.DeleteTrigger(ctx, functionName, triggerName, namespace, region)
}

// ListTriggers 列出触发器
func (p *TencentWrapper) ListTriggers(ctx context.Context, functionName, namespace, region string) ([]*provider.TriggerInfo, error) {
	triggers, err := p.provider.ListTriggers(ctx, functionName, namespace, region)
	if err != nil {
		return nil, err
	}

	// 转换响应
	var result []*provider.TriggerInfo
	for _, tr := range triggers {
		result = append(result, &provider.TriggerInfo{
			TriggerName: tr.TriggerName,
			TriggerType: tr.TriggerType,
			TriggerDesc: tr.TriggerDesc,
			Enable:      tr.Enable,
			CreateTime:  tr.CreateTime,
			UpdateTime:  tr.UpdateTime,
		})
	}

	return result, nil
}

// InvokeFunction 调用云函数
func (p *TencentWrapper) InvokeFunction(ctx context.Context, req *provider.InvokeFunctionRequest) (*provider.InvokeFunctionResponse, error) {
	// 转换请求
	tencentReq := &InvokeFunctionRequest{
		Region:       req.Region,
		FunctionName: req.FunctionName,
		Namespace:    req.Namespace,
		Qualifier:    req.Qualifier,
		EventData:    req.EventData,
		InvokeType:   req.InvokeType,
		Headers:      req.Headers,
	}

	// 调用Provider方法
	tencentResp, err := p.provider.InvokeFunction(ctx, tencentReq)
	if err != nil {
		return nil, err
	}

	// 转换响应
	return &provider.InvokeFunctionResponse{
		RequestID:    tencentResp.RequestID,
		Result:       tencentResp.Result,
		Duration:     tencentResp.Duration,
		BillDuration: tencentResp.BillDuration,
		MemoryUsage:  tencentResp.MemoryUsage,
		StatusCode:   tencentResp.StatusCode,
		ErrorMessage: tencentResp.ErrorMessage,
		ErrorType:    tencentResp.ErrorType,
		ReturnResult: tencentResp.ReturnResult,
	}, nil
}

// 实现COS接口

// UploadCOS 上传文件到COS
func (p *TencentWrapper) UploadCOS(ctx context.Context, req *provider.UploadCOSRequest) (*provider.UploadCOSResponse, error) {
	// 转换请求
	tencentReq := &UploadCOSRequest{
		Bucket:      req.Bucket,
		Key:         req.Key,
		Content:     req.Content,
		ContentType: req.ContentType,
	}

	// 调用Provider方法
	tencentResp, err := p.provider.UploadCOS(ctx, tencentReq)
	if err != nil {
		return nil, err
	}

	// 转换响应
	return &provider.UploadCOSResponse{
		Location: tencentResp.Location,
		ETag:     tencentResp.ETag,
	}, nil
}

// UploadCOSWithReader 使用Reader上传文件到COS
func (p *TencentWrapper) UploadCOSWithReader(ctx context.Context,
	bucket, key string, reader io.Reader, contentType string) (*provider.UploadCOSResponse, error) {
	// 调用Provider方法
	tencentResp, err := p.provider.UploadCOSWithReader(ctx, bucket, key, reader, contentType)
	if err != nil {
		return nil, err
	}

	// 转换响应
	return &provider.UploadCOSResponse{
		Location: tencentResp.Location,
		ETag:     tencentResp.ETag,
	}, nil
}

// DeleteCOSObject 删除COS中的对象
func (p *TencentWrapper) DeleteCOSObject(ctx context.Context, bucket, key string) error {
	return p.provider.DeleteCOSObject(ctx, bucket, key)
}

// GetCOSObjectURL 获取COS对象的访问URL
func (p *TencentWrapper) GetCOSObjectURL(ctx context.Context, bucket, key string, expire time.Duration) (string, error) {
	return p.provider.GetCOSObjectURL(ctx, bucket, key, expire)
}

// DownloadCOSToFile 从COS下载文件到本地路径
func (p *TencentWrapper) DownloadCOSToFile(ctx context.Context, key string, localPath string) error {
	return p.provider.DownloadCOSToFile(ctx, key, localPath)
}
