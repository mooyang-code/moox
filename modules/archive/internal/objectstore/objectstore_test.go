package objectstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore/fakes3"
)

const (
	testBucket = "moox-archive-1250000000"
	testKeyID  = "AKIDtestaccesskey0001"
	testSecret = "secretTestValue-ShouldNeverLeak"
)

func boolPtr(v bool) *bool { return &v }

func validConfig() Config {
	return Config{Endpoint: "cos.ap-nanjing.myqcloud.com", Region: "ap-nanjing", Bucket: testBucket, URLStyle: URLStyleVirtual, CredentialsFile: "../secrets/archive-object-store.env"}
}

func TestConfigValidateAppliesDefaultsAndRejectsEachBadField(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("合法配置被拒: %v", err)
	}
	if cfg.Prefix != DefaultPrefix || !cfg.TLS() {
		t.Fatalf("默认值 prefix=%q tls=%v", cfg.Prefix, cfg.TLS())
	}
	for _, ok := range []func(*Config){
		func(c *Config) { c.Endpoint = "127.0.0.1:9000" },
		func(c *Config) { c.Endpoint = "[::1]:9000" },
		func(c *Config) { c.Endpoint = "minio.local" },
		func(c *Config) { c.URLStyle = URLStylePath; c.UseTLS = boolPtr(false) },
		func(c *Config) { c.StorageClass = "STANDARD_IA"; c.Prefix = "a/b-c_d.e/" },
	} {
		cfg := validConfig()
		ok(&cfg)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("合法配置被拒 %+v: %v", cfg, err)
		}
	}
	for name, bad := range map[string]func(*Config){
		"endpoint 空":         func(c *Config) { c.Endpoint = "" },
		"endpoint 带协议":       func(c *Config) { c.Endpoint = "https://cos.ap-nanjing.myqcloud.com" },
		"endpoint 带路径":       func(c *Config) { c.Endpoint = "cos.example.com/x" },
		"endpoint 有空白":       func(c *Config) { c.Endpoint = "cos example" },
		"endpoint 端口空":       func(c *Config) { c.Endpoint = "cos.example.com:" },
		"endpoint 多个冒号":      func(c *Config) { c.Endpoint = "a:b:c" },
		"region 空":           func(c *Config) { c.Region = "" },
		"bucket 大写":          func(c *Config) { c.Bucket = "Moox" },
		"bucket 太短":          func(c *Config) { c.Bucket = "ab" },
		"prefix 不以斜杠结尾":      func(c *Config) { c.Prefix = "moox-archive" },
		"prefix 以斜杠开头":       func(c *Config) { c.Prefix = "/moox/" },
		"prefix 含双斜杠":        func(c *Config) { c.Prefix = "a//b/" },
		"prefix 含点点":         func(c *Config) { c.Prefix = "a/../b/" },
		"prefix 含空白":         func(c *Config) { c.Prefix = "a b/" },
		"url_style 空":        func(c *Config) { c.URLStyle = "" },
		"url_style 非法":       func(c *Config) { c.URLStyle = "auto" },
		"storage_class 小写":   func(c *Config) { c.StorageClass = "standard" },
		"credentials_file 空": func(c *Config) { c.CredentialsFile = "" },
	} {
		cfg := validConfig()
		bad(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s：应被拒", name)
		}
	}
}

