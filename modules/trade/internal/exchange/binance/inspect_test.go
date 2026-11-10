package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

// 检测测试用官方样例：apiRestrictions、papi 账户、fapi 账户、持仓模式都取自 testdata（来源见 testdata/README.md）。
// 错误体按官方错误码文档（spot errors.md）的格式拼装，只改错误码和说明。

// errorBody 是币安错误响应的格式：{"code":-2015,"msg":"..."}。
func errorBody(code int, message string) string {
	raw, _ := json.Marshal(map[string]any{"code": code, "msg": message})
	return string(raw)
}

// inspectServer 按路径回放：每个路径给一个状态码和响应体，没有登记的路径返回 -1121（视为意外错误）。
type inspectServer struct {
	t        *testing.T
	routes   map[string]inspectReply
	requests []string
}

type inspectReply struct {
	status int
	body   string
}

func (s *inspectServer) handler(w http.ResponseWriter, r *http.Request) {
	s.requests = append(s.requests, r.URL.Path)
	reply, ok := s.routes[r.URL.Path]
	if !ok {
		reply = inspectReply{http.StatusBadRequest, errorBody(-1121, "Invalid symbol.")}
	}
	w.WriteHeader(reply.status)
	_, _ = w.Write([]byte(reply.body))
}

func newInspectServer(t *testing.T, routes map[string]inspectReply) (*inspectServer, inspectClients) {
	t.Helper()
	state := &inspectServer{t: t, routes: routes}
	server := httptest.NewServer(http.HandlerFunc(state.handler))
	t.Cleanup(server.Close)
	// 三个域名都指向同一个测试服务；用不同的客户端对象让域名级限流状态互不影响。
	return state, inspectClients{spot: httpclient.New(server.URL), swap: httpclient.New(server.URL), papi: httpclient.New(server.URL)}
}

// restrictions 取官方 apiRestrictions 样例并按需改字段。
func restrictions(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	var values map[string]any
	if err := json.Unmarshal(sample(t, "api_restrictions.json"), &values); err != nil {
		t.Fatal(err)
	}
	mutate(values)
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ok(body []byte) inspectReply { return inspectReply{http.StatusOK, string(body)} }

func TestInspectPortfolioMargin(t *testing.T) {
	_, clients := newInspectServer(t, map[string]inspectReply{
		"/sapi/v1/account/apiRestrictions": ok(sample(t, "api_restrictions.json")),
		"/papi/v1/account":                 ok(sample(t, "papi_account.json")),
		"/papi/v1/um/positionSide/dual":    ok([]byte(`{"dualSidePosition": false}`)),
	})
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountMode != exchange.AccountModeBinancePortfolioMargin || result.PositionMode != exchange.PositionModeOneWay ||
		!result.CanTradeSpot || !result.CanTradeSwap || len(result.Blockers) != 0 || result.CanWithdraw {
		t.Fatalf("result = %+v", result)
	}
	if !slices.Equal(result.AvailableMarkets, []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}) {
		t.Fatalf("可开通市场 = %v", result.AvailableMarkets)
	}
}

func TestInspectPortfolioMarginKeyWithoutTradingPermissionBlocks(t *testing.T) {
	read := restrictions(t, func(v map[string]any) { v["enablePortfolioMarginTrading"] = false })
	_, clients := newInspectServer(t, map[string]inspectReply{
		"/sapi/v1/account/apiRestrictions": ok([]byte(read)),
		"/papi/v1/account":                 ok(sample(t, "papi_account.json")),
		"/papi/v1/um/positionSide/dual":    ok([]byte(`{"dualSidePosition": false}`)),
	})
	result, err := inspectWith(context.Background(), exchange.InspectRequest{
		Environment: exchange.AccountEnvironmentProduction, Markets: []exchange.MarketType{exchange.MarketTypeSwap},
	}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Blockers, exchange.FindingMarketNotPermitted) {
		t.Fatalf("没有交易权限应阻断：%+v", result.Blockers)
	}
}

