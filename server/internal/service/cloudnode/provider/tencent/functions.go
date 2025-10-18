package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
)

// CreateFunction 创建云函数
func (p *Provider) CreateFunction(ctx context.Context, req *CreateFunctionRequest) (*FunctionInfo, error) {
	p.logInfo(ctx, "Creating function: %s in namespace: %s", req.FunctionName, req.Namespace)

	// 构建请求
	request := scf.NewCreateFunctionRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Runtime = common.StringPtr(req.Runtime)
	request.Namespace = common.StringPtr(req.Namespace)
	request.Description = common.StringPtr(req.Description)

	// 设置代码
	request.Code = &scf.Code{}

	// 优先使用COS部署，如果COS信息完整
	if req.COSBucket != "" && req.COSPath != "" && req.COSRegion != "" {
		request.Code.CosBucketName = common.StringPtr(req.COSBucket)
		request.Code.CosObjectName = common.StringPtr(req.COSPath)
		request.Code.CosBucketRegion = common.StringPtr(req.COSRegion)
	} else if req.ZipFile != "" {
		// 使用ZipFile本地上传
		request.Code.ZipFile = common.StringPtr(req.ZipFile)
	} else {
		// 参数不完整，无法创建函数
		return nil, fmt.Errorf("函数代码参数不完整：需要提供COS信息(COSBucket、COSPath、COSRegion)或ZipFile")
	}

	// 设置内存和超时
	if req.MemorySize > 0 {
		request.MemorySize = common.Int64Ptr(req.MemorySize)
	} else {
		request.MemorySize = common.Int64Ptr(DefaultMemorySize)
	}

	if req.Timeout > 0 {
		request.Timeout = common.Int64Ptr(req.Timeout)
	} else {
		request.Timeout = common.Int64Ptr(DefaultTimeout)
	}

	// 设置环境变量
	if len(req.Environment) > 0 {
		var variables []*scf.Variable
		for key, value := range req.Environment {
			variables = append(variables, &scf.Variable{
				Key:   common.StringPtr(key),
				Value: common.StringPtr(value),
			})
		}
		request.Environment = &scf.Environment{
			Variables: variables,
		}
	}

	// 调用API创建函数
	response, err := p.scfClient.CreateFunction(request)
	if err != nil {
		// 检查是否是函数已存在的错误
		if strings.Contains(err.Error(), "ResourceInUse.Function") {
			return nil, fmt.Errorf("function already exists: %s", req.FunctionName)
		}
		return nil, fmt.Errorf("failed to create function: %w, Timeout: %d", err, *request.Timeout)
	}

	p.logInfo(ctx, "Function created successfully, RequestId: %s", *response.Response.RequestId)

	// 等待函数就绪
	if err := p.waitForFunctionReady(ctx, req.FunctionName, req.Namespace); err != nil {
		return nil, fmt.Errorf("function created but not ready: %w", err)
	}

	// 获取函数详情
	return p.GetFunction(ctx, req.FunctionName, req.Namespace)
}

// UpdateFunction 更新云函数
func (p *Provider) UpdateFunction(ctx context.Context, req *UpdateFunctionRequest) error {
	p.logInfo(ctx, "Updating function: %s in namespace: %s", req.FunctionName, req.Namespace)

	// 更新函数配置
	if req.Description != nil || req.MemorySize != nil || req.Timeout != nil || len(req.Environment) > 0 {
		configRequest := scf.NewUpdateFunctionConfigurationRequest()
		configRequest.FunctionName = common.StringPtr(req.FunctionName)
		configRequest.Namespace = common.StringPtr(req.Namespace)

		if req.Description != nil {
			configRequest.Description = req.Description
		}
		// Handler field is not supported in UpdateFunctionConfiguration
		// It needs to be updated via UpdateFunctionCode if needed
		if req.MemorySize != nil {
			configRequest.MemorySize = req.MemorySize
		}
		if req.Timeout != nil {
			configRequest.Timeout = req.Timeout
		}

		// 设置环境变量
		if len(req.Environment) > 0 {
			var variables []*scf.Variable
			for key, value := range req.Environment {
				variables = append(variables, &scf.Variable{
					Key:   common.StringPtr(key),
					Value: common.StringPtr(value),
				})
			}
			configRequest.Environment = &scf.Environment{
				Variables: variables,
			}
		}

		_, err := p.scfClient.UpdateFunctionConfiguration(configRequest)
		if err != nil {
			return fmt.Errorf("failed to update function configuration: %w", err)
		}
	}

	// 更新函数代码
	if req.ZipFile != "" || (req.COSBucket != "" && req.COSPath != "" && req.COSRegion != "") {
		codeRequest := scf.NewUpdateFunctionCodeRequest()
		codeRequest.FunctionName = common.StringPtr(req.FunctionName)
		codeRequest.Namespace = common.StringPtr(req.Namespace)

		// 优先使用COS方式更新
		if req.COSBucket != "" && req.COSPath != "" && req.COSRegion != "" {
			codeRequest.CosBucketName = common.StringPtr(req.COSBucket)
			codeRequest.CosObjectName = common.StringPtr(req.COSPath)
			codeRequest.CosBucketRegion = common.StringPtr(req.COSRegion)
			p.logInfo(ctx, "Updating function code via COS: bucket=%s, path=%s, region=%s",
				req.COSBucket, req.COSPath, req.COSRegion)
		} else if req.ZipFile != "" {
			codeRequest.ZipFile = common.StringPtr(req.ZipFile)
			p.logInfo(ctx, "Updating function code via ZipFile")
		}

		_, err := p.scfClient.UpdateFunctionCode(codeRequest)
		if err != nil {
			return fmt.Errorf("failed to update function code: %w", err)
		}
	}

	p.logInfo(ctx, "Function updated successfully")
	return nil
}

