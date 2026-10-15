package configinit

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

//go:embed editor.html
var editorSource string

var editorTemplate = template.Must(template.New("editor").Parse(editorSource))

// EditorOptions 控制一次配置查看与修改。
type EditorOptions struct {
	// File 是 moox.toml 的路径。
	File string
	// Timeout 为零时用 DefaultTimeout。
	Timeout time.Duration
	// Open 为空时用系统默认浏览器；测试里替换。
	Open func(url string) error
	// Notice 接收给用户看的提示（不含任何配置值）；为空则丢弃。
	Notice io.Writer
	// KeyStore 为空时用本机的系统钥匙串（没有钥匙串时用仅本人可读的密钥文件）。
	KeyStore configcrypt.KeyStore
	// Backup 在每次保存后把配置备份到管理后台；为空时用 moox.toml 里的管理员账号登录管理台上传。
	// 测试里替换，避免连接真实的管理台。
	Backup func(context.Context, []byte, setupconfig.Manifest) (BackupResult, error)
}

// EditorResult 只说明发生了什么，不含任何配置值。
type EditorResult struct {
	Path      string `json:"path"`
	Saved     bool   `json:"saved"`
	Encrypted bool   `json:"encrypted"`
}

// Overview 是配置页的概览：秘密只给出「有没有」，要看具体值时再单独取。
type Overview struct {
	Encrypted    bool         `json:"encrypted"`
	KeyStore     string       `json:"key_store"`
	Servers      []ServerView `json:"servers"`
	FactorEngine string       `json:"factor_engine"`
	SecretID     string       `json:"secret_id"`
	Region       string       `json:"region"`
	Admin        string       `json:"admin"`
	Channel      string       `json:"channel"`
	HasWebhook   bool         `json:"has_webhook"`
}

// ServerView 是一台主机的概览。
type ServerView struct {
	ID          string   `json:"id"`
	Uses        []string `json:"uses"`
	Address     string   `json:"address"`
	Region      string   `json:"region"`
	SSHUser     string   `json:"ssh_user"`
	SSHPort     int      `json:"ssh_port"`
	HasPassword bool     `json:"has_password"`
}

// RunEditor 在本机起配置页：查看当前配置（秘密默认隐藏）、修改并加密保存。用户点「完成」或超时后结束。
func RunEditor(ctx context.Context, options EditorOptions) (EditorResult, error) {
	path, err := filepath.Abs(options.File)
	if err != nil || options.File == "" {
		return EditorResult{}, errors.New("需要指定 moox.toml 的路径")
	}
	keys := options.KeyStore
	if keys == nil {
		keys = configcrypt.Default()
	}
	backup := options.Backup
	if backup == nil {
		backup = backupPlain
	}
	editor := &editorSession{path: path, keys: keys, backup: backup, result: make(chan EditorResult, 1)}
	if _, err := editor.load(); err != nil {
		return EditorResult{}, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	notice := options.Notice
	if notice == nil {
		notice = io.Discard
	}
	open := options.Open
	if open == nil {
		open = openBrowser
	}
	listener, err := listenLocal()
	if err != nil {
		return EditorResult{}, err
	}
	page, err := newLocalPage(listener.Addr().String(), editorTemplate, map[string]any{"deadline": time.Now().Add(timeout).UnixMilli()})
	if err != nil {
		_ = listener.Close()
		return EditorResult{}, err
	}
	editor.localPage = page
	page.routes = map[string]http.HandlerFunc{
		"config/overview": editor.overview,
		"config/text":     editor.text,
		"config/reveal":   editor.reveal,
		"config/save":     editor.save,
		"config/encrypt":  editor.encrypt,
		"close":           editor.close,
	}
	return serveLocal(ctx, listener, page, serveOptions{Timeout: timeout, Open: open, Notice: notice, Name: "配置页"}, editor.result)
}

type editorSession struct {
	*localPage
	path   string
	keys   configcrypt.KeyStore
	backup func(context.Context, []byte, setupconfig.Manifest) (BackupResult, error)
	result chan EditorResult

	// digest 是最近一次读到或写入的文件摘要；保存前核对，避免覆盖别处的改动。
	digest [sha256.Size]byte
	saved  bool
}

// loaded 是一次读取的结果。
type loaded struct {
	plain     []byte
	manifest  setupconfig.Manifest
	encrypted bool
}

// load 读取并解开配置文件，记下摘要。调用方持有 mu（RunEditor 启动前除外）。
func (e *editorSession) load() (loaded, error) {
	raw, err := readConfigFile(e.path)
	if err != nil {
		return loaded{}, err
	}
	out := loaded{plain: raw}
	if configcrypt.IsEncrypted(raw) {
		out.encrypted = true
		if out.plain, err = configcrypt.Decrypt(raw, e.keys); err != nil {
			return loaded{}, err
		}
	}
	if out.manifest, err = setupconfig.Parse(out.plain); err != nil {
		return loaded{}, fmt.Errorf("当前的 moox.toml 校验没有通过：%w", err)
	}
	e.digest = sha256.Sum256(raw)
	return out, nil
}

// readConfigFile 只读普通文件，且只有本人可读（与 setup 命令的要求一致）。
func readConfigFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("读不到 %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s 必须是普通文件", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s 的权限必须是 0600（只有本人可读写）", path)
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("%s 超过 1 MiB", path)
	}
	return os.ReadFile(path)
}