// standardRoutes 是普通账户：papi 返回 -2015，fapi 账户、持仓模式正常。
func standardRoutes(t *testing.T, dualSide string, mutate func(map[string]any)) map[string]inspectReply {
	read := restrictions(t, func(v map[string]any) {
		v["enablePortfolioMarginTrading"] = false
		v["enableSpotAndMarginTrading"] = true
		v["enableFutures"] = true
		if mutate != nil {
			mutate(v)
		}
	})
	return map[string]inspectReply{
		"/sapi/v1/account/apiRestrictions": ok([]byte(read)),
		"/papi/v1/account":                 {http.StatusUnauthorized, errorBody(-2015, "Invalid API-key, IP, or permissions for action.")},
		"/fapi/v3/account":                 ok(sample(t, "fapi_account_v3.json")),
		"/fapi/v1/positionSide/dual":       ok([]byte(dualSide)),
		"/fapi/v1/multiAssetsMargin":       ok(sample(t, "fapi_multi_assets.json")),
	}
}

func TestInspectStandardAccount(t *testing.T) {
	_, clients := newInspectServer(t, standardRoutes(t, `{"dualSidePosition": false}`, nil))
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountMode != exchange.AccountModeBinanceStandard || result.PositionMode != exchange.PositionModeOneWay ||
		!result.UniversalTransfer || len(result.Blockers) != 0 || !result.MultiAssetsMargin {
		t.Fatalf("result = %+v", result)
	}
}

func TestInspectStandardAccountWithoutUniversalTransferBlocksBothMarkets(t *testing.T) {
	_, clients := newInspectServer(t, standardRoutes(t, `{"dualSidePosition": false}`, func(v map[string]any) {
		v["permitsUniversalTransfer"] = false
	}))
	both := []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction, Markets: both}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Blockers, exchange.FindingNoUniversalTransfer) {
		t.Fatalf("双市场没有万向划转权限应阻断：%+v", result.Blockers)
	}
	// 只开一个市场不需要划转。
	if findings := result.BlockersFor([]exchange.MarketType{exchange.MarketTypeSwap}); len(findings) != 0 {
		t.Fatalf("只开合约不应阻断：%+v", findings)
	}
}

func TestInspectWithdrawPermissionBlocks(t *testing.T) {
	_, clients := newInspectServer(t, standardRoutes(t, `{"dualSidePosition": false}`, func(v map[string]any) {
		v["enableWithdrawals"] = true
	}))
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Blockers, exchange.FindingWithdrawPermission) {
		t.Fatalf("有提现权限应阻断：%+v", result.Blockers)
	}
}

func TestInspectHedgeModeBlocks(t *testing.T) {
	// 官方样例 fapi_position_dual.json 的 dualSidePosition 就是 true。
	_, clients := newInspectServer(t, standardRoutes(t, string(sample(t, "fapi_position_dual.json")), nil))
	swapOnly := []exchange.MarketType{exchange.MarketTypeSwap}
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction, Markets: swapOnly}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if result.PositionMode != exchange.PositionModeHedge || !hasFinding(result.Blockers, exchange.FindingHedgeMode) {
		t.Fatalf("双向持仓应阻断：%+v", result)
	}
	// 预览（还没选市场）只给和市场无关的阻断项：双向持仓要等选了合约才判。
	if findings := result.BlockersFor(nil); hasFinding(findings, exchange.FindingHedgeMode) {
		t.Fatalf("没选市场时不应因持仓模式阻断：%+v", findings)
	}
	// 只开现货时持仓模式不相关。
	if findings := result.BlockersFor([]exchange.MarketType{exchange.MarketTypeSpot}); hasFinding(findings, exchange.FindingHedgeMode) {
		t.Fatalf("只开现货不应因持仓模式阻断：%+v", findings)
	}
}

// 预览（不带市场）：双向持仓这类和市场有关的问题不是阻断项，写成“开通对应市场时会被阻断”的提醒。
func TestInspectPreviewTurnsMarketSpecificProblemsIntoWarnings(t *testing.T) {
	_, clients := newInspectServer(t, standardRoutes(t, string(sample(t, "fapi_position_dual.json")), nil))
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Blockers) != 0 {
		t.Fatalf("预览不应有和市场有关的阻断项：%+v", result.Blockers)
	}
	if !hasFinding(result.Warnings, exchange.FindingHedgeMode) {
		t.Fatalf("双向持仓应写成提醒：%+v", result.Warnings)
	}
	for _, warning := range result.Warnings {
		if warning.Code == exchange.FindingHedgeMode && !strings.Contains(warning.Message, "开通对应市场时会被阻断") {
			t.Fatalf("提醒要说明开通对应市场时会被阻断：%q", warning.Message)
		}
	}
}