// DeleteFunction 删除云函数
func (p *Provider) DeleteFunction(ctx context.Context, functionName, namespace string) error {
	p.logInfo(ctx, "Deleting function: %s in namespace: %s", functionName, namespace)

	request := scf.NewDeleteFunctionRequest()
	request.FunctionName = common.StringPtr(functionName)
	request.Namespace = common.StringPtr(namespace)

	_, err := p.scfClient.DeleteFunction(request)
	if err != nil {
		return fmt.Errorf("failed to delete function: %w", err)
	}

	p.logInfo(ctx, "Function deleted successfully")
	return nil
}

// GetFunction 获取云函数详情
func (p *Provider) GetFunction(ctx context.Context, functionName, namespace string) (*FunctionInfo, error) {
	request := scf.NewGetFunctionRequest()
	request.FunctionName = common.StringPtr(functionName)
	request.Namespace = common.StringPtr(namespace)

	response, err := p.scfClient.GetFunction(request)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFound.Function") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get function: %w", err)
	}

	if response.Response == nil {
		return nil, nil
	}

	// 转换为通用函数信息
	info := &FunctionInfo{
		FunctionName: getString(response.Response.FunctionName),
		FunctionID:   getString(response.Response.FunctionId),
		Runtime:      getString(response.Response.Runtime),
		Namespace:    getString(response.Response.Namespace),
		Description:  getString(response.Response.Description),
		Handler:      getString(response.Response.Handler),
		Status:       getString(response.Response.Status),
		StatusDesc:   getString(response.Response.StatusDesc),
		CreateTime:   getString(response.Response.AddTime),
		UpdateTime:   getString(response.Response.ModTime),
		MemorySize:   getInt64(response.Response.MemorySize),
		Timeout:      getInt64(response.Response.Timeout),
	}

	// 转换环境变量
	if response.Response.Environment != nil && len(response.Response.Environment.Variables) > 0 {
		info.Environment = make(map[string]string)
		for _, v := range response.Response.Environment.Variables {
			if v.Key != nil && v.Value != nil {
				info.Environment[*v.Key] = *v.Value
			}
		}
	}

	return info, nil
}

// ListFunctions 列出云函数
func (p *Provider) ListFunctions(ctx context.Context, namespace string) ([]*FunctionInfo, error) {
	var functions []*FunctionInfo
	var offset int64 = 0
	limit := int64(100)

	for {
		request := scf.NewListFunctionsRequest()
		request.Namespace = common.StringPtr(namespace)
		request.Offset = common.Int64Ptr(offset)
		request.Limit = common.Int64Ptr(limit)

		response, err := p.scfClient.ListFunctions(request)
		if err != nil {
			return nil, fmt.Errorf("failed to list functions: %w", err)
		}

		if response.Response == nil || len(response.Response.Functions) == 0 {
			break
		}

		// 转换函数列表
		for _, fn := range response.Response.Functions {
			info := &FunctionInfo{
				FunctionName: getString(fn.FunctionName),
				FunctionID:   getString(fn.FunctionId),
				Runtime:      getString(fn.Runtime),
				Namespace:    getString(fn.Namespace),
				Description:  getString(fn.Description),
				Status:       getString(fn.Status),
				StatusDesc:   getString(fn.StatusDesc),
				CreateTime:   getString(fn.AddTime),
				UpdateTime:   getString(fn.ModTime),
				// MemorySize and Timeout are not available in list response
				MemorySize: 0,
				Timeout:    0,
			}
			functions = append(functions, info)
		}

		// 检查是否还有更多数据
		if len(response.Response.Functions) < int(limit) {
			break
		}
		offset += limit
	}

	return functions, nil
}

