package configinit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/consoleclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/packages/requestauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

// fakeConsole 按真实协议实现管理台的登录、签名校验和配置备份接口。
type fakeConsole struct {
	t        *testing.T
	server   *httptest.Server
	password string
	mu       sync.Mutex
	envelope string
	signed   int
}

const fakeSigningKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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
	case "/api/admin/auth/GetLoginSalt":
		// protojson 会把 int64 编成字符串，客户端要能接住。
		write(map[string]any{"ret_info": map[string]any{"code": 0}, "salt": "salt-1", "timestamp": "1700000000"})
	case "/api/admin/auth/Login":
		var req struct {
			Username     string `json:"username"`
			PasswordHash string `json:"password_hash"`
			Salt         string `json:"salt"`
			Timestamp    int64  `json:"timestamp"`
		}
		_ = json.Unmarshal(body, &req)
		plain, err := mooxsecurity.Decrypt(req.PasswordHash, req.Salt+strconv.FormatInt(req.Timestamp, 10))
		if err != nil || plain != f.password || req.Username != "admin" {
			write(map[string]any{"ret_info": map[string]any{"code": 3, "msg": "用户名或密码错误"}})
			return
		}
		write(map[string]any{"ret_info": map[string]any{"code": 0}, "access_token": "token-1", "request_signing_key": fakeSigningKey})
	case "/api/admin/setup/PutConfigBackup", "/api/admin/setup/GetConfigBackup":
		timestamp, _ := strconv.ParseInt(r.Header.Get("X-Moox-Timestamp"), 10, 64)
		material := requestauth.Material{Method: r.Method, Path: r.URL.EscapedPath(), Body: body, Timestamp: timestamp, Nonce: r.Header.Get("X-Moox-Nonce")}
		if r.Header.Get("Authorization") != "token-1" || requestauth.Verify(fakeSigningKey, material, r.Header.Get("X-Moox-Signature")) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.signed++
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
	path := filepath.Join(t.TempDir(), "moox.toml")
	sealed, err := configcrypt.Encrypt([]byte(sampleText(t)), keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackupUploadsAnEnvelopeOnlyTheAdminPasswordOpens(t *testing.T) {
	console := newFakeConsole(t, "Adm1n-P@ss")
	useFakeConsole(t, console, true)
	keys := &configcrypt.MemoryStore{}
	result, err := BackupFile(context.Background(), writeConfig(t, keys), keys)
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdatedAt.Unix() != 1700000100 || console.signed != 1 {
		t.Fatalf("结果 = %+v，签名请求 %d 次", result, console.signed)
	}
	if strings.Contains(console.envelope, "Adm1n-P@ss") || strings.Contains(console.envelope, "[admin]") {
		t.Fatal("服务端收到的备份不应含明文")
	}
	plain, err := configcrypt.OpenBackup(console.envelope, "Adm1n-P@ss")
	if err != nil || string(plain) != sampleText(t) {
		t.Fatalf("备份应能用管理员密码解开: %v", err)
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
	envelope, err := configcrypt.SealBackup([]byte(sampleText(t)), "Adm1n-P@ss")
	if err != nil {
		t.Fatal(err)
	}
	console.envelope = envelope
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
	raw, err := os.ReadFile(h.output)
	if err != nil || !configcrypt.IsEncrypted(raw) {
		t.Fatal("恢复后的配置应加密保存")
	}
	plain, err := configcrypt.Decrypt(raw, h.keys)
	if err != nil || string(plain) != sampleText(t) {
		t.Fatal("恢复后的配置应与备份一致，并能用这台电脑的密钥解开")
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
	if _, err := os.Stat(h.output); !os.IsNotExist(err) {
		t.Fatal("没有恢复成功时不能写文件")
	}
}
