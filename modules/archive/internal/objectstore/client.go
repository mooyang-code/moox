package objectstore

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MaxPutSize 是单次 PUT 的上限（S3 规定 5GiB）；只用单次 PUT，更大的文件拒绝上传。
const MaxPutSize int64 = 5 << 30

// 超时：HEAD 固定 30 秒；PUT、GET 按上行至少 128KiB/s 估，60 秒加传输时间。
const (
	headTimeout      = 30 * time.Second
	transferMinSpeed = 128 << 10
	// maxRetries 只覆盖一次调用内的瞬时错误；跨运行的重试由 worker 的退避负责。
	maxRetries = 3
	// maxErrorText 是写进库的错误文本上限（字符）。
	maxErrorText = 500
)

// transferBase 是 PUT、GET 超时的固定部分（测试里调小）。
var transferBase = 60 * time.Second

// 哨兵错误；调用方用 errors.Is 判断。
var (
	ErrNotFound     = errors.New("对象不存在")
	ErrAccessDenied = errors.New("访问被拒绝，检查密钥与权限策略、url_style")
	ErrBadDigest    = errors.New("内容校验不符")
	ErrTooLarge     = errors.New("文件超过单次上传上限 5GiB，需要人工处理")
)

// TransferTimeout 返回传输 size 字节的超时：60 秒加按 128KiB/s 估算的传输时间。
func TransferTimeout(size int64) time.Duration {
	if size < 0 {
		size = 0
	}
	return transferBase + time.Duration(size/transferMinSpeed)*time.Second
}

// Sums 是文件内容的 MD5 与 sha256（小写十六进制）。
type Sums struct {
	MD5    string
	SHA256 string
}

// Digest 读完 r，同时算 MD5 与 sha256，返回字节数。
func Digest(r io.Reader) (int64, Sums, error) {
	m, s := md5.New(), sha256.New()
	n, err := io.Copy(io.MultiWriter(m, s), r)
	if err != nil {
		return n, Sums{}, err
	}
	return n, Sums{MD5: hex.EncodeToString(m.Sum(nil)), SHA256: hex.EncodeToString(s.Sum(nil))}, nil
}

// Meta 是上传时附带的说明：ContentType 与三项用户元数据（发成 x-amz-meta-*）。元数据只为人工查看，不能当校验用。
type Meta struct {
	ContentType string
	SHA256      string
	Dataset     string
	Partition   string
}

// Object 是 HEAD 的结果。
type Object struct {
	Size         int64
	ETag         string
	StorageClass string
	UserMetadata map[string]string
}

// Info 是不含凭据的摘要，worker 放进心跳。
type Info struct {
	Endpoint     string
	Bucket       string
	Prefix       string
	StorageClass string
	URLStyle     string
}

// Client 只用标准 S3 操作：PutObject（只用单次 PUT）、HeadObject、GetObject。
// Delete 与 PresignGet 只给 objectstore-check 核对权限、探路用，归档流程不调用。
type Client struct {
	mc  *minio.Client
	cfg Config
}

// Option 调整客户端（测试与冒烟用）。
type Option func(*options)

type options struct {
	wraps []func(http.RoundTripper) http.RoundTripper
}

// WithRoundTripper 用 wrap 包装（或替换）HTTP Transport；多个按给出的顺序一层层包上去。
func WithRoundTripper(wrap func(http.RoundTripper) http.RoundTripper) Option {
	return func(o *options) { o.wraps = append(o.wraps, wrap) }
}

// Transport 返回按 opts 包装后的 HTTP Transport（默认 DefaultTransport）。
func Transport(opts ...Option) http.RoundTripper {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	var rt http.RoundTripper = DefaultTransport()
	for _, wrap := range o.wraps {
		rt = wrap(rt)
	}
	return rt
}

// DefaultTransport 是对象存储用的 HTTP Transport：不读代理环境变量（直连，避免误走本机代理）。
func DefaultTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// New 创建客户端；不连网。cfg 必须已经通过 Validate。
func New(cfg Config, creds Credentials, opts ...Option) (*Client, error) {
	transport := Transport(opts...)
	lookup := minio.BucketLookupPath
	if cfg.URLStyle == URLStyleVirtual {
		lookup = minio.BucketLookupDNS
	}
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(creds.AccessKeyID, creds.SecretAccessKey, ""),
		Secure:       cfg.TLS(),
		Region:       cfg.Region, // 非空时不再查桶位置（GetBucketLocation）
		BucketLookup: lookup,
		Transport:    transport,
		MaxRetries:   maxRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("创建对象存储客户端: %w", err)
	}
	return &Client{mc: mc, cfg: cfg}, nil
}

// Info 返回不含凭据的摘要。
func (c *Client) Info() Info {
	return Info{Endpoint: c.cfg.Endpoint, Bucket: c.cfg.Bucket, Prefix: c.cfg.Prefix, StorageClass: c.cfg.StorageClass, URLStyle: c.cfg.URLStyle}
}