func TestInspectSuspectedPortfolioMarginOldKey(t *testing.T) {
	routes := standardRoutes(t, `{"dualSidePosition": false}`, nil)
	routes["/fapi/v3/account"] = inspectReply{http.StatusUnauthorized, errorBody(-2015, "Invalid API-key, IP, or permissions for action.")}
	_, clients := newInspectServer(t, routes)
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if !result.PortfolioMarginSuspected || !hasFinding(result.Blockers, exchange.FindingSuspectedPortfolioMargin) {
		t.Fatalf("疑似统一账户旧密钥应阻断：%+v", result)
	}
}

func TestInspectFapiRejectedWithoutFuturesPermissionIsSpotOnlyStandard(t *testing.T) {
	routes := standardRoutes(t, `{"dualSidePosition": false}`, func(v map[string]any) { v["enableFutures"] = false })
	routes["/fapi/v3/account"] = inspectReply{http.StatusUnauthorized, errorBody(-2015, "Invalid API-key, IP, or permissions for action.")}
	_, clients := newInspectServer(t, routes)
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if result.PortfolioMarginSuspected || len(result.Blockers) != 0 ||
		!slices.Equal(result.AvailableMarkets, []exchange.MarketType{exchange.MarketTypeSpot}) {
		t.Fatalf("没开合约权限的普通账户只能做现货：%+v", result)
	}
}

func TestInspectTestnetSkipsDetection(t *testing.T) {
	state, clients := newInspectServer(t, nil)
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentTestnet}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || result.AccountMode != exchange.AccountModeBinanceStandard || len(state.requests) != 0 {
		t.Fatalf("测试网应跳过检测并记为普通账户：%+v 请求=%v", result, state.requests)
	}
}

func TestInspectCredentialRejectedIsConclusive(t *testing.T) {
	_, clients := newInspectServer(t, map[string]inspectReply{
		"/sapi/v1/account/apiRestrictions": {http.StatusUnauthorized, errorBody(-2015, "Invalid API-key, IP, or permissions for action.")},
	})
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Blockers, exchange.FindingCredentialRejected) {
		t.Fatalf("密钥被拒绝应是确定结论：%+v", result)
	}
}

