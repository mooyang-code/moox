package configinit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

type editorHarness struct {
	t       *testing.T
	backups []string // 每次同步时的明文配置
	syncErr error
	base    string
	host    string
	path    string
	keys    *configcrypt.MemoryStore
	done    chan editorOutcome
}

type editorOutcome struct {
	result EditorResult
	err    error
}

// sampleText 是一份合法的明文配置（与 sampleRequest 同构）。
func sampleText(t *testing.T) string {
	t.Helper()
	_, text := renderWith(t, sampleRequest(), nil)
	return text
}

func startEditor(t *testing.T, content []byte) *editorHarness {
	t.Helper()
	h := &editorHarness{t: t, path: filepath.Join(t.TempDir(), "moox.toml"), keys: &configcrypt.MemoryStore{}, done: make(chan editorOutcome, 1)}
	if content == nil {
		content = []byte(sampleText(t))
	}
	if err := os.WriteFile(h.path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	urls := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		result, err := RunEditor(ctx, EditorOptions{File: h.path, Timeout: time.Minute, KeyStore: h.keys, Open: func(u string) error { urls <- u; return nil },
			Backup: func(_ context.Context, plain []byte, _ setupconfig.Manifest) (BackupResult, error) {
				h.backups = append(h.backups, string(plain))
				return BackupResult{}, h.syncErr
			}})
		h.done <- editorOutcome{result, err}
	}()
	select {
	case h.base = <-urls:
	case o := <-h.done:
		t.Fatalf("RunEditor 提前结束: %v", o.err)
	case <-time.After(5 * time.Second):
		t.Fatal("没有打开配置页")
	}
	parsed, _ := url.Parse(h.base)
	h.host = parsed.Host
	return h
}

func (h *editorHarness) call(path string, body any, mutate func(*http.Request)) (int, response, string) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, h.base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+h.host)
	if mutate != nil {
		mutate(req)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	var reply response
	_ = json.Unmarshal(text, &reply)
	return resp.StatusCode, reply, string(text)
}

func (h *editorHarness) plain() string {
	h.t.Helper()
	raw, err := os.ReadFile(h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	if !configcrypt.IsEncrypted(raw) {
		return string(raw)
	}
	plain, err := configcrypt.Decrypt(raw, h.keys)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(plain)
}

func TestEditorPageAndOverviewNeverCarrySecrets(t *testing.T) {
	h := startEditor(t, nil)
	resp, err := http.Get(h.base)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("配置页应带严格的安全响应头")
	}
	status, reply, text := h.call("config/overview", struct{}{}, nil)
	if status != http.StatusOK || reply.Overview == nil {
		t.Fatalf("概览: %d %s", status, text)
	}
	for _, secret := range []string{"Adm1n-P@ss", "key-example", "pw-storage", "pw-control", "AKIDexample", "webhook/send"} {
		if strings.Contains(text, secret) || strings.Contains(string(page), secret) {
			t.Fatalf("页面或概览里不应出现秘密 %q", secret)
		}
	}
	view := reply.Overview
	if view.Encrypted || view.KeyStore != "测试内存" || len(view.Servers) != 3 || view.Admin != "admin" || !view.HasWebhook || view.Region != "广州" {
		t.Fatalf("概览 = %+v", view)
	}
	uses := map[string]string{}
	for _, server := range view.Servers {
		uses[server.ID] = strings.Join(server.Uses, ",")
		if !server.HasPassword || server.Region == "" {
			t.Fatalf("主机概览 = %+v", server)
		}
	}
	if uses["control"] != "管理台" || uses["storage"] != "数据存储" || uses["compute-1"] != "海外接入与交易" {
		t.Fatalf("用途 = %v", uses)
	}
}

func TestEditorRevealsOneSecretAtATime(t *testing.T) {
	h := startEditor(t, nil)
	for field, want := range map[string]string{
		"admin.password": "Adm1n-P@ss", "tencent.secret_key": "key-example", "tencent.secret_id": "AKIDexample",
		"hosts.storage.password": "pw-storage", "notification.webhook_url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc",
	} {
		status, reply, _ := h.call("config/reveal", map[string]string{"field": field}, nil)
		if status != http.StatusOK || reply.Value != want {
			t.Fatalf("%s: %d %q", field, status, reply.Value)
		}
	}
	for _, field := range []string{"hosts.missing.password", "eventbus.port", ""} {
		if status, _, _ := h.call("config/reveal", map[string]string{"field": field}, nil); status != http.StatusNotFound {
			t.Fatalf("%q 应返回 404，实际 %d", field, status)
		}
	}
}

func TestEditorEncryptsAPlaintextConfigInPlace(t *testing.T) {
	original := sampleText(t)
	h := startEditor(t, []byte(original))
	status, reply, text := h.call("config/encrypt", struct{}{}, nil)
	if status != http.StatusOK || reply.Overview == nil || !reply.Overview.Encrypted {
		t.Fatalf("加密: %d %s", status, text)
	}
	raw, _ := os.ReadFile(h.path)
	if !configcrypt.IsEncrypted(raw) || strings.Contains(string(raw), "Adm1n-P@ss") {
		t.Fatal("文件应加密保存")
	}
	if h.plain() != original {
		t.Fatal("加密不应改变配置内容")
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(h.path); info.Mode().Perm() != 0o600 {
			t.Fatalf("权限 = %v，应为 0600", info.Mode().Perm())
		}
	}
}