func (e *editorSession) current(w http.ResponseWriter, r *http.Request) (loaded, bool) {
	var empty struct{}
	if !e.decode(w, r, &empty) {
		return loaded{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	state, err := e.load()
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return loaded{}, false
	}
	return state, true
}

func (e *editorSession) overview(w http.ResponseWriter, r *http.Request) {
	state, ok := e.current(w, r)
	if !ok {
		return
	}
	view := describe(state.manifest, state.encrypted, e.keys.Where())
	reply(w, http.StatusOK, response{OK: true, Overview: &view})
}

func (e *editorSession) text(w http.ResponseWriter, r *http.Request) {
	state, ok := e.current(w, r)
	if !ok {
		return
	}
	reply(w, http.StatusOK, response{OK: true, Text: string(state.plain)})
}

// reveal 按需取出一个秘密的值，供页面上「显示」使用。
func (e *editorSession) reveal(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Field string `json:"field"`
	}
	if !e.decode(w, r, &request) {
		return
	}
	e.mu.Lock()
	state, err := e.load()
	e.mu.Unlock()
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	m := state.manifest
	var value string
	switch field := request.Field; {
	case field == "admin.password":
		value = m.Admin.Password
	case field == "tencent.secret_id":
		value = m.TencentCloud.SecretID
	case field == "tencent.secret_key":
		value = m.TencentCloud.SecretKey
	case field == "notification.webhook_url":
		value = m.Notification.WebhookURL
	case strings.HasPrefix(field, "hosts.") && strings.HasSuffix(field, ".password"):
		host, ok := m.HostCatalog[strings.TrimSuffix(strings.TrimPrefix(field, "hosts."), ".password")]
		if !ok {
			reply(w, http.StatusNotFound, response{Message: "没有这台主机"})
			return
		}
		value = host.SSH.Password
	default:
		reply(w, http.StatusNotFound, response{Message: "没有这一项"})
		return
	}
	reply(w, http.StatusOK, response{OK: true, Value: value})
}

// save 校验修改后的配置，加密后原子替换文件；文件在别处被改过时拒绝覆盖。
func (e *editorSession) save(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Text string `json:"text"`
	}
	if !e.decode(w, r, &request) {
		return
	}
	// 先单独检查 TOML 语法，给出行号（不回显该行内容，免得把秘密带进提示）。
	var syntax map[string]any
	if _, err := toml.Decode(request.Text, &syntax); err != nil {
		message := "配置的格式不对，没有保存"
		var parseErr toml.ParseError
		if errors.As(err, &parseErr) && parseErr.Position.Line > 0 {
			message = fmt.Sprintf("第 %d 行附近的格式不对，没有保存", parseErr.Position.Line)
		}
		reply(w, http.StatusUnprocessableEntity, response{Message: message})
		return
	}
	if _, err := setupconfig.Parse([]byte(request.Text)); err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: "配置没有通过校验，没有保存：" + err.Error()})
		return
	}
	e.write(w, r, []byte(request.Text))
}

// encrypt 把明文保存的配置原样加密保存。
func (e *editorSession) encrypt(w http.ResponseWriter, r *http.Request) {
	state, ok := e.current(w, r)
	if !ok {
		return
	}
	e.write(w, r, state.plain)
}

func (e *editorSession) write(w http.ResponseWriter, r *http.Request, plain []byte) {
	state, status, err := e.saveLocked(plain)
	if err != nil {
		reply(w, status, response{Message: err.Error()})
		return
	}
	view := describe(state.manifest, state.encrypted, e.keys.Where())
	reply(w, http.StatusOK, response{OK: true, Overview: &view, Sync: e.sync(r.Context(), state)})
}

// saveLocked 在文件锁内核对、加密并替换文件，返回新读到的配置。
func (e *editorSession) saveLocked(plain []byte) (loaded, int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	raw, err := readConfigFile(e.path)
	if err != nil || sha256.Sum256(raw) != e.digest {
		return loaded{}, http.StatusConflict, errors.New("moox.toml 在别处被改动过，为避免覆盖没有保存；请刷新页面后重新修改")
	}
	sealed, err := configcrypt.Encrypt(plain, e.keys)
	if err != nil {
		return loaded{}, http.StatusInternalServerError, fmt.Errorf("加密失败：%w", err)
	}
	if err := replaceFile(e.path, sealed); err != nil {
		return loaded{}, http.StatusInternalServerError, err
	}
	e.saved = true
	state, err := e.load()
	if err != nil {
		return loaded{}, http.StatusInternalServerError, err
	}
	return state, http.StatusOK, nil
}

