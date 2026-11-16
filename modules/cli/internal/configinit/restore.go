package configinit

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/consoleclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

//go:embed restore.html
var restoreSource string

var restoreTemplate = template.Must(template.New("restore").Parse(restoreSource))

// RestoreOptions 控制一次从管理台恢复配置。
type RestoreOptions struct {
	// Output 是要写入的 moox.toml 路径，秘密文件写在同一目录；两份任何一份已存在都拒绝覆盖。
	Output string
	// Timeout 为零时用 DefaultTimeout。
	Timeout time.Duration
	// Open 为空时用系统默认浏览器；测试里替换。
	Open func(url string) error
	// Notice 接收给用户看的提示（不含任何填写的值）；为空则丢弃。
	Notice io.Writer
	// KeyStore 为空时用本机的系统钥匙串（没有钥匙串时用仅本人可读的密钥文件）。
	KeyStore configcrypt.KeyStore
}

// RestoreResult 只说明写到了哪里。
type RestoreResult struct {
	Path        string `json:"path"`
	SecretsPath string `json:"secrets_path"`
	Console     string `json:"console"`
}

// RunRestore 在本机起恢复页：用户填管理台地址和管理员账号，CLI 登录后取回备份、在本机用管理员密码解开，
// 写出明文的 moox.toml 与用这台电脑的密钥加密的 moox.secrets.toml。管理员密码只用来登录管理台和在本机解开备份。
func RunRestore(ctx context.Context, options RestoreOptions) (RestoreResult, error) {
	output, err := filepath.Abs(options.Output)
	if err != nil || options.Output == "" {
		return RestoreResult{}, errors.New("需要指定输出文件路径")
	}
	for _, path := range []string{output, topology.SecretsPathFor(output)} {
		if err := checkOutput(path); err != nil {
			return RestoreResult{}, err
		}
	}
	keys := options.KeyStore
	if keys == nil {
		keys = configcrypt.Default()
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
		return RestoreResult{}, err
	}
	page, err := newLocalPage(listener.Addr().String(), restoreTemplate, map[string]any{"deadline": time.Now().Add(timeout).UnixMilli(), "key_store": keys.Where()})
	if err != nil {
		_ = listener.Close()
		return RestoreResult{}, err
	}
	restore := &restoreSession{localPage: page, output: output, keys: keys, result: make(chan RestoreResult, 1)}
	page.routes = map[string]http.HandlerFunc{"restore": restore.restore}
	return serveLocal(ctx, listener, page, serveOptions{Timeout: timeout, Open: open, Notice: notice, Name: "恢复页"}, restore.result)
}

type restoreSession struct {
	*localPage
	output string
	keys   configcrypt.KeyStore
	result chan RestoreResult
}

func (s *restoreSession) restore(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Address     string `json:"address"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		Fingerprint string `json:"fingerprint"`
	}
	if !s.decode(w, r, &request) {
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	for _, field := range []struct{ name, value string }{{"管理台地址", request.Address}, {"管理员用户名", request.Username}, {"管理员密码", request.Password}} {
		if err := checkText(field.name, field.value, true); err != nil {
			reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
			return
		}
	}
	pair, err := fetchBackup(r.Context(), request.Address, request.Fingerprint, request.Username, request.Password)
	var untrusted *consoleclient.UntrustedError
	if errors.As(err, &untrusted) {
		// 证书不受信任时把指纹交给用户核对，确认后带着指纹重试。
		reply(w, http.StatusOK, response{Fingerprint: untrusted.Fingerprint})
		return
	}
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	sealed, err := configcrypt.Encrypt(pair.SecretsText, s.keys)
	clear(pair.SecretsText)
	if err != nil {
		reply(w, http.StatusInternalServerError, response{Message: "加密配置失败：" + err.Error()})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		reply(w, http.StatusGone, response{Message: "页面已使用完毕"})
		return
	}
	secretsPath := topology.SecretsPathFor(s.output)
	if err := writePair(s.output, pair.ConfigText, secretsPath, sealed); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		reply(w, status, response{Message: err.Error()})
		return
	}
	s.done = true
	reply(w, http.StatusOK, response{OK: true})
	s.result <- RestoreResult{Path: s.output, SecretsPath: secretsPath, Console: strings.TrimSpace(request.Address)}
}
