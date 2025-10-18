package tencent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/avast/retry-go"
	"github.com/tencentyun/cos-go-sdk-v5"
)

// UploadCOS 上传文件到腾讯云COS
func (p *Provider) UploadCOS(ctx context.Context, req *UploadCOSRequest) (*UploadCOSResponse, error) {
	if p.cosClient == nil {
		return nil, fmt.Errorf("COS client is not initialized, please check COSBucket and COSAppID configuration")
	}
	if req.Bucket == "" || req.Key == "" || len(req.Content) == 0 {
		return nil, fmt.Errorf("bucket, key and content are required")
	}
	p.logInfo(ctx, "uploading file to COS, bucket: %s, key: %s, content size: %d", req.Bucket, req.Key, len(req.Content))

	var response *cos.Response
	var err error

	// 使用重试机制上传文件
	err = retry.Do(
		func() error {
			// 创建PutObjectOptions
			opt := &cos.ObjectPutOptions{}
			if req.ContentType != "" {
				opt.ObjectPutHeaderOptions = &cos.ObjectPutHeaderOptions{
					ContentType: req.ContentType,
				}
			}

			// 执行上传
			response, err = p.cosClient.Object.Put(ctx, req.Key, strings.NewReader(string(req.Content)), opt)
			if err != nil {
				p.logError(ctx, "failed to upload file to COS, err: %v, key: %s, content size: %d", err, req.Key, len(req.Content))
				return err
			}
			return nil
		},
		retry.Attempts(5),          // 重试5次
		retry.Delay(2*time.Second), // 每次重试间隔2秒
		retry.LastErrorOnly(true),  // 仅返回最后一次错误
		retry.OnRetry(func(n uint, err error) { // 每次重试的回调
			p.logError(ctx, "retry upload to COS #%d, because got err: %s", n, err)
		}),
	)
	if err != nil {
		p.logError(ctx, "upload to COS failed after retries, err: %v", err)
		return nil, fmt.Errorf("upload to COS failed: %w", err)
	}

	// 构造返回结果
	location := ""
	etag := ""
	if response != nil {
		// 从响应头中获取Location和ETag
		if response.Header != nil {
			location = response.Header.Get("Location")
			etag = response.Header.Get("ETag")
			// 移除ETag的引号
			etag = strings.Trim(etag, "\"")
		}
	}

	p.logInfo(ctx, "successfully uploaded file to COS, key: %s, location: %s, etag: %s", req.Key, location, etag)
	return &UploadCOSResponse{
		Location: location,
		ETag:     etag,
	}, nil
}

// UploadCOSWithReader 使用Reader上传文件到腾讯云COS
func (p *Provider) UploadCOSWithReader(ctx context.Context, bucket, key string, reader io.Reader, contentType string) (*UploadCOSResponse, error) {
	if p.cosClient == nil {
		return nil, fmt.Errorf("COS client is not initialized, please check COSBucket and COSAppID configuration")
	}

	if bucket == "" || key == "" || reader == nil {
		return nil, fmt.Errorf("bucket, key and reader are required")
	}
	p.logInfo(ctx, "uploading file to COS with reader, bucket: %s, key: %s", bucket, key)

	var response *cos.Response
	var err error

	// 使用重试机制上传文件
	err = retry.Do(
		func() error {
			// 创建PutObjectOptions
			opt := &cos.ObjectPutOptions{}
			if contentType != "" {
				opt.ObjectPutHeaderOptions = &cos.ObjectPutHeaderOptions{
					ContentType: contentType,
				}
			}

			// 执行上传
			response, err = p.cosClient.Object.Put(ctx, key, reader, opt)
			if err != nil {
				p.logError(ctx, "failed to upload file to COS with reader, err: %v, key: %s", err, key)
				return err
			}
			return nil
		},
		retry.Attempts(5),          // 重试5次
		retry.Delay(2*time.Second), // 每次重试间隔2秒
		retry.LastErrorOnly(true),  // 仅返回最后一次错误
		retry.OnRetry(func(n uint, err error) { // 每次重试的回调
			p.logError(ctx, "retry upload to COS with reader #%d, because got err: %s", n, err)
		}),
	)

	if err != nil {
		p.logError(ctx, "upload to COS with reader failed after retries, err: %v", err)
		return nil, fmt.Errorf("upload to COS failed: %w", err)
	}

	// 构造返回结果
	location := ""
	etag := ""
	if response != nil {
		// 从响应头中获取Location和ETag
		if response.Header != nil {
			location = response.Header.Get("Location")
			etag = response.Header.Get("ETag")
			// 移除ETag的引号
			etag = strings.Trim(etag, "\"")
		}
	}

	p.logInfo(ctx, "successfully uploaded file to COS with reader, key: %s, location: %s, etag: %s", key, location, etag)
	return &UploadCOSResponse{
		Location: location,
		ETag:     etag,
	}, nil
}

// DeleteCOSObject 删除COS中的对象
func (p *Provider) DeleteCOSObject(ctx context.Context, bucket, key string) error {
	if p.cosClient == nil {
		return fmt.Errorf("COS client is not initialized, please check COSBucket and COSAppID configuration")
	}

	if bucket == "" || key == "" {
		return fmt.Errorf("bucket and key are required")
	}

	p.logInfo(ctx, "deleting object from COS, bucket: %s, key: %s", bucket, key)

	_, err := p.cosClient.Object.Delete(ctx, key)
	if err != nil {
		p.logError(ctx, "failed to delete object from COS, err: %v, key: %s", err, key)
		return fmt.Errorf("delete COS object failed: %w", err)
	}

	p.logInfo(ctx, "successfully deleted object from COS, key: %s", key)
	return nil
}

// GetCOSObjectURL 获取COS对象的访问URL
func (p *Provider) GetCOSObjectURL(ctx context.Context, bucket, key string, expire time.Duration) (string, error) {
	if p.cosClient == nil {
		return "", fmt.Errorf("COS client is not initialized, please check COSBucket and COSAppID configuration")
	}

	if bucket == "" || key == "" {
		return "", fmt.Errorf("bucket and key are required")
	}

	// 生成预签名URL
	presignedURL, err := p.cosClient.Object.GetPresignedURL(ctx, "GET", key, "", "", expire, nil)
	if err != nil {
		p.logError(ctx, "failed to get presigned URL for COS object, err: %v, key: %s", err, key)
		return "", fmt.Errorf("get COS object URL failed: %w", err)
	}

	p.logInfo(ctx, "generated presigned URL for COS object, key: %s, url: %s", key, presignedURL.String())
	return presignedURL.String(), nil
}
