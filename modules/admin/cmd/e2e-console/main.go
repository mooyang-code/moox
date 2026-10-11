// e2e-console 通过 Console HTTPS 入口做真实登录并调用各业务服务，用于部署后的端到端验证。
package main

import (
	"crypto/rand"
	"crypto/tls"
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
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

func main() {
	base := flag.String("base", "https://106.53.107.122:9527", "Console 地址")
	user := flag.String("user", "admin123", "用户名")
	createSpaces := flag.Bool("create-spaces", false, "先通过 Console 创建默认业务空间（走管理员授权与 Admin 本地分派）")
	flag.Parse()
	password := os.Getenv("E2E_PASSWORD")
	c := &client{base: *base, http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	code, raw := c.call("/api/admin/auth/GetLoginSalt", "", fmt.Sprintf(`{"username":%q}`, *user))
	var salt struct {
		Salt      string `json:"salt"`
		Timestamp int64  `json:"timestamp,string"`
	}
	if code != 200 || json.Unmarshal([]byte(raw), &salt) != nil || salt.Salt == "" {
		fmt.Println("GetLoginSalt 失败", code, raw)
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
		fmt.Println("Login 失败", code, raw)
		os.Exit(1)
	}
	c.token, c.key = login.Token, login.Key
	fmt.Println("登录成功: 已获得会话与请求签名密钥")
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
			fmt.Printf("创建空间 %-8s http=%d %.120s\n", sp.id, code, raw)
		}
	}
	tests := []struct{ name, path, space, body string }{
		{"auth/GetUserInfo", "/api/admin/auth/GetUserInfo", "", `{}`},
		{"space/ListSpaces", "/api/admin/space/ListSpaces", "", `{"page":{"page":1,"size":20}}`},
		{"sysdeploy/ListPlacements", "/api/admin/sysdeploy/ListPlacements", "", `{"page":{"page":1,"size":100}}`},
		{"sysdeploy/ListHosts", "/api/admin/sysdeploy/ListHosts", "", `{"page":{"page":1,"size":20}}`},
		{"storage/ListDatasets(crypto)", "/api/admin/storage/ListDatasets", "crypto", `{"space_id":"crypto","page":{"page":1,"size":5}}`},
		{"storage/ListDataNodes", "/api/admin/storage/ListDataNodes", "", `{"page":{"page":1,"size":5}}`},
		{"storage/ListViews(mooxsys)", "/api/admin/storage/ListViews", "mooxsys", `{"space_id":"mooxsys","page":{"page":1,"size":5}}`},
		{"collector/GetTaskList(crypto)", "/api/admin/collector/GetTaskList", "crypto", `{"space_id":"crypto","page":{"page":1,"size":5}}`},
		{"monitor/GetHealthOverview", "/api/admin/monitor/GetHealthOverview", "", `{}`},
		{"monitor/ListHostAgents", "/api/admin/monitor/ListHostAgents", "", `{}`},
		{"strategy/ListStrategies(crypto)", "/api/admin/strategy/ListStrategies", "crypto", `{"page":{"page":1,"page_size":5}}`},
		{"factor-mgr/ListFactorSets", "/api/admin/factor-mgr/ListFactorSets", "crypto", `{"page":{"page":1,"size":5}}`},
		{"cloudnode/GetNodeList(crypto)", "/api/admin/cloudnode/GetNodeList", "crypto", `{}`},
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
		if code != 200 || strings.Contains(raw, `"code":`) && !strings.Contains(raw, `"code":0`) && !strings.Contains(raw, `"code":"SUCCESS"`) && strings.Contains(raw, "ret_info") {
			status = "FAIL"
			failed++
		}
		fmt.Printf("%s %-36s http=%d %s\n", status, test.name, code, strings.ReplaceAll(summary, "\n", " "))
	}
	if failed > 0 {
		os.Exit(2)
	}
}
