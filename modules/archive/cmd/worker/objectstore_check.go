package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/config"
)

// largeCheckBytes 是 --large 上传的随机内容大小：确认较大的日分区（因子结果）也能单次上传。
var largeCheckBytes int64 = 70 << 20

// objectstoreCheck 用 worker 的对象存储配置与凭据做冒烟和核对：只解析配置文件里的 object_store 段与它指向的凭据文件。
// 冒烟对象都写在 <prefix>_smoke/<主机名>-<时间戳>/ 下，靠桶上的生命周期规则一天后清理。输出不含凭据。
// 带 --key 与 --sha256 时只下载指定对象并比对 sha256（上线验证用）。任一必须通过的步骤失败就返回错误（退出码 1）。
func objectstoreCheck(ctx context.Context, out io.Writer, args []string, opts ...objectstore.Option) error {
	fs := flag.NewFlagSet("objectstore-check", flag.ContinueOnError)
	configPath := fs.String("config", "config/app.yaml", "含 object_store 段的 worker 配置")
	large := fs.Bool("large", false, "再单次 PUT 一个 70MB 的随机对象")
	key := fs.String("key", "", "只下载这个对象（相对 prefix 的键）并与 --sha256 比对")
	want := fs.String("sha256", "", "与 --key 一起用：期望的 sha256")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadObjectStore(*configPath)
	if err != nil {
		return err
	}
	client, err := cfg.OpenObjectStore(opts...)
	if err != nil {
		return err
	}
	info := client.Info()
	fmt.Fprintf(out, "对象存储 %s 桶 %s 前缀 %s 地址风格 %s 存储类型 %q\n", info.Endpoint, info.Bucket, info.Prefix, info.URLStyle, info.StorageClass)
	if *key != "" || *want != "" {
		if *key == "" || *want == "" {
			return errors.New("--key 与 --sha256 要一起给")
		}
		n, sha, err := client.Get(ctx, *key, io.Discard)
		if err != nil {
			return fmt.Errorf("下载 %s 失败（%s）: %w", *key, objectstore.Code(err), err)
		}
		if sha != strings.ToLower(*want) {
			return fmt.Errorf("对象 %s（%d 字节）的 sha256 是 %s，与给定的 %s 不一致", *key, n, sha, *want)
		}
		fmt.Fprintf(out, "对象 %s（%d 字节）的 sha256 一致\n", *key, n)
		return nil
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	base := fmt.Sprintf("_smoke/%s-%s/", sanitize(host), time.Now().UTC().Format("20060102T150405Z"))
	step := func(n int, format string, a ...any) {
		fmt.Fprintf(out, "第 %d 步 "+format+"\n", append([]any{n}, a...)...)
	}

	// 1. 单次 PUT 1KB（带 Content-MD5）→ HEAD 大小一致、ETag 等于 MD5。
	small, cleanup, err := randomFile(1 << 10)
	if err != nil {
		return err
	}
	defer cleanup()
	smallKey := base + "small.bin"
	obj, sums, err := putAndHead(ctx, client, smallKey, small)
	if err != nil {
		return fmt.Errorf("第 1 步 上传 %s 失败: %w", smallKey, err)
	}
	step(1, "通过：PUT %s，HEAD 大小 %d、ETag 等于 MD5", smallKey, obj.Size)
	// 2. 设了 storage_class 时看 HEAD 返回的存储类型。
	switch {
	case info.StorageClass == "":
		step(2, "跳过：没有配置 storage_class")
	case obj.StorageClass == "":
		step(2, "警告：HEAD 没有返回存储类型（有的后端只回专有头），请到控制台对象详情里确认是 %s", info.StorageClass)
	case obj.StorageClass != info.StorageClass:
		return fmt.Errorf("第 2 步 存储类型是 %s，与配置的 %s 不一致", obj.StorageClass, info.StorageClass)
	default:
		step(2, "通过：存储类型 %s", obj.StorageClass)
	}
	// 3. GET 回来重算 sha256。
	if _, sha, err := client.Get(ctx, smallKey, io.Discard); err != nil || sha != sums.SHA256 {
		return fmt.Errorf("第 3 步 下载 %s 校验失败（sha256 %s）: %v", smallKey, sha, err)
	}
	step(3, "通过：GET 回来 sha256 一致")
	// 4. 改坏请求体再 PUT：必须按 Content-MD5 被拒。
	corrupt, err := cfg.OpenObjectStore(append(append([]objectstore.Option(nil), opts...), objectstore.WithRoundTripper(objectstore.CorruptPutBody))...)
	if err != nil {
		return err
	}
	if _, err := corrupt.Put(ctx, base+"corrupt.bin", small, 1<<10, sums, objectstore.Meta{}); err == nil {
		return errors.New("第 4 步 改坏的请求体被接受了：后端没有按 Content-MD5 校验")
	} else if !errors.Is(err, objectstore.ErrBadDigest) {
		return fmt.Errorf("第 4 步 改坏的请求体没有以 BadDigest/InvalidDigest 被拒: %w", err)
	} else {
		step(4, "通过：改坏的请求体被拒（%s）", objectstore.Code(err))
	}
	// 5. --large：单次 PUT 70MB。
	if *large {
		big, cleanupBig, err := randomFile(largeCheckBytes)
		if err != nil {
			return err
		}
		defer cleanupBig()
		bigKey := base + "large.bin"
		obj, bigSums, err := putAndHead(ctx, client, bigKey, big)
		if err != nil {
			return fmt.Errorf("第 5 步 上传 %s 失败: %w", bigKey, err)
		}
		if _, sha, err := client.Get(ctx, bigKey, io.Discard); err != nil || sha != bigSums.SHA256 {
			return fmt.Errorf("第 5 步 下载 %s 校验失败: %v", bigKey, err)
		}
		step(5, "通过：单次 PUT %d 字节，ETag 等于 MD5，GET 回来 sha256 一致", obj.Size)
	} else {
		step(5, "跳过：没有 --large")
	}
	// 6. DELETE 必须被拒（权限策略不给删除）。
	if err := client.Delete(ctx, smallKey); err == nil {
		return errors.New("第 6 步 删除成功了：权限策略给了删除")
	} else if !errors.Is(err, objectstore.ErrAccessDenied) {
		return fmt.Errorf("第 6 步 删除没有以 403 被拒: %w", err)
	}
	step(6, "通过：删除被拒（403）")
	// 7. PUT 到前缀之外必须被拒（权限限定了前缀）。
	outsideCfg := *cfg.ObjectStore
	outsideCfg.Prefix = strings.TrimSuffix(outsideCfg.Prefix, "/") + "-outside/"
	creds, err := objectstore.LoadCredentials(cfg.ObjectStoreCredentialsPath())
	if err != nil {
		return err
	}
	outside, err := objectstore.New(outsideCfg, creds, opts...)
	if err != nil {
		return err
	}
	if _, err := outside.Put(ctx, base+"outside.bin", small, 1<<10, sums, objectstore.Meta{}); err == nil {
		return fmt.Errorf("第 7 步 写到前缀之外（%s）成功了：权限没有限定前缀", outsideCfg.Prefix)
	} else if !errors.Is(err, objectstore.ErrAccessDenied) {
		return fmt.Errorf("第 7 步 写到前缀之外没有以 403 被拒: %w", err)
	}
	step(7, "通过：写到前缀之外（%s）被拒（403）", outsideCfg.Prefix)
	// 8. 换成另一种地址风格 HEAD 第 1 步的对象：只打印结果。
	otherCfg := *cfg.ObjectStore
	otherCfg.URLStyle = map[string]string{objectstore.URLStyleVirtual: objectstore.URLStylePath, objectstore.URLStylePath: objectstore.URLStyleVirtual}[otherCfg.URLStyle]
	if other, err := objectstore.New(otherCfg, creds, opts...); err == nil {
		if _, err := other.Head(ctx, smallKey); err != nil {
			step(8, "记录：%s 风格 HEAD 失败（%s）：%v", otherCfg.URLStyle, firstNonEmpty(objectstore.Code(err), "无错误码"), err)
		} else {
			step(8, "记录：%s 风格 HEAD 也能成功", otherCfg.URLStyle)
		}
	}
	// 9. 预签名 GET 地址，用普通 HTTP 客户端下载：只打印结果（为以后的控制台下载探路）。
	if u, err := client.PresignGet(ctx, smallKey, 10*time.Minute); err != nil {
		step(9, "记录：生成预签名地址失败：%v", err)
	} else {
		step(9, "记录：%s", presignedDownload(ctx, u.String(), sums.SHA256, opts))
	}
	// 10. HEAD 前缀下不存在的键必须是 404：没有列举权限时有的 S3 实现回 403，那样维护抽查发现不了远端副本丢失。
	if _, err := client.Head(ctx, base+"missing.bin"); !errors.Is(err, objectstore.ErrNotFound) {
		return fmt.Errorf("第 10 步 HEAD 不存在的对象没有返回 404（%v）：维护抽查将无法发现远端副本丢失", err)
	}
	step(10, "通过：HEAD 不存在的对象返回 404")
	fmt.Fprintln(out, "对象存储核对通过")
	return nil
}

// presignedDownload 用普通 HTTP 客户端（不带签名头）下载预签名地址。
func presignedDownload(ctx context.Context, url, wantSHA string, opts []objectstore.Option) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "预签名下载失败：" + err.Error()
	}
	rsp, err := (&http.Client{Timeout: time.Minute, Transport: objectstore.Transport(opts...)}).Do(req)
	if err != nil {
		// url.Error 里带着完整的预签名地址（含签名），只打印内层错误。
		var uerr *neturl.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return "预签名下载失败：" + err.Error()
	}
	defer rsp.Body.Close()
	h := sha256.New()
	_, _ = io.Copy(h, io.LimitReader(rsp.Body, 16<<20))
	return fmt.Sprintf("预签名下载 HTTP %d，sha256 一致：%t", rsp.StatusCode, rsp.StatusCode == http.StatusOK && hex.EncodeToString(h.Sum(nil)) == wantSHA)
}