// CreateNamespace 创建命名空间
func (p *Provider) CreateNamespace(ctx context.Context, namespace, description string) error {
	p.logInfo(ctx, "Creating namespace: %s", namespace)

	request := scf.NewCreateNamespaceRequest()
	request.Namespace = common.StringPtr(namespace)
	request.Description = common.StringPtr(description)

	_, err := p.scfClient.CreateNamespace(request)
	if err != nil {
		// 检查是否是命名空间已存在的错误
		if strings.Contains(err.Error(), "ResourceInUse.Namespace") {
			p.logInfo(ctx, "Namespace already exists: %s", namespace)
			return nil
		}
		return fmt.Errorf("failed to create namespace: %w", err)
	}

	p.logInfo(ctx, "Namespace created successfully")
	return nil
}

// DeleteNamespace 删除命名空间
func (p *Provider) DeleteNamespace(ctx context.Context, namespace string) error {
	p.logInfo(ctx, "Deleting namespace: %s", namespace)

	request := scf.NewDeleteNamespaceRequest()
	request.Namespace = common.StringPtr(namespace)

	_, err := p.scfClient.DeleteNamespace(request)
	if err != nil {
		return fmt.Errorf("failed to delete namespace: %w", err)
	}

	p.logInfo(ctx, "Namespace deleted successfully")
	return nil
}

// ListNamespaces 列出命名空间
func (p *Provider) ListNamespaces(ctx context.Context) ([]*NamespaceInfo, error) {
	var namespaces []*NamespaceInfo
	var offset int64 = 0
	limit := int64(100)

	for {
		request := scf.NewListNamespacesRequest()
		request.Offset = common.Int64Ptr(offset)
		request.Limit = common.Int64Ptr(limit)

		response, err := p.scfClient.ListNamespaces(request)
		if err != nil {
			return nil, fmt.Errorf("failed to list namespaces: %w", err)
		}

		if response.Response == nil || len(response.Response.Namespaces) == 0 {
			break
		}

		// 转换命名空间列表
		for _, ns := range response.Response.Namespaces {
			info := &NamespaceInfo{
				Name:        getString(ns.Name),
				Description: getString(ns.Description),
				CreateTime:  getString(ns.AddTime),
				UpdateTime:  getString(ns.ModTime),
			}
			namespaces = append(namespaces, info)
		}

		// 检查是否还有更多数据
		if len(response.Response.Namespaces) < int(limit) {
			break
		}
		offset += limit
	}

	return namespaces, nil
}

// CreateTrigger 创建触发器
func (p *Provider) CreateTrigger(ctx context.Context, req *CreateTriggerRequest) error {
	p.logInfo(ctx, "Creating trigger: %s for function: %s", req.TriggerName, req.FunctionName)

	request := scf.NewCreateTriggerRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.TriggerName = common.StringPtr(req.TriggerName)
	request.Type = common.StringPtr(req.TriggerType)
	request.TriggerDesc = common.StringPtr(req.TriggerDesc)
	request.Namespace = common.StringPtr(req.Namespace)

	if req.Enable {
		request.Enable = common.StringPtr("OPEN")
	} else {
		request.Enable = common.StringPtr("CLOSE")
	}

	if req.Description != "" {
		request.Description = common.StringPtr(req.Description)
	}

	_, err := p.scfClient.CreateTrigger(request)
	if err != nil {
		// 检查是否是触发器已存在的错误
		if strings.Contains(err.Error(), "相同的触发器已经存在") || strings.Contains(err.Error(), "InvalidParameterValue") {
			p.logInfo(ctx, "Trigger already exists: %s", req.TriggerName)
			return nil
		}
		return fmt.Errorf("failed to create trigger: %w", err)
	}

	p.logInfo(ctx, "Trigger created successfully")
	return nil
}

// DeleteTrigger 删除触发器
func (p *Provider) DeleteTrigger(ctx context.Context, functionName, triggerName, namespace string) error {
	p.logInfo(ctx, "Deleting trigger: %s for function: %s", triggerName, functionName)

	request := scf.NewDeleteTriggerRequest()
	request.FunctionName = common.StringPtr(functionName)
	request.TriggerName = common.StringPtr(triggerName)
	request.Type = common.StringPtr("timer") // TODO: 需要先查询触发器类型
	request.Namespace = common.StringPtr(namespace)

	_, err := p.scfClient.DeleteTrigger(request)
	if err != nil {
		return fmt.Errorf("failed to delete trigger: %w", err)
	}

	p.logInfo(ctx, "Trigger deleted successfully")
	return nil
}

