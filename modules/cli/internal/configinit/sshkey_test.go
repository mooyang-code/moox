package configinit

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 页面会同时发出多个需要密钥的请求：并发生成不能互相撞上“已存在”。
func TestEnsureSSHKeyIsSafeUnderConcurrentFirstUse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const workers = 8
	keys := make([]SSHKey, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], errs[i] = ensureSSHKey()
		}()
	}
	wg.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("第 %d 个并发请求失败: %v", i, errs[i])
		}
		if keys[i].PublicKey != keys[0].PublicKey {
			t.Fatal("并发请求应拿到同一把密钥")
		}
	}
}

func TestEnsureSSHKeyBacksUpAnUnusableKeyAndGeneratesANewOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sshKeyName)
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pub", []byte("old pub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := ensureSSHKey()
	if err != nil || !strings.HasPrefix(key.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("旧密钥不可用时应备份后生成新的: %v %+v", err, key)
	}
	backups, _ := filepath.Glob(path + ".bak-*")
	if len(backups) != 2 {
		t.Fatalf("旧私钥和公钥都应备份: %v", backups)
	}
	for _, backup := range backups {
		if !strings.HasSuffix(backup, ".pub") {
			if raw, _ := os.ReadFile(backup); string(raw) != "not a key" {
				t.Fatalf("备份内容应保持原样: %q", raw)
			}
		}
	}
	again, err := ensureSSHKey()
	if err != nil || again.PublicKey != key.PublicKey {
		t.Fatalf("可用的密钥不应再被换掉: %v", err)
	}
}

func TestPageServesItsScreenshotsOnlyWithTheToken(t *testing.T) {
	h := start(t, 0)
	resp, body := h.request(http.MethodGet, "assets/cam-select-policy.webp", nil, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/webp" || !strings.HasPrefix(body, "RIFF") {
		t.Fatalf("应返回内嵌截图: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := h.request(http.MethodGet, "assets/missing.webp", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的图片应 404: %d", resp.StatusCode)
	}
	if resp, _ := h.request(http.MethodGet, "assets/..%2Fpage.html", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不允许带目录: %d", resp.StatusCode)
	}
	if resp, _ := h.request(http.MethodPost, "assets/cam-select-policy.webp", nil, nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("图片只读: %d", resp.StatusCode)
	}
	page, _ := h.request(http.MethodGet, "", nil, nil)
	if !strings.Contains(page.Header.Get("Content-Security-Policy"), "img-src 'self'") {
		t.Fatal("页面应允许加载同源图片")
	}
}