// sync 在本机保存成功后把配置备份到管理后台（不持锁，失败只提示，不影响本机保存）。
func (e *editorSession) sync(ctx context.Context, state loaded) string {
	if e.backup == nil {
		return ""
	}
	if _, err := e.backup(ctx, state.plain, state.manifest); err != nil {
		return "本机已保存；暂时没能同步到管理后台（" + err.Error() + "）。管理台部署好后，再保存一次或让 Agent 运行一次同步即可。"
	}
	return "已同步到你的管理后台（用管理员密码加密备份，换电脑时可以恢复）。"
}

func (e *editorSession) close(w http.ResponseWriter, r *http.Request) {
	var empty struct{}
	if !e.decode(w, r, &empty) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		reply(w, http.StatusGone, response{Message: "页面已关闭"})
		return
	}
	e.done = true
	raw, _ := readConfigFile(e.path)
	reply(w, http.StatusOK, response{OK: true})
	e.result <- EditorResult{Path: e.path, Saved: e.saved, Encrypted: configcrypt.IsEncrypted(raw)}
}

// replaceFile 先写同目录的临时文件（0600、落盘），再原子改名替换，中途失败不会留下半截配置。
func replaceFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".moox.toml.tmp-*")
	if err != nil {
		return fmt.Errorf("无法写入 %s：%w", filepath.Dir(path), err)
	}
	tmp := file.Name()
	cleanup := func(err error) error {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("保存 %s 失败：%w", path, err)
	}
	if err := file.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return cleanup(err)
	}
	if _, err := file.Write(data); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存 %s 失败：%w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存 %s 失败：%w", path, err)
	}
	return nil
}

// EncryptFile 把明文保存的 moox.toml 原样加密保存；已经加密的只核对能否解开，不改动。
func EncryptFile(path string, keys configcrypt.KeyStore) (alreadyEncrypted bool, err error) {
	if keys == nil {
		keys = configcrypt.Default()
	}
	raw, err := readConfigFile(path)
	if err != nil {
		return false, err
	}
	if configcrypt.IsEncrypted(raw) {
		_, err := configcrypt.Decrypt(raw, keys)
		return true, err
	}
	if _, err := setupconfig.Parse(raw); err != nil {
		return false, fmt.Errorf("当前的 moox.toml 校验没有通过，先修好再加密：%w", err)
	}
	sealed, err := configcrypt.Encrypt(raw, keys)
	if err != nil {
		return false, err
	}
	return false, replaceFile(path, sealed)
}

// describe 把配置整理成给人看的概览，不含任何秘密值。
func describe(m setupconfig.Manifest, encrypted bool, where string) Overview {
	view := Overview{
		Encrypted: encrypted, KeyStore: where, SecretID: maskID(m.TencentCloud.SecretID),
		Region: regionName(m.TencentCloud.Region), Admin: m.Admin.Username,
		Channel: map[string]string{"wecom": "企业微信群机器人", "feishu": "飞书群机器人"}[m.Notification.ChannelType], HasWebhook: m.Notification.WebhookURL != "",
	}
	switch {
	case m.FactorEngineIsLocal():
		view.FactorEngine = "这台电脑"
	case m.FactorEngineRef.Host != "":
		view.FactorEngine = m.FactorEngineHost().Address
	}
	ids := make([]string, 0, len(m.HostCatalog))
	for id := range m.HostCatalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		host := m.HostCatalog[id]
		server := ServerView{ID: id, Address: host.Address, SSHUser: host.SSH.Username, SSHPort: host.SSH.Port, HasPassword: host.SSH.Password != ""}
		if host.Region != "" {
			server.Region = regionName(host.Region)
		}
		placed := m.Placements[id]
		for _, use := range []struct {
			label      string
			components []string
		}{{"管理台", []string{"admin"}}, {"数据存储", []string{"storage-primary"}}, {"海外接入与交易", []string{"trade", "egress-proxy"}}} {
			for _, component := range use.components {
				if slices.Contains(placed, component) {
					server.Uses = append(server.Uses, use.label)
					break
				}
			}
		}
		if id == m.FactorEngineRef.Host {
			server.Uses = append(server.Uses, "因子计算")
		}
		if id == m.CompileHostRef.Host {
			server.Uses = append(server.Uses, "源码编译")
		}
		view.Servers = append(view.Servers, server)
	}
	// 按用途排序：管理台、数据存储、海外、因子计算机，其余在后。
	rank := func(server ServerView) int {
		for i, use := range []string{"管理台", "数据存储", "海外接入与交易", "因子计算", "源码编译"} {
			if slices.Contains(server.Uses, use) {
				return i
			}
		}
		return 9
	}
	sort.SliceStable(view.Servers, func(i, j int) bool { return rank(view.Servers[i]) < rank(view.Servers[j]) })
	return view
}

// maskID 只露出 SecretId 的头尾，便于用户认出是哪一把。
func maskID(id string) string {
	if len(id) <= 10 {
		return strings.Repeat("•", len(id))
	}
	return id[:6] + strings.Repeat("•", 6) + id[len(id)-4:]
}
