package configinit

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

//go:embed editor.html
var editorSource string

var editorTemplate = template.Must(template.New("editor").Parse(editorSource))

// EditorOptions 控制一次配置查看与修改。
type EditorOptions struct {
	// File 是 moox.toml 的路径；秘密文件 moox.secrets.toml 在同一目录。
	File string
	// Timeout 为零时用 DefaultTimeout。
	Timeout time.Duration
	// Open 为空时用系统默认浏览器；测试里替换。
	Open func(url string) error
	// Notice 接收给用户看的提示（不含任何配置值）；为空则丢弃。
	Notice io.Writer
	// KeyStore 为空时用本机的系统钥匙串（没有钥匙串时用仅本人可读的密钥文件）。
	KeyStore configcrypt.KeyStore
	// Backup 在每次保存后把两份配置备份到管理后台；为空时用秘密文件里的管理员账号登录管理台上传。
	// 测试里替换，避免连接真实的管理台。
	Backup func(context.Context, ConfigPair) (BackupResult, error)
}

// EditorResult 只说明发生了什么，不含任何配置值。Encrypted 说的是秘密文件。
type EditorResult struct {
	Path      string `json:"path"`
	Saved     bool   `json:"saved"`
	Encrypted bool   `json:"encrypted"`
}

// Overview 是配置页的概览：秘密只给出「有没有」，要看具体值时再单独取。Encrypted 说的是秘密文件。
type Overview struct {
	Encrypted  bool         `json:"encrypted"`
	KeyStore   string       `json:"key_store"`
	Servers    []ServerView `json:"servers"`
	SecretID   string       `json:"secret_id"`
	Region     string       `json:"region"`
	Admin      string       `json:"admin"`
	Channel    string       `json:"channel"`
	HasWebhook bool         `json:"has_webhook"`
	// HasNotifySecret 表示告警渠道配了密钥（飞书签名校验、钉钉加签或通用 Webhook 签名）。
	HasNotifySecret bool `json:"has_notify_secret"`
	// ExchangeKeys 是 [[exchange_keys]] 的概览，顺序与配置里一致；明文只在 reveal 时按序号给出。
	ExchangeKeys []ExchangeKeyView `json:"exchange_keys"`
}

// ExchangeKeyView 是一把交易所密钥的概览：只给出打码的 API Key，Secret Key 与 passphrase 只说有没有。
type ExchangeKeyView struct {
	Index         int    `json:"index"`
	Name          string `json:"name"`
	Exchange      string `json:"exchange"`
	Environment   string `json:"environment"`
	ReadOnly      bool   `json:"readonly"`
	SecretID      string `json:"secret_id"`
	APIKey        string `json:"api_key"`
	HasPassphrase bool   `json:"has_passphrase"`
}

// ServerView 是一台主机的概览。
type ServerView struct {
	ID      string   `json:"id"`
	Uses    []string `json:"uses"`
	Address string   `json:"address"`
	Region  string   `json:"region"`
	SSHUser string   `json:"ssh_user"`
	SSHPort int      `json:"ssh_port"`
}

// RunEditor 在本机起配置页：查看两份配置（秘密默认隐藏）、修改后保存（秘密文件重新加密）。用户点「完成」或超时后结束。
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
	editor := &editorSession{path: path, secretsPath: topology.SecretsPathFor(path), keys: keys, backup: backup, result: make(chan EditorResult, 1)}
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
		"config/form":     editor.form,
		"config/reveal":   editor.reveal,
		"config/update":   editor.update,
		"config/encrypt":  editor.encrypt,
		"close":           editor.close,
	}
	return serveLocal(ctx, listener, page, serveOptions{Timeout: timeout, Open: open, Notice: notice, Name: "配置页"}, editor.result)
}

type editorSession struct {
	*localPage
	path, secretsPath string
	keys              configcrypt.KeyStore
	backup            func(context.Context, ConfigPair) (BackupResult, error)
	result            chan EditorResult

	// digest、secretsDigest 是最近一次读到或写入的两份文件的摘要；保存前核对，避免覆盖别处的改动。
	digest, secretsDigest [sha256.Size]byte
	saved                 bool
}

// loaded 是一次读取的结果。
type loaded struct {
	ConfigPair
	// encrypted 表示秘密文件是加密保存的。
	encrypted bool
}

// load 读取两份文件（秘密文件加密的先解开），记下摘要。调用方持有 mu（RunEditor 启动前除外）。
func (e *editorSession) load() (loaded, error) {
	configRaw, err := readConfigFile(e.path, false)
	if err != nil {
		return loaded{}, err
	}
	secretsRaw, err := readConfigFile(e.secretsPath, true)
	if err != nil {
		return loaded{}, err
	}
	out := loaded{encrypted: configcrypt.IsEncrypted(secretsRaw)}
	secrets := secretsRaw
	if out.encrypted {
		if secrets, err = configcrypt.Decrypt(secretsRaw, e.keys); err != nil {
			return loaded{}, err
		}
	}
	if out.ConfigPair, err = parsePair(configRaw, secrets); err != nil {
		return loaded{}, err
	}
	e.digest, e.secretsDigest = sha256.Sum256(configRaw), sha256.Sum256(secretsRaw)
	return out, nil
}