func writeCreds(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive-object-store.env")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCredentialsAcceptsOnlyTheStrictFormat(t *testing.T) {
	good := "# 归档对象存储\n\nACCESS_KEY_ID=" + testKeyID + "\nSECRET_ACCESS_KEY=" + testSecret + "\n"
	creds, err := LoadCredentials(writeCreds(t, good, 0o600))
	if err != nil || creds.AccessKeyID != testKeyID || creds.SecretAccessKey != testSecret {
		t.Fatalf("creds=%+v err=%v", creds, err)
	}
	for name, body := range map[string]string{
		"重复键":    good + "ACCESS_KEY_ID=again\n",
		"未知键":    good + "SESSION_TOKEN=x\n",
		"空值":     "ACCESS_KEY_ID=\nSECRET_ACCESS_KEY=" + testSecret + "\n",
		"缺键":     "ACCESS_KEY_ID=" + testKeyID + "\n",
		"带引号":    "ACCESS_KEY_ID=\"" + testKeyID + "\"\nSECRET_ACCESS_KEY=" + testSecret + "\n",
		"export": "export ACCESS_KEY_ID=" + testKeyID + "\nSECRET_ACCESS_KEY=" + testSecret + "\n",
		"值含空白":   "ACCESS_KEY_ID=" + testKeyID + "\nSECRET_ACCESS_KEY=a b" + testSecret + "\n",
		"不是键值":   "ACCESS_KEY_ID\n",
	} {
		_, err := LoadCredentials(writeCreds(t, body, 0o600))
		if err == nil {
			t.Fatalf("%s：应被拒", name)
		}
		if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testKeyID) {
			t.Fatalf("%s：错误里回显了值: %v", name, err)
		}
	}
	if _, err := LoadCredentials(writeCreds(t, good, 0o640)); err == nil || !strings.Contains(err.Error(), "权限") {
		t.Fatalf("组可读应被拒: %v", err)
	}
	target := writeCreds(t, good, 0o600)
	link := filepath.Join(t.TempDir(), "link.env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(link); err == nil {
		t.Fatal("符号链接应被拒")
	}
}

func TestVersionedKeyInsertsShortSHABeforeTheExtension(t *testing.T) {
	sha := "3f9a0c1b2d4e5f60718293a4b5c6d7e8f9012345678901234567890abcdef12"
	for in, want := range map[string]string{
		"crypto/dataset_x/2026/20261107.csv.gz": "crypto/dataset_x/2026/20261107.3f9a0c1b2d4e.csv.gz",
		"crypto/dataset_x/2026/202611.csv":      "crypto/dataset_x/2026/202611.3f9a0c1b2d4e.csv",
	} {
		if got, err := VersionedKey(in, sha); err != nil || got != want {
			t.Fatalf("VersionedKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := VersionedKey("a/b.parquet", sha); err == nil {
		t.Fatal("未知扩展名应报错")
	}
}

// env 是一个连到假 S3 的客户端。
type env struct {
	srv    *fakes3.Server
	client *Client
	cfg    Config
}

func newEnv(t *testing.T, style string, useTLS bool, mutate func(*Config)) env {
	t.Helper()
	srv := fakes3.New(t, testBucket, useTLS)
	cfg := Config{Endpoint: "cos.ap-nanjing.myqcloud.com", Region: "ap-nanjing", Bucket: testBucket, URLStyle: style, UseTLS: boolPtr(useTLS), CredentialsFile: "x"}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	client, err := New(cfg, Credentials{AccessKeyID: testKeyID, SecretAccessKey: testSecret}, WithRoundTripper(func(http.RoundTripper) http.RoundTripper { return srv.Transport() }))
	if err != nil {
		t.Fatal(err)
	}
	return env{srv: srv, client: client, cfg: cfg}
}

func tempFile(t *testing.T, content []byte) (*os.File, int64, Sums) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "part.csv.gz")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	// 和 worker 一样先读一遍算摘要（偏移停在文件末尾），Put 必须自己回到开头。
	n, sums, err := Digest(f)
	if err != nil {
		t.Fatal(err)
	}
	return f, n, sums
}

