package configinit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

type outcome struct {
	result Result
	err    error
}

type harness struct {
	t      *testing.T
	keys   *configcrypt.MemoryStore
	cloud  *fakeCloud
	prober *fakeProber
	base   string // http://127.0.0.1:port/<token>/
	host   string // 127.0.0.1:port
	output string
	done   chan outcome
	notice *bytes.Buffer
	cancel context.CancelFunc
}

func start(t *testing.T, timeout time.Duration) *harness {
	t.Helper()
	h := &harness{t: t, output: filepath.Join(t.TempDir(), "moox.toml"), done: make(chan outcome, 1), notice: &bytes.Buffer{},
		keys: &configcrypt.MemoryStore{}, cloud: newFakeCloud(), prober: &fakeProber{report: linuxReport()}}
	urls := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	go func() {
		result, err := Run(ctx, Options{
			Output: h.output, Timeout: timeout, Notice: h.notice, Cloud: h.cloud, Prober: h.prober, KeyStore: h.keys,
			Open: func(u string) error { urls <- u; return nil },
		})
		h.done <- outcome{result, err}
	}()
	select {
	case h.base = <-urls:
	case o := <-h.done:
		t.Fatalf("Run 提前结束: %v", o.err)
	case <-time.After(5 * time.Second):
		t.Fatal("没有打开录入页")
	}
	parsed, err := url.Parse(h.base)
	if err != nil {
		t.Fatal(err)
	}
	h.host = parsed.Host
	return h
}

func (h *harness) request(method, path string, mutate func(*http.Request), body []byte) (*http.Response, string) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://"+h.host)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

func (h *harness) wait() outcome {
	h.t.Helper()
	select {
	case o := <-h.done:
		return o
	case <-time.After(10 * time.Second):
		h.t.Fatal("Run 没有结束")
		return outcome{}
	}
}

func validBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPageIsServedWithStrictHeadersAndNoSecrets(t *testing.T) {
	before := time.Now()
	h := start(t, 45*time.Minute)
	resp, body := h.request(http.MethodGet, "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 %d", resp.StatusCode)
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff"} {
		if got := resp.Header.Get(header); got != want {
			t.Fatalf("%s = %q，期望 %q", header, got, want)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "connect-src 'self'", "form-action 'none'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP 缺少 %q: %s", want, csp)
		}
	}
	nonce := between(csp, "script-src 'nonce-", "'")
	if nonce == "" || !strings.Contains(body, `<script nonce="`+nonce+`">`) || !strings.Contains(body, `<style nonce="`+nonce+`">`) {
		t.Fatal("页面的脚本与样式必须带本次响应的 nonce")
	}
	for _, external := range []string{"<link", "<img", "<iframe", `src="http`, "url(", "@import"} {
		if strings.Contains(body, external) {
			t.Fatalf("页面不应引用外部资源: %s", external)
		}
	}
	// 外部链接只有腾讯云的帮助入口，而且在新窗口打开、不带来源信息。
	for rest := body; strings.Contains(rest, `href="`); {
		rest = rest[strings.Index(rest, `href="`)+len(`href="`):]
		link := rest[:strings.Index(rest, `"`)]
		if !strings.HasPrefix(link, "https://console.cloud.tencent.com/") && !strings.HasPrefix(link, "https://cloud.tencent.com/document/") {
			t.Fatalf("页面不应链接到 %q", link)
		}
		if tag := rest[:strings.Index(rest, ">")]; !strings.Contains(tag, `target="_blank"`) || !strings.Contains(tag, `rel="noopener noreferrer"`) {
			t.Fatalf("链接 %q 应在新窗口打开且带 noopener noreferrer", link)
		}
	}
	if !strings.Contains(body, `href="https://console.cloud.tencent.com/cam"`) {
		t.Fatal("第一步应引导用户去访问管理创建密钥")
	}
	// 隐私说明要讲清去向，且不能承诺本地加密（本地是仅本人可读的明文文件）。
	if !strings.Contains(body, "不会发给 AI，也不会发给 MooX 开发者") || strings.Contains(body, "本地加密") {
		t.Fatal("底部应如实说明信息的去向")
	}
	// 隐私说明按实际实现描述：本机加密保存、密钥位置、不发给 AI 和开发者。
	for _, want := range []string{"加密保存在这台电脑上", "不会发给 AI，也不会发给 MooX 开发者", `"key_store":"测试内存"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("页面应说明 %q", want)
		}
	}
	// 系统用到的每个腾讯云服务都要列出对应的预设策略，免得用户不知道授什么权限。
	for _, policy := range []string{"QcloudSCFFullAccess", "QcloudCOSFullAccess", "QcloudCLSFullAccess", "QcloudLighthouseFullAccess", "QcloudCVMFullAccess", "QcloudVPCFullAccess"} {
		if !strings.Contains(body, "<code>"+policy+"</code>") {
			t.Fatalf("权限清单里缺少 %s", policy)
		}
	}
	// 用户不需要理解底层细节：页面里不出现文件名和路径。
	for _, detail := range []string{h.output, "moox.toml", "placements"} {
		if strings.Contains(body, detail) {
			t.Fatalf("页面不应出现底层细节 %q", detail)
		}
	}
	if strings.Contains(body, strings.TrimSuffix(strings.TrimPrefix(h.base, "http://"+h.host+"/"), "/")) {
		t.Fatal("页面内容里不应出现令牌")
	}
	var boot struct {
		Roles    []Role `json:"roles"`
		Deadline int64  `json:"deadline"`
	}
	if err := json.Unmarshal([]byte(between(body, `id="boot">`, "</script>")), &boot); err != nil {
		t.Fatalf("页面应内嵌可解析的初始数据: %v", err)
	}
	// 倒计时用的截止时间 = 启动时刻 + 有效期，页面里有倒计时条。
	if low, high := before.Add(45*time.Minute).UnixMilli(), time.Now().Add(45*time.Minute).UnixMilli(); boot.Deadline < low || boot.Deadline > high {
		t.Fatalf("截止时间 %d 应在 [%d, %d] 内", boot.Deadline, low, high)
	}
	if !strings.Contains(body, `id="timer"`) {
		t.Fatal("页面应有倒计时条")
	}
	var keys []string
	for _, role := range boot.Roles {
		keys = append(keys, role.Key)
		if role.Title == "" || role.Purpose == "" || len(role.Specs) == 0 {
			t.Fatalf("角色 %s 缺少说明或建议配置: %+v", role.Key, role)
		}
	}
	if !slices.Equal(keys, []string{RoleStorage, RoleControl, RoleOverseas, RoleFactor}) {
		t.Fatalf("角色顺序 = %v", keys)
	}
	// 用户只看到用途说明，不看到组件和主机 ID。
	for _, id := range []string{"storage-primary", "console-proxy", "egress-proxy", "factor-mgr", "compute-1"} {
		if strings.Contains(body, id) {
			t.Fatalf("页面不应展示组件 ID %q", id)
		}
	}
}

func between(s, left, right string) string {
	i := strings.Index(s, left)
	if i < 0 {
		return ""
	}
	s = s[i+len(left):]
	if j := strings.Index(s, right); j >= 0 {
		return s[:j]
	}
	return ""
}

func TestRequestsWithoutTheTokenOrFromOtherHostsAreRejected(t *testing.T) {
	h := start(t, time.Minute)
	root := "http://" + h.host
	for name, target := range map[string]string{"没有令牌": root + "/", "错误令牌": root + "/wrong-token/", "令牌被截断": root + h.base[len(root):len(h.base)-3] + "/", "提交也要令牌": root + "/wrong/submit"} {
		resp, err := http.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: 状态 %d，应为 404", name, resp.StatusCode)
		}
	}
	// 以另一个主机名访问同一个回环端口（DNS 重绑定）必须拒绝，即使令牌是对的。
	resp, _ := h.request(http.MethodGet, "", func(r *http.Request) { r.Host = "attacker.example:" + strings.Split(h.host, ":")[1] }, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("陌生 Host 状态 %d，应为 403", resp.StatusCode)
	}
	resp, _ = h.request(http.MethodGet, "", func(r *http.Request) { r.Host = "127.0.0.1:1" }, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("端口不符的 Host 状态 %d，应为 403", resp.StatusCode)
	}
	resp, _ = h.request(http.MethodDelete, "", nil, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE 状态 %d，应为 405", resp.StatusCode)
	}
}

func TestSubmitRequiresASameOriginJSONRequest(t *testing.T) {
	h := start(t, time.Minute)
	body := validBody(t)
	cases := map[string]struct {
		mutate func(*http.Request)
		want   int
	}{
		"没有 Origin":      {func(r *http.Request) { r.Header.Del("Origin") }, http.StatusForbidden},
		"别的 Origin":      {func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, http.StatusForbidden},
		"https 的 Origin": {func(r *http.Request) { r.Header.Set("Origin", "https://"+h.host) }, http.StatusForbidden},
		"跨站来源":           {func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		"表单类型":           {func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, http.StatusUnsupportedMediaType},
		"没有类型":           {func(r *http.Request) { r.Header.Del("Content-Type") }, http.StatusUnsupportedMediaType},
	}
	for name, c := range cases {
		resp, _ := h.request(http.MethodPost, "submit", c.mutate, body)
		if resp.StatusCode != c.want {
			t.Fatalf("%s: 状态 %d，应为 %d", name, resp.StatusCode, c.want)
		}
	}
	if _, err := os.Stat(h.output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("被拒绝的请求不能写出文件")
	}
	// 所有被拒绝的请求都不消耗令牌：合法请求仍然能成功。
	resp, _ := h.request(http.MethodPost, "submit", nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合法请求状态 %d", resp.StatusCode)
	}
	h.wait()
}

func TestMalformedAndOversizedBodiesAreRejected(t *testing.T) {
	h := start(t, time.Minute)
	for name, body := range map[string][]byte{
		"不是 JSON":  []byte("not json"),
		"未知字段":     []byte(`{"admin":{"username":"a"},"surprise":1}`),
		"超过上限":     append([]byte(`{"admin":{"username":"`), bytes.Repeat([]byte("a"), maxBodyBytes+10)...),
		"类型不对":     []byte(`{"hosts":"control"}`),
		"空对象会校验失败": []byte(`{}`),
	} {
		resp, _ := h.request(http.MethodPost, "submit", nil, body)
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s: 状态 %d，应为 400 或 422", name, resp.StatusCode)
		}
	}
	if _, err := os.Stat(h.output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("非法请求不能写出文件")
	}
}

