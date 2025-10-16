package tencent

import (
	"context"
	"fmt"
	"os"
	"time"
)

// ExampleUploadCOS 演示如何使用COS上传功能
func ExampleUploadCOS() {
	// 配置腾讯云Provider
	config := &Config{
		SecretID:  "your-secret-id",
		SecretKey: "your-secret-key",
		Region:    "ap-guangzhou",
		COSBucket: "your-bucket",
		COSAppID:  "your-app-id",
	}

	// 创建Provider实例
	provider, err := NewProvider(config)
	if err != nil {
		fmt.Printf("Failed to create provider: %v\n", err)
		return
	}

	ctx := context.Background()

	// 示例1: 上传文件内容
	content := []byte("Hello, COS!")
	uploadReq := &UploadCOSRequest{
		Bucket:      config.COSBucket,
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

	// 示例2: 上传本地文件
	localFile, err := os.Open("local-file.txt")
	if err != nil {
		fmt.Printf("Failed to open local file: %v\n", err)
		return
	}
	defer localFile.Close()

	resp2, err := provider.UploadCOSWithReader(ctx, config.COSBucket, "test/local-file.txt", localFile, "text/plain")
	if err != nil {
		fmt.Printf("Failed to upload local file: %v\n", err)
		return
	}
	fmt.Printf("Local file uploaded successfully. Location: %s, ETag: %s\n", resp2.Location, resp2.ETag)

	// 示例3: 获取文件访问URL
	url, err := provider.GetCOSObjectURL(ctx, config.COSBucket, "test/hello.txt", 24*time.Hour)
	if err != nil {
		fmt.Printf("Failed to get object URL: %v\n", err)
		return
	}
	fmt.Printf("Object URL: %s\n", url)

	// 示例4: 删除文件
	err = provider.DeleteCOSObject(ctx, config.COSBucket, "test/hello.txt")
	if err != nil {
		fmt.Printf("Failed to delete object: %v\n", err)
		return
	}
	fmt.Printf("Object deleted successfully\n")
}

// ExampleUploadCOSFromZip 演示如何压缩并上传文件夹（类似参考实现）
func ExampleUploadCOSFromZip(provider *Provider, localFolderPath, zipFileName, cosFilePath string) error {
	ctx := context.Background()

	// 1. 压缩文件夹（需要自己实现zipFolder函数）
	if err := zipFolder(localFolderPath, zipFileName); err != nil {
		return fmt.Errorf("failed to zip folder: %w", err)
	}
	defer os.Remove(zipFileName) // 清理临时文件

	// 2. 读取压缩文件
	file, err := os.Open(zipFileName)
	if err != nil {
		return fmt.Errorf("failed to open zip file: %w", err)
	}
	defer file.Close()

	// 3. 上传到COS
	resp, err := provider.UploadCOSWithReader(ctx, "your-bucket", cosFilePath, file, "application/zip")
	if err != nil {
		return fmt.Errorf("failed to upload to COS: %w", err)
	}

	fmt.Printf("Zip file uploaded successfully. Location: %s, ETag: %s\n", resp.Location, resp.ETag)
	return nil
}

// zipFolder 压缩文件夹的示例实现（可以从参考代码中复制）
func zipFolder(sourceDir, zipFilePath string) error {
	// 这里可以实现文件夹压缩逻辑
	// 可以参考参考实现中的zipFolder函数
	return nil
}