func TestPutUsesOneStandardRequestAndHeadMatchesMD5(t *testing.T) {
	for _, tc := range []struct {
		name   string
		style  string
		useTLS bool
		class  string
	}{
		{"virtual-https-ia", URLStyleVirtual, true, "STANDARD_IA"},
		{"virtual-http", URLStyleVirtual, false, ""},
		{"path-https", URLStylePath, true, ""},
		{"path-http-ia", URLStylePath, false, "STANDARD_IA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.style, tc.useTLS, func(c *Config) { c.StorageClass = tc.class })
			// 17MiB：超过 minio-go v7 的分片阈值（minPartSize = 16MiB），不设 DisableMultipart 就会走分片上传。
			content := make([]byte, 17<<20)
			_, _ = rand.Read(content)
			f, size, sums := tempFile(t, content)
			key := "crypto/ds/2026/20261107." + sums.SHA256[:12] + ".csv.gz"
			etag, err := e.client.Put(t.Context(), key, f, size, sums, Meta{ContentType: ContentType(key), Dataset: "ds", Partition: "2026-11-07"})
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if etag != sums.MD5 {
				t.Fatalf("ETag %s != MD5 %s", etag, sums.MD5)
			}
			obj := e.srv.Object(DefaultPrefix + key)
			if obj == nil || !bytes.Equal(obj.Body, content) {
				t.Fatal("对象内容与本地文件不一致")
			}
			if obj.Meta["sha256"] != sums.SHA256 || obj.Meta["dataset"] != "ds" || obj.Meta["partition"] != "2026-11-07" || obj.ContentType != "application/gzip" {
				t.Fatalf("元数据 = %+v content-type=%q", obj.Meta, obj.ContentType)
			}
			reqs := e.srv.Requests()
			if len(reqs) != 1 || reqs[0].Method != http.MethodPut {
				t.Fatalf("PUT 应只发一次请求，实际 %+v", reqs)
			}
			assertStandardPut(t, reqs[0], tc.style, tc.class)

			head, err := e.client.Head(t.Context(), key)
			if err != nil || head.Size != size || head.ETag != sums.MD5 || head.StorageClass != tc.class {
				t.Fatalf("Head = %+v, %v", head, err)
			}
			var buf bytes.Buffer
			n, sha, err := e.client.Get(t.Context(), key, &buf)
			if err != nil || n != size || sha != sums.SHA256 || !bytes.Equal(buf.Bytes(), content) {
				t.Fatalf("Get n=%d sha=%s err=%v", n, sha, err)
			}
			for _, r := range e.srv.Requests() {
				assertAddressing(t, r, tc.style)
				assertNoVendorHeaders(t, r)
			}
		})
	}
}

func assertStandardPut(t *testing.T, r fakes3.Request, style, class string) {
	t.Helper()
	if strings.Contains(r.RawQuery, "uploads") || strings.Contains(r.RawQuery, "partNumber") {
		t.Fatalf("PUT 带了分片查询参数: %s", r.RawQuery)
	}
	if r.Header.Get("Content-MD5") == "" {
		t.Fatal("PUT 必须带 Content-MD5")
	}
	if got := r.Header.Get("X-Amz-Storage-Class"); got != class {
		t.Fatalf("x-amz-storage-class = %q, want %q", got, class)
	}
	if got := r.Header.Get("X-Amz-Content-Sha256"); got != "UNSIGNED-PAYLOAD" {
		t.Fatalf("x-amz-content-sha256 = %q, want UNSIGNED-PAYLOAD", got)
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Content-Encoding")), "aws-chunked") {
		t.Fatal("PUT 不能用 aws-chunked")
	}
	for name := range r.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-checksum-") || lower == "x-amz-trailer" || lower == "x-amz-sdk-checksum-algorithm" || lower == "x-amz-decoded-content-length" {
			t.Fatalf("PUT 带了非标准校验头 %s", name)
		}
	}
	assertAddressing(t, r, style)
}

func assertAddressing(t *testing.T, r fakes3.Request, style string) {
	t.Helper()
	host := r.Host
	switch style {
	case URLStyleVirtual:
		if !strings.HasPrefix(host, testBucket+".cos.ap-nanjing.myqcloud.com") || strings.Contains(r.Path, testBucket) {
			t.Fatalf("虚拟主机风格: host=%s path=%s", host, r.Path)
		}
	case URLStylePath:
		if strings.HasPrefix(host, testBucket+".") || !strings.HasPrefix(r.Path, "/"+testBucket+"/") {
			t.Fatalf("路径风格: host=%s path=%s", host, r.Path)
		}
	}
}

func assertNoVendorHeaders(t *testing.T, r fakes3.Request) {
	t.Helper()
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-cos-") {
			t.Fatalf("请求带了厂商专有头 %s", name)
		}
	}
}

func TestPutRejectsFilesOverFiveGiBWithoutARequest(t *testing.T) {
	e := newEnv(t, URLStylePath, false, nil)
	f, _, sums := tempFile(t, []byte("small"))
	_, err := e.client.Put(t.Context(), "a/b.csv.gz", f, MaxPutSize+1, sums, Meta{})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if n := len(e.srv.Requests()); n != 0 {
		t.Fatalf("超过上限不应发请求，实际 %d 次", n)
	}
}