func TestUnknownFieldsAreRejectedEvenWhenTheRestIsValid(t *testing.T) {
	h := start(t, time.Minute)
	raw, err := json.Marshal(sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["surprise"] = 1
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := h.request(http.MethodPost, "submit", nil, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("多出未知字段的请求状态 %d，应为 400", resp.StatusCode)
	}
	if _, err := os.Stat(h.output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("带未知字段的请求不能写出文件")
	}
}

func TestInvalidContentIsExplainedWithoutEchoingSecretsAndKeepsTheTokenUsable(t *testing.T) {
	h := start(t, time.Minute)
	request := sampleRequest()
	request.Notification.WebhookURL = "https://evil.example/hook"
	body, _ := json.Marshal(request)
	resp, text := h.request(http.MethodPost, "submit", nil, body)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("状态 %d，应为 422", resp.StatusCode)
	}
	if !strings.Contains(text, "webhook") {
		t.Fatalf("要告诉用户哪里不对: %s", text)
	}
	for _, secret := range []string{"Adm1n-P@ss", "key-example", "pw-control", "pw-storage", "pw-compute", "evil.example"} {
		if strings.Contains(text, secret) {
			t.Fatalf("错误响应泄露了 %q: %s", secret, text)
		}
	}
	if _, err := os.Stat(h.output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("校验失败不能写文件")
	}
	// 改好之后同一个页面直接再提交。
	resp, _ = h.request(http.MethodPost, "submit", nil, validBody(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("改正后的提交状态 %d", resp.StatusCode)
	}
	h.wait()
}

func TestSubmitWritesTheFileAndEndsTheSession(t *testing.T) {
	h := start(t, time.Minute)
	resp, text := h.request(http.MethodPost, "submit", nil, validBody(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 %d: %s", resp.StatusCode, text)
	}
	var reply response
	if err := json.Unmarshal([]byte(text), &reply); err != nil || !reply.OK || strings.Contains(text, h.output) {
		t.Fatalf("响应 = %q", text)
	}
	outcome := h.wait()
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if outcome.result.Path != h.output || strings.Join(outcome.result.Hosts, ",") != "control,storage,compute-1" {
		t.Fatalf("结果 = %+v", outcome.result)
	}
	raw, err := os.ReadFile(h.output)
	if err != nil {
		t.Fatal(err)
	}
	// 写出的文件是加密的：不含任何明文配置，用本机密钥能解开并通过 CLI 校验。
	if !configcrypt.IsEncrypted(raw) {
		t.Fatal("写出的 moox.toml 应加密保存")
	}
	for _, plain := range []string{"Adm1n-P@ss", "key-example", "pw-storage", "[admin]", "198.51.100.10"} {
		if strings.Contains(string(raw), plain) {
			t.Fatalf("加密文件里出现了明文 %q", plain)
		}
	}
	plain, err := configcrypt.Decrypt(raw, h.keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setupconfig.Parse(plain); err != nil {
		t.Fatalf("写出的文件应通过 CLI 校验: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(h.output)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("权限 = %v，应为 0600", info.Mode().Perm())
		}
	}
	// 会话结束后服务关闭，令牌不能再用。
	if conn, err := net.DialTimeout("tcp", h.host, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("提交成功后录入服务应关闭")
	}
	// 返回值和提示里不含任何填写的值。
	encoded, _ := json.Marshal(outcome.result)
	for _, secret := range []string{"Adm1n-P@ss", "key-example", "pw-control"} {
		if strings.Contains(string(encoded), secret) || strings.Contains(h.notice.String(), secret) {
			t.Fatalf("结果或提示里出现了 %q", secret)
		}
	}
}

func (h *harness) post(path string, body any) (int, response) {
	h.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	resp, text := h.request(http.MethodPost, path, nil, raw)
	var reply response
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		h.t.Fatalf("%s 的响应不是 JSON: %s", path, text)
	}
	return resp.StatusCode, reply
}

func TestTencentEndpointsVerifyKeysAndRecognizeServers(t *testing.T) {
	h := start(t, time.Minute)
	keys := Keys{SecretID: "AKIDexample", SecretKey: "key-example"}
	if status, reply := h.post("tencent/verify", keys); status != http.StatusOK || reply.Account == nil || reply.Account.AccountID != "100012345678" {
		t.Fatalf("验证密钥: %d %+v", status, reply)
	}
	if status, reply := h.post("tencent/verify", Keys{SecretID: "AKIDexample", SecretKey: "wrong"}); status != http.StatusUnprocessableEntity || reply.OK || strings.Contains(reply.Message, "wrong") {
		t.Fatalf("错误的密钥应 422 且不回显: %d %+v", status, reply)
	}
	status, reply := h.post("tencent/lookup", map[string]any{"tencent": keys, "address": "198.51.100.11"})
	if status != http.StatusOK || reply.Instance == nil || reply.Instance.Region != "ap-nanjing" || reply.Instance.ZoneName != "南京一区" || reply.Instance.MemoryGB != 16 {
		t.Fatalf("识别服务器: %d %+v", status, reply)
	}
	if status, reply := h.post("tencent/lookup", map[string]any{"tencent": keys, "address": "203.0.113.50"}); status != http.StatusUnprocessableEntity || !strings.Contains(reply.Message, "不在当前腾讯云账号下") {
		t.Fatalf("不在账号下: %d %+v", status, reply)
	}
	// 提交时复用已经识别过的结果，不再逐个地域查一遍。
	before := h.cloud.count()
	resp, text := h.request(http.MethodPost, "submit", nil, validBody(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("提交: %d %s", resp.StatusCode, text)
	}
	if got := h.cloud.count() - before; got != 2 {
		t.Fatalf("存储服务器已经识别过，提交时只应再查另外两台，实际查了 %d 次", got)
	}
	h.wait()
}

func TestFactorCheckAsksToConfirmTheHostKeyFirst(t *testing.T) {
	h := start(t, time.Minute)
	h.prober.hostKey = "SHA256:abcdef"
	target := FactorServer{Mode: FactorRemote, Address: "192.168.0.103", SSHUser: "builder", SSHPassword: "pw-build"}
	status, reply := h.post("factor/check", target)
	if status != http.StatusOK || reply.Report == nil || reply.Report.HostKey != "SHA256:abcdef" || len(reply.Report.Items) != 0 {
		t.Fatalf("首次连接应返回指纹待确认: %d %+v", status, reply)
	}
	if status, reply := h.post("factor/trust", map[string]any{"target": target, "fingerprint": "SHA256:other"}); status != http.StatusUnprocessableEntity || reply.OK {
		t.Fatalf("指纹对不上应拒绝: %d %+v", status, reply)
	}
	status, reply = h.post("factor/trust", map[string]any{"target": target, "fingerprint": "SHA256:abcdef"})
	if status != http.StatusOK || reply.Report == nil || !reply.Report.OK || reply.Report.OS != "linux" {
		t.Fatalf("确认指纹后应完成检测: %d %+v", status, reply)
	}
	if status, _ := h.post("factor/trust", map[string]any{"target": FactorServer{Mode: FactorLocal}, "fingerprint": "SHA256:abcdef"}); status != http.StatusUnprocessableEntity {
		t.Fatal("本机不需要确认指纹")
	}
	if status, reply := h.post("factor/check", FactorServer{Mode: FactorRemote, Address: "192.168.0.103"}); status != http.StatusUnprocessableEntity || reply.OK {
		t.Fatalf("缺少 SSH 信息应 422: %d %+v", status, reply)
	}
	if strings.Contains(h.notice.String(), "pw-build") {
		t.Fatal("提示里不应出现密码")
	}
}

func TestAPIEndpointsShareTheRequestChecks(t *testing.T) {
	h := start(t, time.Minute)
	for _, path := range []string{"tencent/verify", "tencent/lookup", "factor/check", "factor/trust"} {
		resp, _ := h.request(http.MethodPost, path, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, []byte(`{}`))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s：跨站请求状态 %d，应为 403", path, resp.StatusCode)
		}
		resp, _ = h.request(http.MethodGet, path, nil, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s：GET 状态 %d，应为 405", path, resp.StatusCode)
		}
		resp, _ = h.request(http.MethodPost, path, nil, []byte(`{"surprise":1}`))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s：未知字段状态 %d，应为 400", path, resp.StatusCode)
		}
	}
	if resp, _ := h.request(http.MethodPost, "tencent/other", nil, []byte(`{}`)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知接口状态 %d，应为 404", resp.StatusCode)
	}
}

func TestConcurrentSubmissionsWriteExactlyOnce(t *testing.T) {
	h := start(t, time.Minute)
	body := validBody(t)
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, h.base+"submit", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://"+h.host)
			resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				statuses <- 0 // 会话结束后连接被拒绝也算被挡掉
				return
			}
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	ok := 0
	for status := range statuses {
		if status == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("成功 %d 次，应恰好 1 次", ok)
	}
	if outcome := h.wait(); outcome.err != nil {
		t.Fatal(outcome.err)
	}
}