func TestEditorSavesValidChangesAndRejectsInvalidOnes(t *testing.T) {
	h := startEditor(t, nil)
	_, reply, _ := h.call("config/text", struct{}{}, nil)
	if !strings.Contains(reply.Text, `password = "Adm1n-P@ss"`) {
		t.Fatal("修改页应拿到完整的配置文本")
	}
	before := h.plain()
	if status, reply, _ := h.call("config/save", map[string]string{"text": "unknown_field = 1\n" + reply.Text}, nil); status != http.StatusUnprocessableEntity || !strings.Contains(reply.Message, "没有保存") {
		t.Fatalf("校验不过时应拒绝: %d %+v", status, reply)
	}
	if h.plain() != before {
		t.Fatal("校验不过时文件不能变")
	}
	broken := strings.Replace(reply.Text, `username = "admin"`, `username = "Adm1n-P@ss`, 1)
	if status, syntax, _ := h.call("config/save", map[string]string{"text": broken}, nil); status != http.StatusUnprocessableEntity || !strings.Contains(syntax.Message, "第 2 行") || strings.Contains(syntax.Message, "Adm1n") {
		t.Fatalf("语法错误应给出行号且不回显内容: %d %+v", status, syntax)
	}
	changed := strings.Replace(reply.Text, `username = "admin"`, `username = "owner"`, 1)
	status, saved, text := h.call("config/save", map[string]string{"text": changed}, nil)
	if status != http.StatusOK || saved.Overview == nil || saved.Overview.Admin != "owner" || !saved.Overview.Encrypted {
		t.Fatalf("保存: %d %s", status, text)
	}
	if h.plain() != changed {
		t.Fatal("保存后的内容应与提交的一致")
	}
	if len(h.backups) != 1 || h.backups[0] != changed || !strings.Contains(saved.Sync, "已同步") {
		t.Fatalf("保存后应同步到管理后台: %d 次, %q", len(h.backups), saved.Sync)
	}
	h.syncErr = errors.New("连不上管理台")
	again := strings.Replace(changed, `username = "owner"`, `username = "owner2"`, 1)
	status, saved, _ = h.call("config/save", map[string]string{"text": again}, nil)
	if status != http.StatusOK || !strings.Contains(saved.Sync, "本机已保存") || h.plain() != again {
		t.Fatalf("同步失败不影响本机保存: %d %q", status, saved.Sync)
	}
	if _, err := setupconfig.Parse([]byte(h.plain())); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := h.call("close", struct{}{}, nil); status != http.StatusOK {
		t.Fatal("关闭失败")
	}
	select {
	case outcome := <-h.done:
		if outcome.err != nil || !outcome.result.Saved || !outcome.result.Encrypted {
			t.Fatalf("结果 = %+v %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("关闭后命令应结束")
	}
}

// 页面打开后文件在别处被改过时，保存要拒绝，不能覆盖别人的改动。
func TestEditorRefusesToOverwriteOutsideChanges(t *testing.T) {
	h := startEditor(t, nil)
	_, reply, _ := h.call("config/text", struct{}{}, nil)
	outside := strings.Replace(reply.Text, `username = "admin"`, `username = "outside"`, 1)
	if err := os.WriteFile(h.path, []byte(outside), 0o600); err != nil {
		t.Fatal(err)
	}
	status, saved, _ := h.call("config/save", map[string]string{"text": reply.Text}, nil)
	if status != http.StatusConflict || !strings.Contains(saved.Message, "别处被改动") {
		t.Fatalf("应拒绝覆盖: %d %+v", status, saved)
	}
	if h.plain() != outside {
		t.Fatal("别处的改动不能被覆盖")
	}
}

func TestEditorEndpointsRequireSameOriginJSON(t *testing.T) {
	h := startEditor(t, nil)
	for _, path := range []string{"config/overview", "config/text", "config/reveal", "config/save", "config/encrypt", "close"} {
		if status, _, _ := h.call(path, struct{}{}, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); status != http.StatusForbidden {
			t.Fatalf("%s：跨站请求状态 %d，应为 403", path, status)
		}
	}
}

func TestRunEditorRefusesUnreadableOrInsecureFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := RunEditor(context.Background(), EditorOptions{File: filepath.Join(dir, "moox.toml"), KeyStore: &configcrypt.MemoryStore{}}); err == nil {
		t.Fatal("文件不存在时应报错")
	}
	if runtime.GOOS == "windows" {
		return
	}
	path := filepath.Join(dir, "moox.toml")
	if err := os.WriteFile(path, []byte(sampleText(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunEditor(context.Background(), EditorOptions{File: path, KeyStore: &configcrypt.MemoryStore{}}); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("别人可读的配置应拒绝，实际 %v", err)
	}
}

func TestEncryptFileIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moox.toml")
	text := sampleText(t)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	keys := &configcrypt.MemoryStore{}
	already, err := EncryptFile(path, keys)
	if err != nil || already {
		t.Fatalf("第一次应加密: %v %v", already, err)
	}
	raw, _ := os.ReadFile(path)
	if plain, err := configcrypt.Decrypt(raw, keys); err != nil || string(plain) != text {
		t.Fatal("加密后应能解开且内容不变")
	}
	already, err = EncryptFile(path, keys)
	if err != nil || !already {
		t.Fatalf("已加密的文件只核对: %v %v", already, err)
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, raw) {
		t.Fatal("已加密的文件不应被改动")
	}
	if _, err := EncryptFile(path, &configcrypt.MemoryStore{}); !errors.Is(err, configcrypt.ErrKeyMissing) {
		t.Fatalf("换一台电脑（没有密钥）时应提示，实际 %v", err)
	}
	broken := filepath.Join(t.TempDir(), "moox.toml")
	_ = os.WriteFile(broken, []byte("unknown = 1\n"), 0o600)
	if _, err := EncryptFile(broken, keys); err == nil {
		t.Fatal("校验不过的配置不应加密")
	}
}
