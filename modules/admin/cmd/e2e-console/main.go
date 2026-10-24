// e2e-console 通过 Console HTTPS 入口做真实登录并调用各业务服务，用于部署后的端到端验证。
package main

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

type client struct {
	base, token, key, space string
	http                    *http.Client
}

func (c *client) call(path, space, body string) (int, string) {
	nonceRaw := make([]byte, 32)
	_, _ = rand.Read(nonceRaw)
	nonce := hex.EncodeToString(nonceRaw)
	ts := time.Now().Unix()
	request, _ := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		headers := map[string]string{}
		if space != "" {
			headers["X-Space-Id"] = space
			request.Header.Set("X-Space-Id", space)
		}
		sig, err := requestauth.Sign(c.key, requestauth.Material{Method: http.MethodPost, Path: path, Body: []byte(body), Headers: headers, Timestamp: ts, Nonce: nonce})
		if err != nil {
			return 0, err.Error()
		}
		request.Header.Set("Authorization", c.token)
		request.Header.Set("X-Access-Token", c.token)
		request.Header.Set("X-Moox-Timestamp", fmt.Sprint(ts))
		request.Header.Set("X-Moox-Nonce", nonce)
		request.Header.Set("X-Moox-Signature", sig)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err.Error()
	}
	defer response.Body.Close()
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
	createSpaces := flag.Bool("create-spaces", false, "先通过 Console 创建默认业务空间（走管理员授权与 Admin 本地分派）")
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
	c := &client{base: *base, http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}}}
	code, raw := c.call("/api/admin/auth/GetLoginSalt", "", fmt.Sprintf(`{"username":%q}`, *user))
	var salt struct {
		Salt      string `json:"salt"`
		Timestamp int64  `json:"timestamp,string"`
	}
	if code != 200 || json.Unmarshal([]byte(raw), &salt) != nil || salt.Salt == "" {
		fmt.Println("GetLoginSalt 失败, http =", code)
		os.Exit(1)
	}
	enc, err := mooxsecurity.Encrypt(password, salt.Salt+fmt.Sprint(salt.Timestamp))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	code, raw = c.call("/api/admin/auth/Login", "", fmt.Sprintf(`{"username":%q,"password_hash":%q,"salt":%q,"timestamp":%d}`, *user, enc, salt.Salt, salt.Timestamp))
	var login struct {
		Token string `json:"access_token"`
		Key   string `json:"request_signing_key"`
	}
	if code != 200 || json.Unmarshal([]byte(raw), &login) != nil || login.Token == "" {
		fmt.Println("Login 失败, http =", code, retMessage(raw))
		os.Exit(1)
	}
	c.token, c.key = login.Token, login.Key
	fmt.Println("登录成功: 已获得会话与请求签名密钥")
	createFailed := 0
	if *createSpaces {
		for _, sp := range []struct{ id, name, owner, market, tz, attrs string }{
			{"stockcn", "A股市场", "quant", "CN", "Asia/Shanghai", ""},
			{"stockhk", "港股市场", "quant", "HK", "Asia/Hong_Kong", ""},
			{"stockus", "美股市场", "quant", "US", "America/New_York", ""},
			{"crypto", "加密货币市场", "quant", "crypto", "UTC", ""},
			{"mooxsys", "MooX System", "moox", "", "UTC", `{"scope":"internal","owner_module":"monitor"}`},
		} {
			body, _ := json.Marshal(map[string]any{"space": map[string]any{"space_id": sp.id, "name": sp.name, "owner": sp.owner, "market": sp.market, "timezone": sp.tz, "status": "active", "attributes": sp.attrs}})
			code, raw := c.call("/api/admin/space/CreateSpace", "", string(body))
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
		{"space/ListSpaces", "/api/admin/space/ListSpaces", "", `{"page":{"page":1,"size":20}}`},
		{"sysdeploy/ListDeployments", "/api/admin/sysdeploy/ListDeployments", "", `{"page":{"page":1,"size":100}}`},
		{"sysdeploy/ListHosts", "/api/admin/sysdeploy/ListHosts", "", `{"page":{"page":1,"size":20}}`},
		{"storage/ListDatasets(crypto)", "/api/admin/storage/ListDatasets", "crypto", `{"space_id":"crypto","page":{"page":1,"size":5}}`},
		{"storage/ListDataNodes", "/api/admin/storage/ListDataNodes", "", `{"page":{"page":1,"size":5}}`},
		{"storage/ListViews(mooxsys)", "/api/admin/storage/ListViews", "mooxsys", `{"space_id":"mooxsys","page":{"page":1,"size":5}}`},
		{"collector/GetTaskList(crypto)", "/api/admin/collector/GetTaskList", "crypto", `{"space_id":"crypto","page":{"page":1,"size":5}}`},
		{"monitor/GetHealthOverview", "/api/admin/monitor/GetHealthOverview", "", `{}`},
		{"monitor/ListHostAgents", "/api/admin/monitor/ListHostAgents", "", `{}`},
		{"strategy/ListStrategies(crypto)", "/api/admin/strategy/ListStrategies", "crypto", `{"page":{"page":1,"page_size":5}}`},
		{"factor-mgr/ListFactorSets", "/api/admin/factor-mgr/ListFactorSets", "crypto", `{"page":{"page":1,"size":5}}`},
		{"cloudfunction/ListFunctionInstances(crypto)", "/api/admin/cloudfunction/ListFunctionInstances", "crypto", `{}`},
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

// retMessage 只取出响应里的错误提示，避免把认证响应里的令牌写入日志。
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