// 超时、限频、5xx 和无法识别的错误码都不下结论：返回“没有结论”错误，不返回任何账户类型。
func TestInspectTransportErrorsAreInconclusive(t *testing.T) {
	tests := map[string]inspectReply{
		"限频 HTTP 429":       {http.StatusTooManyRequests, errorBody(-1003, "Too many requests.")},
		"IP 封禁 HTTP 418":    {http.StatusTeapot, errorBody(-1003, "Way too many requests; IP banned.")},
		"服务端 HTTP 503":      {http.StatusServiceUnavailable, `{"msg":"Service Unavailable."}`},
		"超时 -1007":          {http.StatusGatewayTimeout, errorBody(-1007, "Timeout waiting for response from backend server.")},
		"时间戳 -1021":         {http.StatusBadRequest, errorBody(-1021, "Timestamp for this request is outside of the recvWindow.")},
		"无法识别的错误码 -1121":    {http.StatusBadRequest, errorBody(-1121, "Invalid symbol.")},
		"WAF HTTP 403 无错误码": {http.StatusForbidden, "<html>blocked</html>"},
		// 限频、封禁、WAF、5xx 的响应体里即使带着鉴权类错误码（-2015），也不是“密钥被拒绝”：先看状态码和类别，再看错误码。
		"HTTP 429 配 -2015": {http.StatusTooManyRequests, errorBody(-2015, "Invalid API-key, IP, or permissions for action.")},
		"HTTP 418 配 -2015": {http.StatusTeapot, errorBody(-2015, "Invalid API-key, IP, or permissions for action.")},
		"HTTP 403 配 -2015": {http.StatusForbidden, errorBody(-2015, "Invalid API-key, IP, or permissions for action.")},
		"HTTP 500 配 -2015": {http.StatusInternalServerError, errorBody(-2015, "Invalid API-key, IP, or permissions for action.")},
		"HTTP 503 配 -1022": {http.StatusServiceUnavailable, errorBody(-1022, "Signature for this request is not valid.")},
		// HTTP 状态码不能先于响应体里的错误码：401 配网关页、401 配时间戳错误都不是“密钥被拒绝”。
		"HTTP 401 配网关页":       {http.StatusUnauthorized, "<html>401 Authorization Required</html>"},
		"HTTP 401 空响应体":       {http.StatusUnauthorized, ""},
		"HTTP 401 配时间戳 -1021": {http.StatusUnauthorized, errorBody(-1021, "Timestamp for this request is outside of the recvWindow.")},
	}
	for name, reply := range tests {
		t.Run(name+"（papi）", func(t *testing.T) {
			routes := standardRoutes(t, `{"dualSidePosition": false}`, nil)
			routes["/papi/v1/account"] = reply
			_, clients := newInspectServer(t, routes)
			result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
			if err == nil || !errors.Is(err, exchange.ErrInspectionInconclusive) {
				t.Fatalf("应返回没有结论的错误，得到 err=%v result=%+v", err, result)
			}
			if result.AccountMode != exchange.AccountModeUnspecified {
				t.Fatalf("没有结论时不能给出账户类型：%+v", result)
			}
		})
		t.Run(name+"（fapi）", func(t *testing.T) {
			routes := standardRoutes(t, `{"dualSidePosition": false}`, nil)
			routes["/fapi/v3/account"] = reply
			_, clients := newInspectServer(t, routes)
			if _, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients); !errors.Is(err, exchange.ErrInspectionInconclusive) {
				t.Fatalf("fapi 传输错误不能落到“疑似统一账户”：err=%v", err)
			}
		})
	}
}

func TestInspectSignsRequestsAndSendsAPIKey(t *testing.T) {
	var seen []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(r.Context()))
		_, _ = w.Write(sample(t, "api_restrictions.json"))
	}))
	defer server.Close()
	clients := inspectClients{spot: httpclient.New(server.URL), swap: httpclient.New(server.URL), papi: httpclient.New(server.URL)}
	_, _ = inspectWith(context.Background(), exchange.InspectRequest{
		Environment: exchange.AccountEnvironmentProduction, Credential: exchange.Credential{APIKey: "key-1", APISecret: "secret-1"},
	}, clients)
	// 前面的时间请求不签名；第一个签名请求是 apiRestrictions。
	index := slices.IndexFunc(seen, func(r *http.Request) bool { return r.URL.Path == "/sapi/v1/account/apiRestrictions" })
	if index < 0 || seen[index].Header.Get("X-MBX-APIKEY") != "key-1" ||
		!strings.Contains(seen[index].URL.RawQuery, "timestamp=") || !strings.Contains(seen[index].URL.RawQuery, "&signature=") {
		t.Fatalf("请求没有带密钥或签名：%+v", seen)
	}
}

func hasFinding(findings []exchange.InspectionFinding, code string) bool {
	return slices.ContainsFunc(findings, func(f exchange.InspectionFinding) bool { return f.Code == code })
}

// 本机时钟偏差：交易所时间比本机快 4 秒是阻断项，快 2 秒只是提示，没有偏差两者都没有。
func TestInspectClockSkew(t *testing.T) {
	check := func(ahead time.Duration) exchange.Inspection {
		routes := standardRoutes(t, `{"dualSidePosition": false}`, nil)
		serverTime := ok([]byte(fmt.Sprintf(`{"serverTime":%d}`, time.Now().Add(ahead).UnixMilli())))
		routes["/api/v3/time"], routes["/fapi/v1/time"] = serverTime, serverTime
		_, clients := newInspectServer(t, routes)
		result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction}, clients)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	blocked := check(4 * time.Second)
	if !hasFinding(blocked.Blockers, exchange.FindingClockSkew) || blocked.ClockSkewMillis < 3500 || blocked.ClockSkewMillis > 4500 {
		t.Fatalf("偏差 4 秒应阻断：skew=%d blockers=%+v", blocked.ClockSkewMillis, blocked.Blockers)
	}
	warned := check(-2 * time.Second)
	if hasFinding(warned.Blockers, exchange.FindingClockSkew) || !hasFinding(warned.Warnings, exchange.FindingClockSkew) {
		t.Fatalf("偏差 2 秒只提示：blockers=%+v warnings=%+v", warned.Blockers, warned.Warnings)
	}
	clean := check(0)
	if hasFinding(clean.Blockers, exchange.FindingClockSkew) || hasFinding(clean.Warnings, exchange.FindingClockSkew) {
		t.Fatalf("没有偏差不应有提示：%+v", clean)
	}
}