// ListTriggers 列出触发器
func (p *Provider) ListTriggers(ctx context.Context, functionName, namespace string) ([]*TriggerInfo, error) {
	// 腾讯云SDK暂不支持直接列出触发器，需要通过获取函数详情来获取
	request := scf.NewGetFunctionRequest()
	request.FunctionName = common.StringPtr(functionName)
	request.Namespace = common.StringPtr(namespace)

	response, err := p.scfClient.GetFunction(request)
	if err != nil {
		return nil, fmt.Errorf("failed to get function for triggers: %w", err)
	}

	var triggers []*TriggerInfo
	if response.Response != nil && response.Response.Triggers != nil {
		for _, trigger := range response.Response.Triggers {
			info := &TriggerInfo{
				TriggerName: getString(trigger.TriggerName),
				TriggerType: getString(trigger.Type),
				TriggerDesc: getString(trigger.TriggerDesc),
				Enable:      trigger.Enable != nil && *trigger.Enable == 1,
				CreateTime:  getString(trigger.AddTime),
				UpdateTime:  getString(trigger.ModTime),
			}
			triggers = append(triggers, info)
		}
	}

	return triggers, nil
}

// waitForFunctionReady 等待函数就绪
func (p *Provider) waitForFunctionReady(ctx context.Context, functionName, namespace string) error {
	p.logInfo(ctx, "Waiting for function %s to be ready...", functionName)

	maxWaitTime := 5 * time.Minute
	startTime := time.Now()

	for {
		// 检查是否超时
		if time.Since(startTime) > maxWaitTime {
			return fmt.Errorf("timeout waiting for function to be ready")
		}

		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 获取函数状态
		info, err := p.GetFunction(ctx, functionName, namespace)
		if err != nil {
			p.logError(ctx, "Failed to get function status: %v, retrying...", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if info == nil {
			p.logError(ctx, "Function not found, retrying...")
			time.Sleep(2 * time.Second)
			continue
		}

		// 检查函数状态
		p.logInfo(ctx, "Function status: %s", info.Status)
		if info.Status == FunctionStatusActive {
			p.logInfo(ctx, "Function %s is ready!", functionName)
			return nil
		}

		if info.Status == FunctionStatusCreateFailed {
			return fmt.Errorf("function creation failed: %s", info.StatusDesc)
		}

		// 等待2秒后重试
		time.Sleep(2 * time.Second)
	}
}

// 辅助函数：安全获取字符串指针的值
func getString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// 辅助函数：安全获取int64指针的值
func getInt64(i *int64) int64 {
	if i == nil {
		return 0
	}
	return *i
}

// InvokeFunction 调用云函数
func (p *Provider) InvokeFunction(ctx context.Context, req *InvokeFunctionRequest) (*InvokeFunctionResponse, error) {
	p.logInfo(ctx, "Invoking function: %s in namespace: %s", req.FunctionName, req.Namespace)

	// 构建请求
	request := scf.NewInvokeRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.Namespace = common.StringPtr(req.Namespace)

	// 设置版本号或别名
	if req.Qualifier != "" {
		request.Qualifier = common.StringPtr(req.Qualifier)
	}

	// 转换事件数据为JSON
	var eventData string
	if req.EventData != nil {
		jsonData, err := json.Marshal(req.EventData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal event data: %w", err)
		}
		eventData = string(jsonData)
	}

	// 设置事件数据
	request.ClientContext = common.StringPtr(eventData)

	// 设置调用类型
	if req.InvokeType != "" {
		request.InvocationType = common.StringPtr(req.InvokeType)
	} else {
		request.InvocationType = common.StringPtr("RequestResponse")
	}

	// 调用函数
	response, err := p.scfClient.Invoke(request)
	if err != nil {
		return nil, fmt.Errorf("failed to invoke function: %w", err)
	}

	// 构建响应
	result := &InvokeFunctionResponse{
		RequestID: getString(response.Response.RequestId),
	}

	// 处理函数执行结果
	if response.Response.Result != nil {
		// Result结构体包含函数返回信息
		if response.Response.Result.RetMsg != nil {
			result.Result = *response.Response.Result.RetMsg
		}

		if response.Response.Result.ErrMsg != nil {
			result.ErrorMessage = *response.Response.Result.ErrMsg
		}

		if response.Response.Result.InvokeResult != nil {
			result.StatusCode = int(*response.Response.Result.InvokeResult)
		}

		if response.Response.Result.MemUsage != nil {
			result.MemoryUsage = *response.Response.Result.MemUsage
		}

		if response.Response.Result.Duration != nil {
			result.Duration = int64(*response.Response.Result.Duration)
		}

		if response.Response.Result.BillDuration != nil {
			result.BillDuration = int64(*response.Response.Result.BillDuration)
		}

		if response.Response.Result.FunctionRequestId != nil {
			result.RequestID = *response.Response.Result.FunctionRequestId
		}
	}

	p.logInfo(ctx, "Function invoked successfully, RequestId: %s, StatusCode: %d",
		result.RequestID, result.StatusCode)
	return result, nil
}
