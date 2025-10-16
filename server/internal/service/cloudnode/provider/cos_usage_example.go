package provider

import (
	"context"
	"fmt"
	"os"
	"time"
)

// ExampleCOSUsage 演示如何使用COS功能
func ExampleCOSUsage() {
	// 创建腾讯云Provider配置
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

	// 创建带COS功能的Provider实例
	provider, err := NewTencentCloudProviderWithCOS(config)
	if err != nil {
		fmt.Printf("Failed to create provider: %v\n", err)
		return
	}

	ctx := context.Background()

	// 示例1: 上传文件内容到COS
	content := []byte("Hello, COS! This is a test file.")
	uploadReq := &UploadCOSRequest{
		Bucket:      "your-bucket",
		Key:         "test/hello.txt",
		Content:     content,
		ContentType: "text/plain",
	}

	resp, err := provider.UploadCOS(ctx, uploadReq)
	if err != nil {
		fmt.Printf("Failed to upload file: %v\n", err)
		return
	}
	fmt.Printf("File uploaded successfully. Location: %s, ETag: %s\n", resp.Location, resp.ETag)

	// 示例2: 使用Reader上传本地文件
	localFile, err := os.Open("local-file.txt")
	if err != nil {
		fmt.Printf("Note: local-file.txt not found, skipping Reader example: %v\n", err)
	} else {
		defer localFile.Close()

		resp2, err := provider.UploadCOSWithReader(ctx, "your-bucket", "test/local-file.txt", localFile, "text/plain")
		if err != nil {
			fmt.Printf("Failed to upload local file: %v\n", err)
		} else {
			fmt.Printf("Local file uploaded successfully. Location: %s, ETag: %s\n", resp2.Location, resp2.ETag)
		}
	}

	// 示例3: 获取文件访问URL（24小时有效期）
	url, err := provider.GetCOSObjectURL(ctx, "your-bucket", "test/hello.txt", 24*time.Hour)
	if err != nil {
		fmt.Printf("Failed to get object URL: %v\n", err)
	} else {
		fmt.Printf("Object URL (24h expiry): %s\n", url)
	}

	// 示例4: 删除文件
	err = provider.DeleteCOSObject(ctx, "your-bucket", "test/hello.txt")
	if err != nil {
		fmt.Printf("Failed to delete object: %v\n", err)
	} else {
		fmt.Printf("Object deleted successfully\n")
	}
}

// ExampleCompressAndUpload 演示压缩文件夹并上传（参考实现风格）
func ExampleCompressAndUpload(provider CloudProviderWithCOS, localFolderPath, zipFileName, cosKey string) error {
	ctx := context.Background()

	// 1. 假设已有zipFolder函数压缩文件夹
	// if err := zipFolder(localFolderPath, zipFileName); err != nil {
	//     return fmt.Errorf("failed to zip folder: %w", err)
	// }
	// defer os.Remove(zipFileName) // 清理临时文件

	// 2. 读取压缩文件并上传
	file, err := os.Open(zipFileName)
	if err != nil {
		return fmt.Errorf("failed to open zip file: %w", err)
	}
	defer file.Close()

	// 3. 上传到COS
	resp, err := provider.UploadCOSWithReader(ctx, "your-bucket", cosKey, file, "application/zip")
	if err != nil {
		return fmt.Errorf("failed to upload to COS: %w", err)
	}

	fmt.Printf("Zip file uploaded successfully. Location: %s, ETag: %s\n", resp.Location, resp.ETag)
	return nil
}

// ExampleWithRetry 演示带重试机制的上传（类似参考实现）
func ExampleWithRetry(provider CloudProviderWithCOS, content []byte, cosKey string) error {
	ctx := context.Background()

	// 类似参考实现中的retry.Do模式
	// 这里COS上传功能已经内置了重试机制（retry.Do），
	// 所以直接调用即可，无需在业务层再次实现重试
	uploadReq := &UploadCOSRequest{
		Bucket:      "your-bucket",
		Key:         cosKey,
		Content:     content,
		ContentType: "application/octet-stream",
	}

	resp, err := provider.UploadCOS(ctx, uploadReq)
	if err != nil {
		return fmt.Errorf("upload failed after internal retries: %w", err)
	}

	fmt.Printf("Upload successful with internal retry mechanism. Location: %s\n", resp.Location)
	return nil
}