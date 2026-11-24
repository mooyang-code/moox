// e2e-console 通过 Console HTTPS 入口做真实登录并调用各业务服务，用于部署后的端到端验证。
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// sessionCookie 是管理台的会话 Cookie 名（与 Admin 的 session.CookieName 一致）。
const sessionCookie = "__Host-moox_session"

// client 像浏览器一样调用管理台：登录拿到会话 Cookie，之后每个请求带上它；POST 带本站 Origin。
type client struct {
	base    string
	session *http.Cookie
	http    *http.Client
}

func (c *client) call(path, space, body string) (int, string) {
	request, err := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", c.base)
	if space != "" {
		request.Header.Set("X-Space-Id", space)
	}
	if c.session != nil {
		request.AddCookie(c.session)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err.Error()
	}
	defer response.Body.Close()
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookie && cookie.Value != "" {
			c.session = &http.Cookie{Name: cookie.Name, Value: cookie.Value}
		}
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "读取响应失败: " + err.Error()
	}
	return response.StatusCode, string(raw)
}

func main() {
	base := flag.String("base", "", "Console 地址，例如 https://control.example:9527（必填）")
	caFile := flag.String("ca-file", "", "internal CA 证书 PEM；默认使用系统信任库并校验主机名")
	insecure := flag.Bool("insecure-skip-verify", false, "仅限一次性联调：跳过证书校验（密码经环境变量传入，不要在不可信网络使用）")
	user := flag.String("user", "admin123", "用户名")
	createSpaces := flag.Bool("create-spaces", false, "先通过 Console 在 Storage 创建默认业务空间")
	flag.Parse()
	password := os.Getenv("E2E_PASSWORD")
	if *base == "" || password == "" {
		fmt.Println("需要 -base 和环境变量 E2E_PASSWORD")
		os.Exit(1)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: *insecure}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		pool := x509.NewCertPool()
		if err != nil || !pool.AppendCertsFromPEM(pem) {
			fmt.Println("无法读取 -ca-file 指定的 CA 证书")
			os.Exit(1)
		}
		tlsConfig.RootCAs = pool
	}
	c := &client{base: strings.TrimRight(*base, "/"), http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}}}
	loginBody, _ := json.Marshal(map[string]string{"username": *user, "password": password})
	code, raw := c.call("/api/admin/auth/Login", "", string(loginBody))
	if !businessOK(code, raw) || c.session == nil {
		fmt.Println("Login 失败, http =", code, retMessage(raw))
		os.Exit(1)
	}
	fmt.Println("登录成功: 已获得会话 Cookie")
	createFailed := 0
	if *createSpaces {
		for _, sp := range []struct{ id, name, owner, market, tz string }{
			{"stockcn", "A股市场", "quant", "CN", "Asia/Shanghai"},
			{"stockhk", "港股市场", "quant", "HK", "Asia/Hong_Kong"},
			{"stockus", "美股市场", "quant", "US", "America/New_York"},
			{"crypto", "加密货币市场", "quant", "crypto", "UTC"},
		} {
			body, _ := json.Marshal(map[string]any{"space": map[string]any{"space_id": sp.id, "name": sp.name, "owner": sp.owner, "market": sp.market, "timezone": sp.tz, "status": "active", "attributes": map[string]string{}}})
			code, raw := c.call("/api/admin/storage/CreateSpace", "", string(body))
			// 已存在视为成功（重复执行）；其余失败计入验收结果。
			ok := businessOK(code, raw) || strings.Contains(raw, "UNIQUE") || strings.Contains(raw, "already exists") || strings.Contains(raw, "已存在")
			if !ok {
				createFailed++
			}
			fmt.Printf("创建空间 %-8s ok=%v http=%d %.120s\n", sp.id, ok, code, raw)
		}
	}
	tests := []struct{ name, path, space, body string }{
		{"auth/GetUserInfo", "/api/admin/auth/GetUserInfo", "", `{}`},
		{"storage/ListSpaces", "/api/admin/storage/ListSpaces", "", `{"page":{"page":1,"size":20}}`},
		{"sysdeploy/ListDeployments", "/api/admin/sysdeploy/ListDeployments", "", `{"page":{"page":1,"size":100}}`},
		{"sysdeploy/ListHosts", "/api/admin/sysdeploy/ListHosts", "", `{"page":{"page":1,"size":20}}`},
		{"storage/ListDatasets(crypto)", "/api/admin/storage/ListDatasets", "crypto", `{"space_id":"crypto","page":{"page":1,"size":5}}`},
		{"storage/ListViews(stockus)", "/api/admin/storage/ListViews", "stockus", `{"space_id":"stockus","page":{"page":1,"size":5}}`},
		{"collector/GetTaskList(crypto)", "/api/admin/collector/GetTaskList", "crypto", `{"space_id":"crypto","page":{"page":1,"size":5}}`},
		{"monitor/GetHealthOverview", "/api/admin/monitor/GetHealthOverview", "", `{}`},
		{"monitor/ListHostAgents", "/api/admin/monitor/ListHostAgents", "", `{}`},
		{"strategy/ListStrategies(crypto)", "/api/admin/strategy/ListStrategies", "crypto", `{"page":{"page":1,"page_size":5}}`},
		{"factor-mgr/ListFactorTasks", "/api/admin/factor-mgr/ListFactorTasks", "crypto", `{"page":{"page":1,"size":5}}`},
		// 云账号、代码包、云函数在 Collector 上；ListCloudFunctions 会逐个调腾讯云读回，探测用不碰云的 ListCloudAccounts。
		{"collector/ListCloudAccounts(crypto)", "/api/admin/collector/ListCloudAccounts", "crypto", `{}`},
		{"trade/ListTradingAccounts(crypto)", "/api/admin/trade/ListTradingAccounts", "crypto", `{"page":{"page":1,"size":5}}`},
	}
	failed := 0
	for _, test := range tests {
		code, raw := c.call(test.path, test.space, test.body)
		summary := raw
		if len(summary) > 160 {
			summary = summary[:160] + "..."
		}
		status := "OK  "
		if !businessOK(code, raw) {
			status = "FAIL"
			failed++
		}
		fmt.Printf("%s %-36s http=%d %s\n", status, test.name, code, strings.ReplaceAll(summary, "\n", " "))
	}
	failed += createFailed
	if failed > 0 {
		os.Exit(2)
	}
}

// businessOK 要求 HTTP 200、完整可解析的 JSON 对象，且顶层 ret_info.code 为 0。
func businessOK(code int, raw string) bool {
	if code != 200 {
		return false
	}
	var body struct {
		RetInfo *struct {
			Code json.Number `json:"code"`
		} `json:"ret_info"`
	}
	if json.Unmarshal([]byte(raw), &body) != nil || body.RetInfo == nil {
		return false
	}
	return body.RetInfo.Code.String() == "0"
}

// retMessage 只取出响应里的错误提示，不回显响应的其余内容。
func retMessage(raw string) string {
	var body struct {
		RetInfo struct {
			Msg string `json:"msg"`
		} `json:"ret_info"`
	}
	if json.Unmarshal([]byte(raw), &body) != nil {
		return "(响应不可解析)"
	}
	return body.RetInfo.Msg
}