func TestErrorsAreClassifiedAndNeverLeakTheSecret(t *testing.T) {
	e := newEnv(t, URLStyleVirtual, true, nil)
	if _, err := e.client.Head(t.Context(), "missing.csv.gz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("HEAD 不存在 = %v", err)
	}
	if _, _, err := e.client.Get(t.Context(), "missing.csv.gz", &bytes.Buffer{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GET 不存在 = %v", err)
	}
	f, size, sums := tempFile(t, []byte("payload"))
	var status int
	var code string
	e.srv.Hook = func(*http.Request, string) (int, string, bool) { return status, code, true }
	for _, tc := range []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusForbidden, "AccessDenied", ErrAccessDenied},
		{http.StatusForbidden, "SignatureDoesNotMatch", ErrAccessDenied},
		{http.StatusBadRequest, "BadDigest", ErrBadDigest},
		{http.StatusBadRequest, "InvalidDigest", ErrBadDigest},
	} {
		status, code = tc.status, tc.code
		_, err := e.client.Put(t.Context(), "a.csv.gz", f, size, sums, Meta{})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v", tc.code, err)
		}
		if strings.Contains(err.Error(), testSecret) || strings.Contains(ErrorText(err), testSecret) {
			t.Fatalf("错误里出现了密钥: %v", err)
		}
	}
	status, code = http.StatusForbidden, ""
	if _, err := e.client.Head(t.Context(), "a.csv.gz"); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("HEAD 403 = %v", err)
	}
	status, code = http.StatusInternalServerError, "InternalError"
	_, err := e.client.Put(t.Context(), "a.csv.gz", f, size, sums, Meta{})
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrAccessDenied) || errors.Is(err, ErrBadDigest) || !strings.Contains(err.Error(), "上传 a.csv.gz") {
		t.Fatalf("5xx 应原样包装: %v", err)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatalf("错误里出现了密钥: %v", err)
	}
}

func TestPutWithAWrongContentMD5IsRejected(t *testing.T) {
	e := newEnv(t, URLStylePath, false, nil)
	content := []byte("partition bytes")
	path := filepath.Join(t.TempDir(), "p.csv")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(path)
	defer f.Close()
	_, sums, _ := Digest(f)
	flip := func(base http.RoundTripper) http.RoundTripper { return CorruptPutBody(e.srv.Transport()) }
	client, err := New(e.cfg, Credentials{AccessKeyID: testKeyID, SecretAccessKey: testSecret}, WithRoundTripper(flip))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(t.Context(), "p.csv", f, int64(len(content)), sums, Meta{}); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("改坏的请求体应被拒: %v", err)
	}
	if e.srv.Object(DefaultPrefix+"p.csv") != nil {
		t.Fatal("被拒的对象不应写入")
	}
}

func TestErrorTextIsTruncated(t *testing.T) {
	long := errors.New(strings.Repeat("错", 800))
	if got := []rune(ErrorText(long)); len(got) != 500 {
		t.Fatalf("长度 %d", len(got))
	}
	if ErrorText(nil) != "" {
		t.Fatal("nil 应为空")
	}
}

func TestMissingBucketIsNotReportedAsAMissingObject(t *testing.T) {
	e := newEnv(t, URLStylePath, false, func(c *Config) { c.Bucket = "wrong-bucket-1" })
	_, _, err := e.client.Get(t.Context(), "a.csv.gz", &bytes.Buffer{})
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "桶不存在") {
		t.Fatalf("GET 桶不存在 = %v", err)
	}
}

// 下载超时是本地计时器取消的：错误不能带出 context.Canceled（否则读回取回超时会被记成“运行已取消”）。
func TestGetTimeoutIsNotReportedAsCancellation(t *testing.T) {
	saved := transferBase
	transferBase = 50 * time.Millisecond
	t.Cleanup(func() { transferBase = saved })
	e := newEnv(t, URLStylePath, false, nil)
	e.srv.Put(DefaultPrefix+"slow.csv.gz", []byte("x"))
	e.srv.Hook = func(r *http.Request, _ string) (int, string, bool) {
		if r.Method == http.MethodGet {
			time.Sleep(300 * time.Millisecond)
		}
		return 0, "", false
	}
	_, _, err := e.client.Get(t.Context(), "slow.csv.gz", &bytes.Buffer{})
	if err == nil || errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("超时错误 = %v", err)
	}
}