func TestExistingOutputIsNeverOverwrittenAndFailsBeforeAskingTheUser(t *testing.T) {
	output := filepath.Join(t.TempDir(), "moox.toml")
	if err := os.WriteFile(output, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := false
	_, err := Run(context.Background(), Options{Output: output, Open: func(string) error { opened = true; return nil }})
	if err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("err = %v", err)
	}
	if opened {
		t.Fatal("输出已存在时不应打开页面让用户白填")
	}
	if got, _ := os.ReadFile(output); string(got) != "precious" {
		t.Fatal("已有文件被改动了")
	}
	if _, err := Run(context.Background(), Options{Output: filepath.Join(t.TempDir(), "missing-dir", "moox.toml")}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("输出目录不存在时应报错: %v", err)
	}
}

// 页面打开期间有人先创建了同名文件：提交时仍然不能覆盖。
func TestFileCreatedWhileTheFormIsOpenIsNotOverwritten(t *testing.T) {
	h := start(t, time.Minute)
	if err := os.WriteFile(h.output, []byte("created meanwhile"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, text := h.request(http.MethodPost, "submit", nil, validBody(t))
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text, "已存在") {
		t.Fatalf("状态 %d: %s", resp.StatusCode, text)
	}
	if got, _ := os.ReadFile(h.output); string(got) != "created meanwhile" {
		t.Fatal("文件被覆盖了")
	}
	h.cancel()
	h.wait()
}

func TestTimeoutAndCancellationWriteNothing(t *testing.T) {
	h := start(t, 300*time.Millisecond)
	outcome := h.wait()
	if outcome.err == nil || !strings.Contains(outcome.err.Error(), "超过") {
		t.Fatalf("超时应报错: %v", outcome.err)
	}
	if _, err := os.Stat(h.output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("超时不能写文件")
	}
	if conn, err := net.DialTimeout("tcp", h.host, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("超时后服务应关闭")
	}

	h = start(t, time.Minute)
	h.cancel()
	if outcome := h.wait(); !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled: %v", outcome.err)
	}
}

func TestBrowserFailureShowsTheAddressButSuccessDoesNot(t *testing.T) {
	// 浏览器打不开：给出地址让用户手动打开。
	var failed bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _ = Run(ctx, Options{Output: filepath.Join(t.TempDir(), "moox.toml"), Notice: &failed, Open: func(string) error { return errors.New("no browser") }})
	if !strings.Contains(failed.String(), "http://127.0.0.1:") {
		t.Fatalf("打不开浏览器时应给出地址: %q", failed.String())
	}
	// 浏览器成功打开：提示里不出现带令牌的地址。
	h := start(t, 300*time.Millisecond)
	token := strings.Trim(strings.TrimPrefix(h.base, "http://"+h.host), "/")
	h.wait()
	if strings.Contains(h.notice.String(), token) || strings.Contains(h.notice.String(), "http://") {
		t.Fatalf("成功打开浏览器后提示里不该出现地址和令牌: %q", h.notice.String())
	}
}

func TestDefaultTimeoutIsOneHour(t *testing.T) {
	if DefaultTimeout != time.Hour {
		t.Fatalf("默认有效期 = %s，应为 1 小时", DefaultTimeout)
	}
}
