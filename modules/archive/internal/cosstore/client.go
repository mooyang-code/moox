package cosstore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/tencentyun/cos-go-sdk-v5"
)

type ObjectClient interface {
	Put(ctx context.Context, key, localPath string, metadata ObjectMetadata) error
	Head(ctx context.Context, key string) (ObjectMetadata, error)
}

type ObjectMetadata struct {
	SHA256 string
	Size   int64
}

type Client struct {
	cos    *cos.Client
	bucket string
	root   string
	prefix string
}

func New(region, bucket, root, prefix, secretID, secretKey string) (*Client, error) {
	region = strings.TrimSpace(region)
	bucket = strings.TrimSpace(bucket)
	if region == "" || bucket == "" {
		return nil, fmt.Errorf("cos region and bucket are required")
	}
	if secretID == "" {
		secretID = os.Getenv("TENCENTCLOUD_SECRET_ID")
	}
	if secretKey == "" {
		secretKey = os.Getenv("TENCENTCLOUD_SECRET_KEY")
	}
	if secretID == "" || secretKey == "" {
		return nil, fmt.Errorf("cos credentials are required")
	}
	bucketURL, err := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com", bucket, region))
	if err != nil {
		return nil, err
	}
	return &Client{cos: cos.NewClient(&cos.BaseURL{BucketURL: bucketURL}, &http.Client{Transport: &cos.AuthorizationTransport{SecretID: secretID, SecretKey: secretKey}}), bucket: bucket, root: root, prefix: strings.Trim(prefix, "/")}, nil
}

func (c *Client) Put(ctx context.Context, key, localPath string, metadata ObjectMetadata) error {
	if c == nil || c.cos == nil {
		return fmt.Errorf("cos client is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	headers := http.Header{}
	headers.Set("x-cos-meta-moox-sha256", metadata.SHA256)
	_, err = c.cos.Object.Put(ctx, strings.TrimPrefix(key, "/"), file, &cos.ObjectPutOptions{
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
			ContentLength: metadata.Size,
			XCosMetaXXX:   &headers,
		},
	})
	return err
}

func (c *Client) Head(ctx context.Context, key string) (ObjectMetadata, error) {
	if c == nil || c.cos == nil {
		return ObjectMetadata{}, fmt.Errorf("cos client is nil")
	}
	response, err := c.cos.Object.Head(ctx, strings.TrimPrefix(key, "/"), nil)
	if err != nil {
		return ObjectMetadata{}, err
	}
	defer response.Body.Close()
	return ObjectMetadata{
		SHA256: response.Header.Get("x-cos-meta-moox-sha256"),
		Size:   response.ContentLength,
	}, nil
}

func ObjectKey(root, prefix, localPath string) (string, error) {
	partition, err := domain.ParseArchivePath(localPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, localPath)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("path %s is outside archive root", localPath)
	}
	expected, err := partition.RelativePath()
	if err != nil {
		return "", err
	}
	if filepath.Clean(rel) != filepath.Clean(expected) {
		return "", fmt.Errorf("archive path does not match partition identity")
	}
	key := filepath.ToSlash(rel)
	if prefix != "" {
		key = strings.Trim(prefix, "/") + "/" + key
	}
	return key, nil
}
