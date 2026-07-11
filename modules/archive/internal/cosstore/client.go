package cosstore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/tencentyun/cos-go-sdk-v5"
)

type ObjectClient interface {
	Put(ctx context.Context, key, localPath string) error
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

func (c *Client) Put(ctx context.Context, key, localPath string) error {
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
	_, err = c.cos.Object.Put(ctx, strings.TrimPrefix(key, "/"), file, nil)
	return err
}

func ObjectKey(root, prefix, localPath string) (string, error) {
	rel, err := filepath.Rel(root, localPath)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("path %s is outside archive root", localPath)
	}
	key := filepath.ToSlash(rel)
	if prefix != "" {
		key = strings.Trim(prefix, "/") + "/" + key
	}
	return key, nil
}
