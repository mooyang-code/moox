package configinit

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/consoleclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// fakeConsole 按真实协议实现管理台的登录（下发会话 Cookie）、Cookie 与 Origin 校验和配置备份接口。
type fakeConsole struct {
	t             *testing.T
	server        *httptest.Server
	password      string
	mu            sync.Mutex
	envelope      string
	authenticated int
}

const fakeSessionID = "fake-session-id"

func newFakeConsole(t *testing.T, password string) *fakeConsole {
	f := &fakeConsole{t: t, password: password}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeConsole) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch r.URL.Path {
	case "/api/admin/auth/Login":
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Password != f.password || req.Username != "admin" || r.Header.Get("Origin") != "https://"+r.Host {
			write(map[string]any{"ret_info": map[string]any{"code": 3, "msg": "用户名或密码错误"}})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "__Host-moox_session", Value: fakeSessionID, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
		write(map[string]any{"ret_info": map[string]any{"code": 0}, "expires_at": "1700086400"})
	case "/api/admin/setup/PutConfigBackup", "/api/admin/setup/GetConfigBackup":
		cookie, err := r.Cookie("__Host-moox_session")
		if err != nil || cookie.Value != fakeSessionID || r.Header.Get("Origin") != "https://"+r.Host {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.authenticated++
		if strings.HasSuffix(r.URL.Path, "PutConfigBackup") {
			var req struct {
				Envelope string `json:"envelope"`
			}
			_ = json.Unmarshal(body, &req)
			f.envelope = req.Envelope
			write(map[string]any{"ret_info": map[string]any{"code": 0}, "updated_at": "1700000100"})
			return
		}
		if f.envelope == "" {
			write(map[string]any{"ret_info": map[string]any{"code": 5, "msg": "config_backup_missing"}})
			return
		}
		write(map[string]any{"ret_info": map[string]any{"code": 0}, "envelope": f.envelope, "updated_at": "1700000100"})
	default:
		http.NotFound(w, r)
	}
}

// useFakeConsole 让 CLI 连到假管理台（信任它的测试证书）。pin 为 true 时不预置信任，走指纹确认。
func useFakeConsole(t *testing.T, console *fakeConsole, trust bool) {
	original := newConsole
	t.Cleanup(func() { newConsole = original })
	newConsole = func(options consoleclient.Options) (*consoleclient.Client, error) {
		options.Address = console.server.Listener.Addr().String()
		if trust {
			options.HTTPClient = console.server.Client()
		}
		return consoleclient.New(options)
	}
}

func writeConfig(t *testing.T, keys configcrypt.KeyStore) string {
	t.Helper()
	return writePairFiles(t, t.TempDir(), sampleRendered(t), keys)
}

func TestBackupUploadsAnEnvelopeOnlyTheAdminPasswordOpens(t *testing.T) {
	console := newFakeConsole(t, "Adm1n-P@ss")
	useFakeConsole(t, console, true)
	keys := &configcrypt.MemoryStore{}
	result, err := BackupFile(context.Background(), writeConfig(t, keys), keys)
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdatedAt.Unix() != 1700000100 || console.authenticated != 1 {
		t.Fatalf("结果 = %+v，带会话的请求 %d 次", result, console.authenticated)
	}
	if strings.Contains(console.envelope, "Adm1n-P@ss") || strings.Contains(console.envelope, "[admin]") || strings.Contains(console.envelope, "[hosts.") {
		t.Fatal("服务端收到的备份不应含明文")
	}
	plain, err := configcrypt.OpenBackup(console.envelope, "Adm1n-P@ss")
	if err != nil {
		t.Fatalf("备份应能用管理员密码解开: %v", err)
	}
	var bundle backupBundle
	rendered := sampleRendered(t)
	if err := json.Unmarshal(plain, &bundle); err != nil || bundle.Config != string(rendered.Config) || bundle.Secrets != string(rendered.Secrets) {
		t.Fatalf("备份里应原样带上两份文件: %v", err)
	}
	if _, err := BackupFile(context.Background(), writeConfig(t, keys), &configcrypt.MemoryStore{}); err == nil {
		t.Fatal("这台电脑没有密钥时不能备份")
	}
}

func TestBackupReportsAWrongAdminPassword(t *testing.T) {
	console := newFakeConsole(t, "a-different-password")
	useFakeConsole(t, console, true)
	keys := &configcrypt.MemoryStore{}
	if _, err := BackupFile(context.Background(), writeConfig(t, keys), keys); err == nil || !strings.Contains(err.Error(), "用户名或密码错误") {
		t.Fatalf("管理员密码不对时应说明，实际 %v", err)
	}
}

type restoreHarness struct {
	base   string
	host   string
	output string
	keys   *configcrypt.MemoryStore
	done   chan error
}

func startRestore(t *testing.T) *restoreHarness {
	t.Helper()
	h := &restoreHarness{output: filepath.Join(t.TempDir(), "moox.toml"), keys: &configcrypt.MemoryStore{}, done: make(chan error, 1)}
	urls := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_, err := RunRestore(ctx, RestoreOptions{Output: h.output, Timeout: time.Minute, KeyStore: h.keys, Open: func(u string) error { urls <- u; return nil }})
		h.done <- err
	}()
	select {
	case h.base = <-urls:
	case err := <-h.done:
		t.Fatalf("RunRestore 提前结束: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("没有打开恢复页")
	}
	h.host = strings.TrimSuffix(strings.SplitN(strings.TrimPrefix(h.base, "http://"), "/", 2)[0], "/")
	return h
}

func (h *restoreHarness) restore(t *testing.T, body map[string]string) (int, response) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, h.base+"restore", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+h.host)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply response
	_ = json.NewDecoder(resp.Body).Decode(&reply)
	return resp.StatusCode, reply
}