// Put 用单次 PUT 上传本地文件到 prefix+key，带 Content-MD5（服务端算出的 MD5 不符就拒绝写入）。
// sums 是调用方读文件时算好的值，sha256 写进用户元数据。超过 5GiB 返回 ErrTooLarge，不发请求。
// 返回服务端的 ETag（去掉引号、转小写）。
func (c *Client) Put(ctx context.Context, key string, f *os.File, size int64, sums Sums, meta Meta) (string, error) {
	if size > MaxPutSize {
		return "", fmt.Errorf("上传 %s: %w（%d 字节）", key, ErrTooLarge, size)
	}
	// minio-go 从文件的当前偏移开始读：调用方刚读过一遍算摘要，必须回到开头。
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("上传 %s: %w", key, err)
	}
	ctx, cancel := context.WithTimeout(ctx, TransferTimeout(size))
	defer cancel()
	userMeta := map[string]string{}
	for name, value := range map[string]string{"sha256": firstNonEmpty(meta.SHA256, sums.SHA256), "dataset": meta.Dataset, "partition": meta.Partition} {
		if value != "" {
			userMeta[name] = value
		}
	}
	info, err := c.mc.PutObject(ctx, c.cfg.Bucket, c.cfg.Prefix+key, f, size, minio.PutObjectOptions{
		// 只走单次 PUT：分片路径默认给每片加 x-amz-checksum-*，开 SendContentMd5 时还会把整片读进内存。
		DisableMultipart: true,
		// 载荷签成 UNSIGNED-PAYLOAD：否则非 TLS 连接会走 aws-chunked 流式签名，不少 S3 兼容存储不认。
		DisableContentSha256: true,
		SendContentMd5:       true,
		StorageClass:         c.cfg.StorageClass,
		ContentType:          meta.ContentType,
		UserMetadata:         userMeta,
	})
	if err != nil {
		return "", classify("上传", key, err)
	}
	return normalizeETag(info.ETag), nil
}

// Head 返回对象的大小、ETag、存储类型与用户元数据；不存在时返回 ErrNotFound。
func (c *Client) Head(ctx context.Context, key string) (Object, error) {
	ctx, cancel := context.WithTimeout(ctx, headTimeout)
	defer cancel()
	info, err := c.mc.StatObject(ctx, c.cfg.Bucket, c.cfg.Prefix+key, minio.StatObjectOptions{})
	if err != nil {
		return Object{}, classify("查询", key, err)
	}
	// HEAD 的存储类型在响应头 x-amz-storage-class 里（minio-go 不把它填进 ObjectInfo.StorageClass）；标准存储通常不返回。
	class := firstNonEmpty(info.StorageClass, info.Metadata.Get("X-Amz-Storage-Class"))
	return Object{Size: info.Size, ETag: normalizeETag(info.ETag), StorageClass: class, UserMetadata: info.UserMetadata}, nil
}

// Get 把对象写到 w，同时算 sha256；不存在时返回 ErrNotFound（桶不存在不算）。超时按对象大小计算。
// 用 Core 的 GetObject 只发一次 GET：错误带正文，分得清对象与桶不存在（Client.GetObject 的 Stat 发的是没有正文的 HEAD）。
func (c *Client) Get(ctx context.Context, key string, w io.Writer) (int64, string, error) {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 先按 60 秒等响应头，拿到大小之后再按大小放宽超时。
	limit := TransferTimeout(0)
	timer := time.AfterFunc(limit, cancel)
	defer timer.Stop()
	// 超时是本地计时器取消的：返回超时错误，不能带出 context.Canceled（调用方会当成运行被取消）。
	fail := func(n int64, err error) (int64, string, error) {
		if ctx.Err() != nil && parent.Err() == nil {
			return n, "", fmt.Errorf("下载 %s 超时（%s）: %w", key, limit, context.DeadlineExceeded)
		}
		return n, "", classify("下载", key, err)
	}
	body, info, _, err := minio.Core{Client: c.mc}.GetObject(ctx, c.cfg.Bucket, c.cfg.Prefix+key, minio.GetObjectOptions{})
	if err != nil {
		return fail(0, err)
	}
	defer body.Close()
	limit = TransferTimeout(info.Size)
	timer.Reset(limit)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), body)
	if err != nil {
		return fail(n, err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// Stream 是一次流式 GET 的响应体：Size 是对象大小；读完或不用时必须 Close。超时按对象大小计算，与 Get 相同。
type Stream struct {
	Size   int64
	key    string
	body   io.ReadCloser
	ctx    context.Context
	parent context.Context
	limit  time.Duration
	stop   func()
}

// Read 读响应体；本地计时器到期时报超时（不带出 context.Canceled）。
func (s *Stream) Read(p []byte) (int, error) {
	n, err := s.body.Read(p)
	if err != nil && err != io.EOF {
		if s.ctx.Err() != nil && s.parent.Err() == nil {
			return n, fmt.Errorf("下载 %s 超时（%s）: %w", s.key, s.limit, context.DeadlineExceeded)
		}
		return n, classify("下载", s.key, err)
	}
	return n, err
}

// Close 关闭响应体并停止计时。
func (s *Stream) Close() error {
	err := s.body.Close()
	s.stop()
	return err
}

// Open 发一次 GET 并返回响应体，由调用方边读边处理（Get 要写完才返回 sha256，装不下边读边转发）。
// 不存在时返回 ErrNotFound；先按 60 秒等响应头，拿到大小后按大小放宽超时，计时器与取消挂在 Stream.Close 上。
func (c *Client) Open(ctx context.Context, key string) (*Stream, error) {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	limit := TransferTimeout(0)
	timer := time.AfterFunc(limit, cancel)
	stop := func() { timer.Stop(); cancel() }
	body, info, _, err := minio.Core{Client: c.mc}.GetObject(ctx, c.cfg.Bucket, c.cfg.Prefix+key, minio.GetObjectOptions{})
	if err != nil {
		timedOut := ctx.Err() != nil && parent.Err() == nil
		stop()
		if timedOut {
			return nil, fmt.Errorf("下载 %s 超时（%s）: %w", key, limit, context.DeadlineExceeded)
		}
		return nil, classify("下载", key, err)
	}
	limit = TransferTimeout(info.Size)
	timer.Reset(limit)
	return &Stream{Size: info.Size, key: key, body: body, ctx: ctx, parent: parent, limit: limit, stop: stop}, nil
}

// Delete 删除对象。只给 objectstore-check 核对“权限策略不给删除”用。
func (c *Client) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, headTimeout)
	defer cancel()
	if err := c.mc.RemoveObject(ctx, c.cfg.Bucket, c.cfg.Prefix+key, minio.RemoveObjectOptions{}); err != nil {
		return classify("删除", key, err)
	}
	return nil
}