func putAndHead(ctx context.Context, client *objectstore.Client, key string, f *os.File) (objectstore.Object, objectstore.Sums, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return objectstore.Object{}, objectstore.Sums{}, err
	}
	size, sums, err := objectstore.Digest(f)
	if err != nil {
		return objectstore.Object{}, sums, err
	}
	etag, err := client.Put(ctx, key, f, size, sums, objectstore.Meta{ContentType: "application/octet-stream", SHA256: sums.SHA256})
	if err != nil {
		return objectstore.Object{}, sums, fmt.Errorf("%s: %w", firstNonEmpty(objectstore.Code(err), "无错误码"), err)
	}
	obj, err := client.Head(ctx, key)
	if err != nil {
		return obj, sums, err
	}
	if obj.Size != size || obj.ETag != sums.MD5 || etag != sums.MD5 {
		return obj, sums, fmt.Errorf("HEAD 大小 %d、ETag %s，本地大小 %d、MD5 %s", obj.Size, obj.ETag, size, sums.MD5)
	}
	return obj, sums, nil
}

// randomFile 在临时目录写一个 size 字节的随机文件。
func randomFile(size int64) (*os.File, func(), error) {
	f, err := os.CreateTemp("", "moox-objectstore-check-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = f.Close(); _ = os.Remove(f.Name()) }
	if _, err := io.CopyN(f, rand.Reader, size); err != nil {
		cleanup()
		return nil, nil, err
	}
	return f, cleanup, nil
}

func sanitize(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' {
			return r
		}
		return '-'
	}, name)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
