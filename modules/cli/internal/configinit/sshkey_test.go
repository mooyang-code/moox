package configinit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestReachRouteReportsNetworkResultWithoutNeedingCredentials(t *testing.T) {
	h := start(t, time.Minute)
	target := SSHTarget{Address: "192.168.0.103", SSHUser: "builder", SSHPort: 2222}
	status, ok := h.post("ssh/reach", target)
	if status != http.StatusOK || !ok.OK || !strings.Contains(ok.Message, "SSH-2.0-OpenSSH_9.6") {
		t.Fatalf("网络通畅应返回成功和 SSH 版本: %d %+v", status, ok)
	}
	if got := h.prober.targets; len(got) != 1 || got[0].SSHPort != 2222 {
		t.Fatalf("应按填写的端口探测: %+v", got)
	}
	h.prober.reachErr = errors.New("连不上 192.168.0.103:2222")
	if status, bad := h.post("ssh/reach", target); status != http.StatusUnprocessableEntity || bad.OK || !strings.Contains(bad.Message, "连不上") {
		t.Fatalf("连不上应 422 并给出原因: %d %+v", status, bad)
	}
	if status, bad := h.post("ssh/reach", SSHTarget{SSHUser: "builder"}); status != http.StatusUnprocessableEntity || bad.OK {
		t.Fatalf("没填 IP 应 422: %d %+v", status, bad)
	}
}

// 真实探测：对本机起的临时监听口，分别验证 SSH 问候、非 SSH 服务和端口没开三种情况。
func TestSystemProberReachTellsSSHFromOtherServicesAndClosedPorts(t *testing.T) {
	serve := func(greeting string) SSHTarget {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_, _ = conn.Write([]byte(greeting))
				conn.Close()
			}
		}()
		return SSHTarget{Address: "127.0.0.1", SSHPort: listener.Addr().(*net.TCPAddr).Port, SSHUser: "u"}
	}
	banner, err := systemProber{}.Reach(context.Background(), serve("SSH-2.0-OpenSSH_9.6\r\n"))
	if err != nil || banner != "SSH-2.0-OpenSSH_9.6" {
		t.Fatalf("SSH 服务应探测成功: %q %v", banner, err)
	}
	if _, err := (systemProber{}).Reach(context.Background(), serve("HTTP/1.1 400 Bad Request\r\n")); err == nil || !strings.Contains(err.Error(), "不是 SSH") {
		t.Fatalf("非 SSH 服务应提示端口上不是 SSH: %v", err)
	}
	closed := serve("")
	listener, _ := net.Listen("tcp4", "127.0.0.1:0")
	closed.SSHPort = listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	if _, err := (systemProber{}).Reach(context.Background(), closed); err == nil || !strings.Contains(err.Error(), "没有 SSH 服务") {
		t.Fatalf("端口没开应提示没有 SSH 服务: %v", err)
	}
}

func TestFingerprintFailureOffersAResetThatStartsTheConfirmationOver(t *testing.T) {
	h := start(t, time.Minute)
	t.Setenv("HOME", t.TempDir())
	h.prober.hostKey = "SHA256:abcdef"
	target := SSHTarget{Address: "192.168.0.103", SSHUser: "builder"}
	status, bad := h.post("ssh/trust", map[string]any{"target": target, "fingerprint": "SHA256:other"})
	if status != http.StatusUnprocessableEntity || bad.OK || !bad.Resettable || !strings.Contains(bad.Message, "不一致") {
		t.Fatalf("指纹对不上应 422 并带上可重置标记: %d %+v", status, bad)
	}
	// 其他失败（例如字段不对）不应给重置按钮。
	if status, other := h.post("ssh/trust", map[string]any{"target": SSHTarget{Address: "192.168.0.103"}, "fingerprint": "x"}); status != http.StatusUnprocessableEntity || other.Resettable {
		t.Fatalf("参数错误不该可重置: %d %+v", status, other)
	}
	if status, ok := h.post("ssh/trust", map[string]any{"target": target, "fingerprint": "SHA256:abcdef"}); status != http.StatusOK || ok.Login == nil || ok.Login.HostKey != "" {
		t.Fatalf("确认正确指纹后应进入下一步: %d %+v", status, ok)
	}
	status, reset := h.post("ssh/reset-host", target)
	if status != http.StatusOK || reset.Login == nil || reset.Login.HostKey != "SHA256:abcdef" {
		t.Fatalf("重置后应重新要求核对指纹: %d %+v", status, reset)
	}
	if got := h.prober.resets; len(got) != 1 || got[0] != "192.168.0.103" {
		t.Fatalf("应清除这台主机的指纹记录: %v", got)
	}
	h.prober.resetErr = errors.New("没能清除")
	if status, failed := h.post("ssh/reset-host", target); status != http.StatusUnprocessableEntity || failed.OK {
		t.Fatalf("清除失败应 422: %d %+v", status, failed)
	}
}