// readConfigFile 与 CLI 读配置用同一套校验（topology.ReadFile）：只读普通文件、不跟随符号链接、不超过 1 MiB；
// private 为 true（秘密文件）时还要求 0600 且属于本人、读取时没被替换。
func readConfigFile(path string, private bool) ([]byte, error) {
	return topology.ReadFile(path, private)
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
	view := describe(state, e.keys.Where())
	reply(w, http.StatusOK, response{OK: true, Overview: &view})
}

// form 返回「修改配置」表单的当前值（含密码，只在本机页面里使用）。
func (e *editorSession) form(w http.ResponseWriter, r *http.Request) {
	state, ok := e.current(w, r)
	if !ok {
		return
	}
	form := formFrom(state.ConfigPair)
	reply(w, http.StatusOK, response{OK: true, Form: &form})
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
	m := state.Secrets
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
	case field == "notification.secret":
		value = m.Notification.Secret
	case strings.HasPrefix(field, "exchange_keys."):
		// 按序号取：exchange_keys.<序号>.api_key | secret_key | passphrase。
		parts := strings.Split(field, ".")
		index, err := strconv.Atoi(parts[1])
		if len(parts) != 3 || err != nil || index < 0 || index >= len(m.ExchangeKeys) {
			reply(w, http.StatusNotFound, response{Message: "没有这把交易所密钥"})
			return
		}
		key := m.ExchangeKeys[index]
		switch parts[2] {
		case "api_key":
			value = key.APIKey
		case "secret_key":
			value = key.SecretKey
		case "passphrase":
			value = key.Passphrase
		default:
			reply(w, http.StatusNotFound, response{Message: "没有这一项"})
			return
		}
	default:
		reply(w, http.StatusNotFound, response{Message: "没有这一项"})
		return
	}
	reply(w, http.StatusOK, response{OK: true, Value: value})
}

// update 把表单的修改逐项写回配置，完整校验后加密保存；文件在别处改动过时拒绝覆盖。
func (e *editorSession) update(w http.ResponseWriter, r *http.Request) {
	var form ConfigForm
	if !e.decode(w, r, &form) {
		return
	}
	e.mu.Lock()
	state, err := e.load()
	e.mu.Unlock()
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	config, secrets, err := applyForm(state.ConfigPair, form)
	if err == nil {
		_, err = parsePair(config, secrets)
	}
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: "修改没有通过校验，没有保存：" + err.Error()})
		return
	}
	// 秘密文件只在内容有变化时重写（一律加密）；明文保存的秘密文件不顺手加密，由「加密保存」单独做。
	e.write(w, r, state, config, secrets, !bytes.Equal(secrets, state.SecretsText))
}

// encrypt 把明文保存的秘密文件原样加密保存。
func (e *editorSession) encrypt(w http.ResponseWriter, r *http.Request) {
	state, ok := e.current(w, r)
	if !ok {
		return
	}
	e.write(w, r, state, state.ConfigText, state.SecretsText, true)
}

func (e *editorSession) write(w http.ResponseWriter, r *http.Request, before loaded, config, secrets []byte, sealSecrets bool) {
	state, status, err := e.saveLocked(before, config, secrets, sealSecrets)
	if err != nil {
		reply(w, status, response{Message: err.Error()})
		return
	}
	view := describe(state, e.keys.Where())
	reply(w, http.StatusOK, response{OK: true, Overview: &view, Sync: e.sync(r.Context(), state)})
}

