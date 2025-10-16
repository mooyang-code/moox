# Cloud Provider

这个包提供了云厂商的统一接口和具体实现。

## 目录结构

```
provider/
├── config.go              # 云厂商配置
├── interface.go            # 通用接口定义(云函数+COS)
├── factory.go              # Provider工厂模式
├── types.go                # 通用类型定义
├── tencent_wrapper.go      # 腾讯云Provider包装器
├── cos_usage_example.go    # COS使用示例
├── README.md               # 说明文档
└── tencent/                # 腾讯云具体实现
    ├── provider.go         # 腾讯云Provider主体
    ├── functions.go        # 云函数相关实现
    ├── cos.go              # COS功能实现
    ├── types.go            # 腾讯云内部类型
    ├── models.go           # 腾讯云模型定义
    └── cos_example.go      # 腾讯云COS示例
```

## 使用方式

### 基本云函数功能

```go
// 创建Provider
config := &CloudConfig{
    Provider:  ProviderTencent,
    SecretID:  "your-secret-id",
    SecretKey: "your-secret-key",
    ExtraConfig: map[string]interface{}{
        "region": "ap-guangzhou",
    },
}

provider, err := NewProvider(config)

// 使用云函数功能
req := &CreateFunctionRequest{
    FunctionName: "test-function",
    Runtime:      "Go1",
    // ...
}
info, err := provider.CreateFunction(ctx, req)
```

### COS对象存储功能

```go
// 创建带COS功能的Provider
config := &CloudConfig{
    Provider:  ProviderTencent,
    SecretID:  "your-secret-id",
    SecretKey: "your-secret-key",
    ExtraConfig: map[string]interface{}{
        "region":     "ap-guangzhou",
        "cos_bucket": "your-bucket",
        "cos_app_id": "your-app-id",
    },
}

cosProvider, err := NewTencentCloudProviderWithCOS(config)

// 上传文件
uploadReq := &UploadCOSRequest{
    Bucket:      "your-bucket",
    Key:         "path/to/file.txt",
    Content:     []byte("file content"),
    ContentType: "text/plain",
}
resp, err := cosProvider.UploadCOS(ctx, uploadReq)
```

## 架构设计

这个包采用了包装器模式和工厂模式：

1. **接口定义层** (`interface.go`)：定义了通用的云厂商接口，包括云函数和COS功能
2. **具体实现层** (`tencent/`)：各云厂商的具体实现
3. **适配器层** (`tencent_wrapper.go`)：将具体实现适配到通用接口
4. **工厂层** (`factory.go`)：提供统一的创建入口

这种设计的优点：
- **解耦**：上层代码只依赖接口，不依赖具体实现
- **扩展性**：易于添加新的云厂商支持
- **一致性**：所有云厂商提供统一的API
- **类型安全**：避免了循环依赖问题