// 合约 listenKey 续期：PUT /fapi/v1/listenKey 只带 X-MBX-APIKEY 请求头，请求里没有 listenKey 参数。
func TestRenewListenKeySendsNoListenKeyParameter(t *testing.T) {
	var method, path, query, apiKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, query, apiKey = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-MBX-APIKEY")
		_, _ = w.Write([]byte(`{"listenKey":"secret-listen-key"}`))
	}))
	t.Cleanup(server.Close)
	adapter := testAdapter(exchange.MarketTypeSwap, server.URL)
	if err := adapter.renewListenKey(context.Background(), "/fapi/v1/listenKey"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/fapi/v1/listenKey" || query != "" || apiKey != "key" {
		t.Fatalf("method=%s path=%s query=%q apiKey=%q", method, path, query, apiKey)
	}
}

// 本机时钟偏快时签名请求全被 -1021 拒绝：签名检测没有结论，但不签名的时间接口能测出偏差，
// 偏差达到阻断阈值就把它作为结论返回（只有时钟阻断项，没有账户相关的误报）；偏差不大时仍是“没有结论”。
func TestInspectClockSkewExplainsSignedRequestsRejected(t *testing.T) {
	check := func(ahead time.Duration) (exchange.Inspection, error) {
		timestampRejected := inspectReply{http.StatusBadRequest, errorBody(-1021, "Timestamp for this request is outside of the recvWindow.")}
		serverTime := ok([]byte(fmt.Sprintf(`{"serverTime":%d}`, time.Now().Add(ahead).UnixMilli())))
		_, clients := newInspectServer(t, map[string]inspectReply{
			"/sapi/v1/account/apiRestrictions": timestampRejected,
			"/api/v3/time":                     serverTime,
			"/fapi/v1/time":                    serverTime,
		})
		return inspectWith(context.Background(), exchange.InspectRequest{
			Environment: exchange.AccountEnvironmentProduction,
			Markets:     []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		}, clients)
	}
	result, err := check(-4 * time.Second)
	if err != nil {
		t.Fatalf("偏差 4 秒应给出结论：%v", err)
	}
	if len(result.Blockers) != 1 || result.Blockers[0].Code != exchange.FindingClockSkew || result.ClockSkewMillis > -3500 {
		t.Fatalf("应只有时钟阻断项：skew=%d blockers=%+v", result.ClockSkewMillis, result.Blockers)
	}
	if _, err := check(0); !errors.Is(err, exchange.ErrInspectionInconclusive) {
		t.Fatalf("没有时钟偏差时签名失败仍然没有结论：err=%v", err)
	}
}

// 往返超过 1 秒的样本按没测到处理；现货慢、合约正常时只看合约的样本。
func TestMeasureClockSkewIgnoresSlowSamples(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := time.Duration(0)
		if r.URL.Path == "/api/v3/time" {
			time.Sleep(exchange.ClockSkewWarn + 100*time.Millisecond)
			offset = 10 * time.Second
		}
		_, _ = fmt.Fprintf(w, `{"serverTime":%d}`, time.Now().Add(offset).UnixMilli())
	}))
	defer server.Close()
	clients := inspectClients{spot: httpclient.New(server.URL), swap: httpclient.New(server.URL), papi: httpclient.New(server.URL)}
	if skew := measureClockSkew(context.Background(), clients); skew < -200 || skew > 200 {
		t.Fatalf("慢样本应被丢弃，只剩合约的零偏差样本：skew=%d", skew)
	}
}
