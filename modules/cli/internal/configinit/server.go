package configinit

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

// DefaultTimeout 是录入页的有效期：超时后令牌失效，进程退出。
const DefaultTimeout = time.Hour

//go:embed page.html
var pageSource string

var pageTemplate = template.Must(template.New("page").Parse(pageSource))

// Options 控制一次录入。
type Options struct {
	// Output 是要写入的 moox.toml 路径；已存在时拒绝覆盖。
	Output string
	// Timeout 为零时用 DefaultTimeout。
	Timeout time.Duration
	// Open 为空时用系统默认浏览器；测试里替换。
	Open func(url string) error
	// Notice 接收给用户看的提示（不含任何填写的值）；为空则丢弃。
	Notice io.Writer
	// Cloud 与 Prober 为空时用腾讯云接口和真实的环境检测；测试里替换。
	Cloud  Cloud
	Prober Prober
	// KeyStore 为空时用本机的系统钥匙串（没有钥匙串时用仅本人可读的密钥文件）。
	KeyStore configcrypt.KeyStore
}

// Result 只含「写到哪里、有哪些主机」，不含任何值。
type Result struct {
	Path  string   `json:"path"`
	Hosts []string `json:"hosts"`
}

// Run 起一个只绑回环地址的一次性页面，等用户提交，渲染并写出 moox.toml（0600，不覆盖已有文件）。
// 令牌放在 URL 路径里，写出成功后立即失效；超时或取消时返回错误且不写任何文件。
func Run(ctx context.Context, options Options) (Result, error) {
	output, err := filepath.Abs(options.Output)
	if err != nil || options.Output == "" {
		return Result{}, errors.New("需要指定输出文件路径")
	}
	if err := checkOutput(output); err != nil {
		return Result{}, err
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

	keys := options.KeyStore
	if keys == nil {
		keys = configcrypt.Default()
	}
	listener, err := listenLocal()
	if err != nil {
		return Result{}, err
	}
	session, err := newSession(listener.Addr().String(), output, time.Now().Add(timeout), keys)
	if err != nil {
		_ = listener.Close()
		return Result{}, err
	}
	cloud := options.Cloud
	if cloud == nil {
		cloud = tencentCloud{}
	}
	session.cloud = newCloudCache(cloud)
	session.prober = options.Prober
	if session.prober == nil {
		session.prober = systemProber{}
	}
	return serveLocal(ctx, listener, session.localPage, serveOptions{Timeout: timeout, Open: open, Notice: notice, Name: "录入页"}, session.result)
}

// checkOutput 在起页面之前就确认输出位置可用，免得用户填完才发现写不进去。
func checkOutput(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s 已存在，不会覆盖：请先改名备份，或用 --output 指定新的路径", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return fmt.Errorf("输出目录 %s 不存在", filepath.Dir(path))
	}
	return nil
}

type session struct {
	*localPage
	output string
	keys   configcrypt.KeyStore
	cloud  *cloudCache
	prober Prober
	result chan Result
}

// newSession 的 deadline 只用于让页面显示倒计时；真正的超时由 serveLocal 负责。
func newSession(addr, output string, deadline time.Time, keys configcrypt.KeyStore) (*session, error) {
	roles, err := loadRoles()
	if err != nil {
		return nil, err
	}
	page, err := newLocalPage(addr, pageTemplate, map[string]any{"roles": roles, "deadline": deadline.UnixMilli(), "key_store": keys.Where()})
	if err != nil {
		return nil, err
	}
	s := &session{localPage: page, output: output, keys: keys, result: make(chan Result, 1)}
	page.routes = map[string]http.HandlerFunc{
		"submit":         s.submit,
		"tencent/verify": s.verifyTencent,
		"tencent/lookup": s.lookupServer,
		"factor/check":   s.checkFactor,
		"factor/trust":   s.trustFactor,
	}
	return s, nil
}

func (s *session) verifyTencent(w http.ResponseWriter, r *http.Request) {
	var keys Keys
	if !s.decode(w, r, &keys) {
		return
	}
	account, err := s.cloud.verify(r.Context(), keys)
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	reply(w, http.StatusOK, response{OK: true, Account: &account})
}

func (s *session) lookupServer(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Tencent Keys   `json:"tencent"`
		Address string `json:"address"`
	}
	if !s.decode(w, r, &request) {
		return
	}
	instance, err := s.cloud.lookup(r.Context(), request.Tencent, request.Address)
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	reply(w, http.StatusOK, response{OK: true, Instance: &instance})
}

func (s *session) checkFactor(w http.ResponseWriter, r *http.Request) {
	var target FactorServer
	if !s.decode(w, r, &target) {
		return
	}
	s.checkTarget(w, r, target)
}

// trustFactor 在用户确认指纹后记录因子机的主机密钥，再检测一次。
func (s *session) trustFactor(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Target      FactorServer `json:"target"`
		Fingerprint string       `json:"fingerprint"`
	}
	if !s.decode(w, r, &request) {
		return
	}
	target, err := request.Target.normalize()
	if err == nil {
		err = checkText("主机指纹", strings.TrimSpace(request.Fingerprint), true)
	}
	if err == nil {
		err = s.prober.Trust(r.Context(), target, request.Fingerprint)
	}
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	s.checkTarget(w, r, target)
}

func (s *session) checkTarget(w http.ResponseWriter, r *http.Request, target FactorServer) {
	report, err := s.probe(r.Context(), target)
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	reply(w, http.StatusOK, response{OK: true, Report: &report})
}

func (s *session) probe(ctx context.Context, target FactorServer) (ProbeReport, error) {
	target, err := target.normalize()
	if err != nil {
		return ProbeReport{}, err
	}
	return s.prober.Probe(ctx, target)
}

func (s *session) submit(w http.ResponseWriter, r *http.Request) {
	var request Request
	if !s.decode(w, r, &request) {
		return
	}
	raw, err := Render(r.Context(), &request, Environment{Lookup: s.cloud.lookup, Probe: s.probe})
	if err != nil {
		// 校验没过不消耗令牌：用户改完可以直接再提交。
		reply(w, http.StatusUnprocessableEntity, response{Message: err.Error()})
		return
	}
	sealed, err := configcrypt.Encrypt(raw, s.keys)
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
	if err := writeNew(s.output, sealed); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		reply(w, status, response{Message: err.Error()})
		return
	}
	s.done = true
	result := Result{Path: s.output}
	for _, host := range request.hosts {
		result.Hosts = append(result.Hosts, host.ID)
	}
	reply(w, http.StatusOK, response{OK: true})
	s.result <- result
}

// writeNew 创建新文件（已存在则失败）、权限 0600，写完落盘；写失败时删掉半截文件。
func writeNew(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s 已存在，不会覆盖：请先改名备份，或用 --output 指定新的路径: %w", path, os.ErrExist)
		}
		return fmt.Errorf("无法创建 %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}