func TestRestoreConfirmsTheCertificateThenWritesAnEncryptedConfig(t *testing.T) {
	console := newFakeConsole(t, "Adm1n-P@ss")
	rendered := sampleRendered(t)
	console.envelope = sealedBundle(t, rendered, "Adm1n-P@ss")
	useFakeConsole(t, console, false) // 新电脑不信任管理台的自签证书
	h := startRestore(t)
	form := map[string]string{"address": "198.51.100.10", "username": "admin", "password": "Adm1n-P@ss"}

	status, reply := h.restore(t, form)
	if status != http.StatusOK || reply.OK || reply.Fingerprint == "" {
		t.Fatalf("证书不受信任时应先请用户核对指纹: %d %+v", status, reply)
	}
	if want := consoleclient.Fingerprint(console.server.Certificate().Raw); reply.Fingerprint != want {
		t.Fatalf("指纹 = %s，应为 %s", reply.Fingerprint, want)
	}
	form["fingerprint"] = "00:11"
	if status, reply := h.restore(t, form); status != http.StatusUnprocessableEntity || reply.OK {
		t.Fatalf("指纹对不上时应拒绝: %d %+v", status, reply)
	}
	form["fingerprint"] = reply.Fingerprint
	form["password"] = "wrong"
	if status, reply := h.restore(t, form); status != http.StatusUnprocessableEntity || !strings.Contains(reply.Message, "用户名或密码错误") {
		t.Fatalf("密码不对时应说明: %d %+v", status, reply)
	}
	form["password"] = "Adm1n-P@ss"
	if status, reply := h.restore(t, form); status != http.StatusOK || !reply.OK {
		t.Fatalf("恢复: %d %+v", status, reply)
	}
	if err := <-h.done; err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(h.output)
	if err != nil || string(config) != string(rendered.Config) {
		t.Fatal("恢复后的 moox.toml 应是备份里的明文")
	}
	raw, err := os.ReadFile(topology.SecretsPathFor(h.output))
	if err != nil || !configcrypt.IsEncrypted(raw) {
		t.Fatal("恢复后的秘密文件应加密保存")
	}
	plain, err := configcrypt.Decrypt(raw, h.keys)
	if err != nil || string(plain) != string(rendered.Secrets) {
		t.Fatal("恢复后的秘密文件应与备份一致，并能用这台电脑的密钥解开")
	}
}

func TestRestoreExplainsAMissingBackup(t *testing.T) {
	console := newFakeConsole(t, "Adm1n-P@ss")
	useFakeConsole(t, console, true)
	h := startRestore(t)
	status, reply := h.restore(t, map[string]string{"address": "198.51.100.10", "username": "admin", "password": "Adm1n-P@ss"})
	if status != http.StatusUnprocessableEntity || !strings.Contains(reply.Message, "还没有配置备份") {
		t.Fatalf("没有备份时应说明: %d %+v", status, reply)
	}
	for _, path := range []string{h.output, topology.SecretsPathFor(h.output)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("没有恢复成功时不能写文件")
		}
	}
}

// 本机缓存的管理台 CA 过期或缺失时，备份先从管理台服务器取回根证书刷新缓存，再按证书链校验。
func TestBackupRefreshesAStaleConsoleCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	console := newFakeConsole(t, "Adm1n-P@ss")
	useFakeConsole(t, console, false)
	original := refreshConsoleCA
	t.Cleanup(func() { refreshConsoleCA = original })
	refreshed := 0
	refreshConsoleCA = func(_ context.Context, cfg *topology.Config) error {
		refreshed++
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: console.server.Certificate().Raw})
		return tlstrust.FileCAStore{}.Save(cfg.ControlHost().Address, pemBytes)
	}
	keys := &configcrypt.MemoryStore{}
	if _, err := BackupFile(context.Background(), writeConfig(t, keys), keys); err != nil {
		t.Fatal(err)
	}
	if refreshed != 1 || console.envelope == "" {
		t.Fatalf("应刷新一次根证书后完成备份: refreshed=%d", refreshed)
	}
	// 缓存已经是对的，下次不再刷新。
	if _, err := BackupFile(context.Background(), writeConfig(t, keys), keys); err != nil || refreshed != 1 {
		t.Fatalf("第二次备份不应再刷新: %v refreshed=%d", err, refreshed)
	}
}