// saveLocked 在文件锁内核对两份文件没被别处改过，再替换有变化的那份（秘密文件加密后写），返回新读到的配置。
func (e *editorSession) saveLocked(before loaded, config, secrets []byte, sealSecrets bool) (loaded, int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	configRaw, err := readConfigFile(e.path, false)
	secretsRaw, secretsErr := readConfigFile(e.secretsPath, true)
	if err != nil || secretsErr != nil || sha256.Sum256(configRaw) != e.digest || sha256.Sum256(secretsRaw) != e.secretsDigest {
		return loaded{}, http.StatusConflict, errors.New("配置文件在别处被改动过，为避免覆盖没有保存；请刷新页面后重新修改")
	}
	if sealSecrets {
		sealed, err := configcrypt.Encrypt(secrets, e.keys)
		if err != nil {
			return loaded{}, http.StatusInternalServerError, fmt.Errorf("加密失败：%w", err)
		}
		if err := replaceFile(e.secretsPath, sealed); err != nil {
			return loaded{}, http.StatusInternalServerError, err
		}
	}
	if !bytes.Equal(config, before.ConfigText) {
		if err := replaceFile(e.path, config); err != nil {
			return loaded{}, http.StatusInternalServerError, err
		}
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
	if _, err := e.backup(ctx, state.ConfigPair); err != nil {
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
	raw, _ := readConfigFile(e.secretsPath, true)
	reply(w, http.StatusOK, response{OK: true})
	e.result <- EditorResult{Path: e.path, Saved: e.saved, Encrypted: configcrypt.IsEncrypted(raw)}
}

// replaceFile 先写同目录的临时文件（0600、落盘），再原子改名替换，中途失败不会留下半截配置。
func replaceFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
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

// EncryptFile 把明文保存的 moox.secrets.toml 原样加密保存；path 是 moox.toml 或秘密文件本身的路径。
// 已经加密的只核对能否解开，不改动。
func EncryptFile(path string, keys configcrypt.KeyStore) (alreadyEncrypted bool, err error) {
	if keys == nil {
		keys = configcrypt.Default()
	}
	if filepath.Base(path) != topology.SecretsFileName {
		path = topology.SecretsPathFor(path)
	}
	raw, err := readConfigFile(path, true)
	if err != nil {
		return false, err
	}
	if configcrypt.IsEncrypted(raw) {
		_, err := configcrypt.Decrypt(raw, keys)
		return true, err
	}
	if _, err := topology.ParseSecrets(raw); err != nil {
		return false, fmt.Errorf("当前的 moox.secrets.toml 校验没有通过，先修好再加密：%w", err)
	}
	sealed, err := configcrypt.Encrypt(raw, keys)
	if err != nil {
		return false, err
	}
	return false, replaceFile(path, sealed)
}

// channelNames 是告警渠道类型在页面上的称呼，与录入页、编辑页的下拉选项一致。
var channelNames = map[string]string{"wecom": "企业微信群机器人", "feishu": "飞书群机器人", "dingtalk": "钉钉群机器人", "webhook": "通用 Webhook"}

// describe 把两份配置整理成给人看的概览，不含任何秘密值。
func describe(state loaded, where string) Overview {
	m, sec := state.Config, state.Secrets
	view := Overview{
		Encrypted: state.encrypted, KeyStore: where, SecretID: maskID(sec.TencentCloud.SecretID),
		Region: regionName(m.CloudRegion()), Admin: sec.Admin.Username,
		Channel: channelNames[sec.Notification.ChannelType], HasWebhook: sec.Notification.WebhookURL != "", HasNotifySecret: sec.Notification.Secret != "",
	}
	view.ExchangeKeys = make([]ExchangeKeyView, 0, len(sec.ExchangeKeys))
	for i, key := range sec.ExchangeKeys {
		view.ExchangeKeys = append(view.ExchangeKeys, ExchangeKeyView{
			Index: i, Name: key.Name, Exchange: key.Exchange, Environment: key.Environment, ReadOnly: key.ReadOnly,
			SecretID: key.ID(), APIKey: maskID(key.APIKey), HasPassphrase: key.Passphrase != "",
		})
	}
	view.Servers = describeServers(m)
	return view
}

// describeServers 按用途排序列出主机：管理台、数据存储、海外、因子机，其余在后。
func describeServers(m topology.Config) []ServerView {
	ids := make([]string, 0, len(m.HostCatalog))
	for id := range m.HostCatalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var servers []ServerView
	for _, id := range ids {
		host := m.HostCatalog[id]
		server := ServerView{ID: id, Address: host.Address, SSHUser: host.SSH.Username, SSHPort: host.SSH.Port}
		if host.Region != "" {
			server.Region = regionName(host.Region)
		}
		placed := m.Deployments[id]
		for _, use := range []struct {
			label      string
			components []string
		}{{"管理台", []string{"admin"}}, {"数据存储", []string{"storage-primary"}}, {"海外接入与交易", []string{"trade", "egress-proxy", "collector-stream", "callback-dispatcher"}}, {"因子计算", []string{"factor-engine"}}} {
			for _, component := range use.components {
				if slices.Contains(placed, component) {
					server.Uses = append(server.Uses, use.label)
					break
				}
			}
		}
		servers = append(servers, server)
	}
	rank := func(server ServerView) int {
		for i, use := range []string{"管理台", "数据存储", "海外接入与交易", "因子计算"} {
			if slices.Contains(server.Uses, use) {
				return i
			}
		}
		return 9
	}
	sort.SliceStable(servers, func(i, j int) bool { return rank(servers[i]) < rank(servers[j]) })
	return servers
}

// maskID 只露出 SecretId 的头尾，便于用户认出是哪一把。
func maskID(id string) string {
	if len(id) <= 10 {
		return strings.Repeat("•", len(id))
	}
	return id[:6] + strings.Repeat("•", 6) + id[len(id)-4:]
}
