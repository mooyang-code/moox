package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/objectstore/fakes3"
)

const (
	checkBucket = "moox-archive-1250000000"
	checkSecret = "SecretValueThatMustNotBePrinted"
)

// smokeConfig 写一份只有 object_store 段的临时配置和 0600 的凭据文件（本机冒烟的做法）。
func smokeConfig(t *testing.T, endpoint, extra string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, "secrets", "archive-object-store.env")
	if err := os.WriteFile(creds, []byte("ACCESS_KEY_ID=AKIDcheck\nSECRET_ACCESS_KEY="+checkSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config", "app.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "object_store:\n  endpoint: " + endpoint + "\n  region: ap-nanjing\n  bucket: " + checkBucket +
		"\n  url_style: path\n  use_tls: false\n  credentials_file: ../secrets/archive-object-store.env\n" + extra
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return config
}

// restrictedHook 模拟受限的子账号：不给删除，不给前缀之外的写入。
func restrictedHook(r *http.Request, key string) (int, string, bool) {
	if r.Method == http.MethodDelete || !strings.HasPrefix(key, objectstore.DefaultPrefix) {
		return http.StatusForbidden, "AccessDenied", true
	}
	return 0, "", false
}

func TestObjectstoreCheckPassesStepsOneToSevenOnTheFakeS3(t *testing.T) {
	srv := fakes3.New(t, checkBucket, false)
	srv.Hook = restrictedHook
	saved := largeCheckBytes
	largeCheckBytes = 3 << 20
	t.Cleanup(func() { largeCheckBytes = saved })
	config := smokeConfig(t, "cos.ap-nanjing.myqcloud.com", "  storage_class: STANDARD_IA\n")
	var out bytes.Buffer
	err := objectstoreCheck(context.Background(), &out, []string{"--config", config, "--large"},
		objectstore.WithRoundTripper(func(http.RoundTripper) http.RoundTripper { return srv.Transport() }))
	if err != nil {
		t.Fatalf("核对失败: %v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{"第 1 步 通过", "第 2 步 通过：存储类型 STANDARD_IA", "第 3 步 通过", "第 4 步 通过：改坏的请求体被拒（BadDigest）",
		"第 5 步 通过", "第 6 步 通过", "第 7 步 通过", "第 8 步 记录", "第 9 步 记录", "第 10 步 通过", "对象存储核对通过"} {
		if !strings.Contains(text, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, checkSecret) || strings.Contains(text, "AKIDcheck") {
		t.Fatalf("输出里出现了凭据:\n%s", text)
	}
	var smoke int
	for _, key := range srv.Keys() {
		if !strings.HasPrefix(key, objectstore.DefaultPrefix+"_smoke/") {
			t.Fatalf("冒烟对象必须写在 _smoke/ 下: %s", key)
		}
		smoke++
	}
	if smoke != 2 {
		t.Fatalf("应留下小文件与大文件两个对象，实际 %v", srv.Keys())
	}
}

func TestObjectstoreCheckFailsWhenTheKeyIsNotRestricted(t *testing.T) {
	for name, hook := range map[string]func(*http.Request, string) (int, string, bool){
		"给了删除": func(r *http.Request, key string) (int, string, bool) {
			if r.Method == http.MethodDelete {
				return http.StatusNoContent, "", true
			}
			return restrictedHook(r, key)
		},
		"没有限定前缀": func(r *http.Request, key string) (int, string, bool) {
			if r.Method == http.MethodDelete {
				return http.StatusForbidden, "AccessDenied", true
			}
			return 0, "", false
		},
	} {
		srv := fakes3.New(t, checkBucket, false)
		srv.Hook = hook
		var out bytes.Buffer
		err := objectstoreCheck(context.Background(), &out, []string{"--config", smokeConfig(t, "cos.ap-nanjing.myqcloud.com", "")},
			objectstore.WithRoundTripper(func(http.RoundTripper) http.RoundTripper { return srv.Transport() }))
		want := map[string]string{"给了删除": "权限策略给了删除", "没有限定前缀": "权限没有限定前缀"}[name]
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v\n%s", name, err, out.String())
		}
	}
}

func TestObjectstoreCheckVerifiesASingleObject(t *testing.T) {
	srv := fakes3.New(t, checkBucket, false)
	content := []byte("partition bytes")
	sum := sha256.Sum256(content)
	srv.Put(objectstore.DefaultPrefix+"crypto/ds/2026/20261107.abc.csv.gz", content)
	config := smokeConfig(t, "cos.ap-nanjing.myqcloud.com", "")
	opt := objectstore.WithRoundTripper(func(http.RoundTripper) http.RoundTripper { return srv.Transport() })
	var out bytes.Buffer
	if err := objectstoreCheck(context.Background(), &out, []string{"--config", config, "--key", "crypto/ds/2026/20261107.abc.csv.gz", "--sha256", hex.EncodeToString(sum[:])}, opt); err != nil {
		t.Fatalf("一致时应通过: %v", err)
	}
	if err := objectstoreCheck(context.Background(), &out, []string{"--config", config, "--key", "crypto/ds/2026/20261107.abc.csv.gz", "--sha256", strings.Repeat("0", 64)}, opt); err == nil {
		t.Fatal("不一致时应失败")
	}
	if err := objectstoreCheck(context.Background(), &out, []string{"--config", config, "--key", "missing.csv.gz", "--sha256", strings.Repeat("0", 64)}, opt); err == nil {
		t.Fatal("对象不存在时应失败")
	}
}

func TestCheckConfigLoadsTheWholeConfigAndTheCredentials(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets")
	_ = os.MkdirAll(secrets, 0o700)
	for name, body := range map[string]string{
		"access-archive-worker.key":   strings.Repeat("k", 40),
		"storage-primary-auth.secret": strings.Repeat("s", 40),
		"archive-object-store.env":    "ACCESS_KEY_ID=AKIDcheck\nSECRET_ACCESS_KEY=" + checkSecret + "\n",
	} {
		if err := os.WriteFile(filepath.Join(secrets, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := "gateway_client:\n  access_address: access.example.test:11004\n  access_id: access@storage\n  caller: archive-worker\n  key_id: k1\n  key_file: " +
		secrets + "/access-archive-worker.key\nstorage_auth_file: " + secrets + "/storage-primary-auth.secret\nworker:\n  worker_id: archive-worker@t\n  root_dir: /data/moox-archive\n"
	store := "object_store:\n  endpoint: cos.ap-nanjing.myqcloud.com\n  region: ap-nanjing\n  bucket: " + checkBucket + "\n  url_style: virtual\n  credentials_file: " + secrets + "/archive-object-store.env\n"
	write := func(body string) string {
		path := filepath.Join(dir, "app.yaml.new")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if err := checkConfig([]string{"--config", write(base)}); err != nil {
		t.Fatalf("没配对象存储: %v", err)
	}
	if err := checkConfig([]string{"--config", write(base + store)}); err != nil {
		t.Fatalf("配了对象存储: %v", err)
	}
	if err := checkConfig([]string{"--config", write(base + strings.Replace(store, "virtual", "auto", 1))}); err == nil {
		t.Fatal("url_style 非法应失败")
	}
	_ = os.Chmod(filepath.Join(secrets, "archive-object-store.env"), 0o644)
	if err := checkConfig([]string{"--config", write(base + store)}); err == nil || strings.Contains(err.Error(), checkSecret) {
		t.Fatalf("凭据权限不对应失败且不回显值: %v", err)
	}
}