// PresignGet 生成预签名 GET 地址。只给 objectstore-check 为以后的控制台下载探路用。
func (c *Client) PresignGet(ctx context.Context, key string, expiry time.Duration) (*url.URL, error) {
	u, err := c.mc.PresignedGetObject(ctx, c.cfg.Bucket, c.cfg.Prefix+key, expiry, nil)
	if err != nil {
		return nil, classify("生成下载地址", key, err)
	}
	return u, nil
}

// classify 按 S3 错误码与 HTTP 状态把错误归到哨兵错误；HEAD 的错误响应没有正文，Code 可能为空，所以也看状态码。
func classify(action, key string, err error) error {
	resp := minio.ToErrorResponse(err)
	switch {
	// 桶不存在（endpoint、桶名配错）不是“对象不存在”：不能据此把远端副本记成丢失。
	case resp.Code == "NoSuchBucket":
		return fmt.Errorf("%s %s: 桶不存在，检查 endpoint、bucket 与 url_style: %w", action, key, err)
	case resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey" || resp.Code == "NotFound":
		return fmt.Errorf("%s %s: %w: %w", action, key, ErrNotFound, err)
	case resp.StatusCode == http.StatusForbidden || resp.Code == "AccessDenied" || resp.Code == "SignatureDoesNotMatch" || resp.Code == "InvalidAccessKeyId":
		return fmt.Errorf("%s %s: %w（%s）: %w", action, key, ErrAccessDenied, firstNonEmpty(resp.Code, "403"), err)
	case resp.Code == "BadDigest" || resp.Code == "InvalidDigest" || resp.Code == "XAmzContentSHA256Mismatch":
		return fmt.Errorf("%s %s: %w（%s）: %w", action, key, ErrBadDigest, resp.Code, err)
	}
	return fmt.Errorf("%s %s: %w", action, key, err)
}

// Code 返回错误链里的 S3 错误码（没有时为空），冒烟时打印用。
func Code(err error) string {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code
	}
	return ""
}

// ErrorText 把错误截到 500 个字符，写进库与上报用。
func ErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if utf8.RuneCountInString(text) <= maxErrorText {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxErrorText])
}

func normalizeETag(etag string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(etag), `"`))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// ContentType 按文件名给出上传时的 Content-Type；不设 Content-Encoding，免得下载时被悄悄解压。
func ContentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".csv.gz"):
		return "application/gzip"
	case strings.HasSuffix(name, ".csv"):
		return "text/csv; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	}
	return "application/octet-stream"
}

// VersionedKey 由分区文件的相对路径与内容 sha256 生成对象键：在扩展名（.csv.gz 或 .csv）前插入 sha256 前 12 位，
// 例如 crypto/ds/2026/20261107.csv.gz → crypto/ds/2026/20261107.3f9a0c1b2d4e.csv.gz。键只由内容决定，每个版本一个对象。
func VersionedKey(filePath, sha256hex string) (string, error) {
	if len(sha256hex) < 12 || filePath == "" {
		return "", fmt.Errorf("生成对象键需要文件路径和 sha256（%q）", filePath)
	}
	short := sha256hex[:12]
	for _, ext := range []string{".csv.gz", ".csv"} {
		if strings.HasSuffix(filePath, ext) {
			return strings.TrimSuffix(filePath, ext) + "." + short + ext, nil
		}
	}
	return "", fmt.Errorf("分区文件 %s 的扩展名不是 .csv.gz 或 .csv", filePath)
}
