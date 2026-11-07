package okx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

// 检测测试以官方账户配置样例（testdata/account_config.json）为底，按需改 acctLv、perm、posMode；
// 官方样例本身同时带提现权限和双向持仓，正好覆盖这两个阻断项。

func readSample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// configReply 取官方样例并改字段。
func configReply(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(readSample(t, "account_config.json"), &envelope); err != nil {
		t.Fatal(err)
	}
	row := envelope["data"].([]any)[0].(map[string]any)
	if mutate != nil {
		mutate(row)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func inspectAgainst(t *testing.T, status int, body string, request exchange.InspectRequest) (exchange.Inspection, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/config" {
			t.Errorf("意外的路径 %s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	if request.Credential.Passphrase == "" {
		request.Credential = exchange.Credential{APIKey: "key", APISecret: "secret", Passphrase: "phrase"}
	}
	adapter := New(exchange.AccountConfig{Exchange: exchange.ExchangeOKX, Environment: request.Environment}, request.Credential)
	adapter.client = httpclient.New(server.URL)
	return inspectWith(context.Background(), request, adapter)
}

func hasFinding(findings []exchange.InspectionFinding, code string) bool {
	return slices.ContainsFunc(findings, func(f exchange.InspectionFinding) bool { return f.Code == code })
}

func TestInspectOfficialSampleHasWithdrawPermissionAndHedgeMode(t *testing.T) {
	result, err := inspectAgainst(t, http.StatusOK, string(readSample(t, "account_config.json")), exchange.InspectRequest{
		Environment: exchange.AccountEnvironmentProduction, Markets: []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.CanWithdraw || result.PositionMode != exchange.PositionModeHedge || result.OKXAcctLv != "2" ||
		result.AccountMode != exchange.AccountModeOKXUnified {
		t.Fatalf("result = %+v", result)
	}
	if !hasFinding(result.Blockers, exchange.FindingWithdrawPermission) || !hasFinding(result.Blockers, exchange.FindingHedgeMode) {
		t.Fatalf("官方样例应同时阻断提现权限和双向持仓：%+v", result.Blockers)
	}
	// 预览（还没选市场）只给和市场无关的阻断项：提现权限照样阻断，双向持仓要等选了永续才判。
	preview := result.BlockersFor(nil)
	if !hasFinding(preview, exchange.FindingWithdrawPermission) || hasFinding(preview, exchange.FindingHedgeMode) {
		t.Fatalf("预览只拦和市场无关的阻断项：%+v", preview)
	}
}

// 预览（不带市场）：双向持仓不是阻断项，写成提醒；提现权限这类和市场无关的照样阻断。
func TestInspectPreviewTurnsHedgeModeIntoWarning(t *testing.T) {
	result, err := inspectAgainst(t, http.StatusOK, string(readSample(t, "account_config.json")), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Blockers, exchange.FindingWithdrawPermission) || hasFinding(result.Blockers, exchange.FindingHedgeMode) {
		t.Fatalf("预览只拦和市场无关的阻断项：%+v", result.Blockers)
	}
	if !hasFinding(result.Warnings, exchange.FindingHedgeMode) {
		t.Fatalf("双向持仓应写成提醒：%+v", result.Warnings)
	}
}

// OKX 现货模式（acctLv=1）有交易权限：SWAP 不在可开通市场里，预览（不带市场）仍要有“开通永续时会被阻断”的提醒。
func TestInspectPreviewWarnsAboutSpotModeAccountOpeningSwap(t *testing.T) {
	body := configReply(t, func(row map[string]any) {
		row["perm"] = "read_only,trade"
		row["posMode"] = "net_mode"
		row["acctLv"] = "1"
	})
	result, err := inspectAgainst(t, http.StatusOK, body, exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Blockers) != 0 {
		t.Fatalf("预览不应有和市场有关的阻断项：%+v", result.Blockers)
	}
	if !hasFinding(result.Warnings, exchange.FindingSpotModeWithSwap) {
		t.Fatalf("现货模式开永续应写成提醒：%+v", result.Warnings)
	}
	// 没有交易权限同样提醒（SWAP 不在可开通市场里）。
	readOnly := configReply(t, func(row map[string]any) { row["perm"] = "read_only"; row["posMode"] = "net_mode"; row["acctLv"] = "2" })
	result, err = inspectAgainst(t, http.StatusOK, readOnly, exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Warnings, exchange.FindingMarketNotPermitted) {
		t.Fatalf("没有合约交易权限应写成提醒：%+v", result.Warnings)
	}
}

func TestInspectUnifiedAccountPasses(t *testing.T) {
	for _, level := range []string{"2", "3", "4"} {
		replaced := configReply(t, func(row map[string]any) {
			row["perm"] = "read_only,trade"
			row["posMode"] = "net_mode"
			row["acctLv"] = level
		})
		result, err := inspectAgainst(t, http.StatusOK, replaced, exchange.InspectRequest{
			Environment: exchange.AccountEnvironmentProduction, Markets: []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		})
		if err != nil || len(result.Blockers) != 0 || !slices.Equal(result.AvailableMarkets, []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}) {
			t.Fatalf("acctLv=%s result=%+v err=%v", level, result, err)
		}
	}
}

func TestInspectSpotModeAccountBlocksSwapOnly(t *testing.T) {
	body := configReply(t, func(row map[string]any) {
		row["perm"] = "read_only,trade"
		row["posMode"] = "net_mode"
		row["acctLv"] = "1"
	})
	result, err := inspectAgainst(t, http.StatusOK, body, exchange.InspectRequest{
		Environment: exchange.AccountEnvironmentProduction, Markets: []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Blockers, exchange.FindingSpotModeWithSwap) {
		t.Fatalf("acctLv=1 却开通 SWAP 应阻断：%+v", result.Blockers)
	}
	if findings := result.BlockersFor([]exchange.MarketType{exchange.MarketTypeSpot}); len(findings) != 0 {
		t.Fatalf("现货模式只做 SPOT 不应阻断：%+v", findings)
	}
	if !slices.Equal(result.AvailableMarkets, []exchange.MarketType{exchange.MarketTypeSpot}) {
		t.Fatalf("现货模式只能开通 SPOT：%v", result.AvailableMarkets)
	}
}

func TestInspectRejectedCredentialIsConclusive(t *testing.T) {
	// OKX 的鉴权类错误常带 HTTP 401（也可能是 HTTP 200 里 code 非 0）：50110、50111、50113 都是确定结论。
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		for _, code := range []string{"50100", "50110", "50111", "50113", "50114"} {
			body := `{"code":"` + code + `","msg":"auth","data":[]}`
			result, err := inspectAgainst(t, status, body, exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction})
			if err != nil {
				t.Fatalf("HTTP %d 错误码 %s 应是确定结论：%v", status, code, err)
			}
			if !hasFinding(result.Blockers, exchange.FindingCredentialRejected) {
				t.Fatalf("HTTP %d 错误码 %s：%+v", status, code, result)
			}
			if strings.Contains(result.CredentialRejected, "auth") || !strings.Contains(result.CredentialRejected, code) {
				t.Fatalf("只记错误码和中文说明，不带响应体原文：%q", result.CredentialRejected)
			}
		}
	}
}

func TestInspectTransportErrorsAreInconclusive(t *testing.T) {
	tests := map[string]struct {
		status int
		body   string
	}{
		"限频 HTTP 429":          {http.StatusTooManyRequests, `{"code":"50011","msg":"Too Many Requests","data":[]}`},
		"服务端 5xx":              {http.StatusBadGateway, `bad gateway`},
		"系统繁忙 50001":           {http.StatusOK, `{"code":"50001","msg":"Service temporarily unavailable","data":[]}`},
		"超时 50004":             {http.StatusOK, `{"code":"50004","msg":"Endpoint request timeout","data":[]}`},
		"无法识别的错误码":             {http.StatusOK, `{"code":"59999","msg":"unknown","data":[]}`},
		"时间戳相关 50112 HTTP 200": {http.StatusOK, `{"code":"50112","msg":"Invalid OK-ACCESS-TIMESTAMP","data":[]}`},
		"时间戳过期 50102 HTTP 200": {http.StatusOK, `{"code":"50102","msg":"Timestamp request expired","data":[]}`},
		// 时间戳类错误在 OKX 上常以 HTTP 401 返回：不能因为状态码是 401 就判成密钥被拒绝。
		"时间戳过期 50102 HTTP 401": {http.StatusUnauthorized, `{"code":"50102","msg":"Timestamp request expired","data":[]}`},
		"时间戳相关 50112 HTTP 401": {http.StatusUnauthorized, `{"code":"50112","msg":"Invalid OK-ACCESS-TIMESTAMP","data":[]}`},
		"HTTP 401 配 HTML 网关页":  {http.StatusUnauthorized, `<html><body>401 Authorization Required</body></html>`},
		"HTTP 401 空响应体":        {http.StatusUnauthorized, ``},
		"HTTP 403 WAF 页":       {http.StatusForbidden, `<html>blocked</html>`},
		// 5xx 响应体里带着鉴权类错误码（网关、代理改写）也不是“密钥被拒绝”。
		"HTTP 500 配鉴权码 50111": {http.StatusInternalServerError, `{"code":"50111","msg":"Invalid OK-ACCESS-KEY","data":[]}`},
		"HTTP 502 配鉴权码 50110": {http.StatusBadGateway, `{"code":"50110","msg":"IP whitelist","data":[]}`},
		"HTTP 429 配鉴权码 50111": {http.StatusTooManyRequests, `{"code":"50111","msg":"Invalid OK-ACCESS-KEY","data":[]}`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := inspectAgainst(t, test.status, test.body, exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction})
			if !errors.Is(err, exchange.ErrInspectionInconclusive) {
				t.Fatalf("应没有结论：err=%v result=%+v", err, result)
			}
		})
	}
}

// 交易路径同样按响应体里的错误码分类：401 配 50102 是“结果未知”，不是鉴权失败。
func TestClassifyUsesOKXErrorCodeFromNon2xxBody(t *testing.T) {
	httpErr := func(status int) error {
		return &exchange.Error{Kind: exchange.ErrorAuthentication, HTTPStatus: status, Err: errors.New("http 401")}
	}
	err := classify(httpErr(401), []byte(`{"code":"50102","msg":"Timestamp request expired"}`))
	if !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
		t.Fatalf("50102 应是结果未知：%v", err)
	}
	err = classify(httpErr(401), []byte(`{"code":"50111","msg":"Invalid OK-ACCESS-KEY"}`))
	var typed *exchange.Error
	if !errors.As(err, &typed) || typed.Kind != exchange.ErrorAuthentication || typed.Code != "50111" || typed.HTTPStatus != 401 {
		t.Fatalf("50111 应是鉴权失败并保留码和状态：%+v", typed)
	}
	if err := classify(httpErr(401), []byte(`<html>`)); !exchange.IsKind(err, exchange.ErrorAuthentication) {
		t.Fatalf("没有错误码时保持状态码的分类：%v", err)
	}
	limited := &exchange.Error{Kind: exchange.ErrorRateLimited, HTTPStatus: 429, Err: errors.New("429")}
	if err := classify(limited, []byte(`{"code":"50011","msg":"Too Many Requests"}`)); !exchange.IsKind(err, exchange.ErrorRateLimited) {
		t.Fatalf("限频以状态码分类为准：%v", err)
	}
	// 5xx 配鉴权类错误码同样保持结果未知（只保留码），不能当成确定的鉴权失败。
	serverAuth := &exchange.Error{Kind: exchange.ErrorTransportUnknown, HTTPStatus: 500, Err: errors.New("500")}
	err = classify(serverAuth, []byte(`{"code":"50111","msg":"Invalid OK-ACCESS-KEY"}`))
	if !errors.As(err, &typed) || typed.Kind != exchange.ErrorTransportUnknown || typed.Code != "50111" {
		t.Fatalf("5xx 配鉴权码应保持结果未知并保留码：%+v", typed)
	}
	bad := &exchange.Error{Kind: exchange.ErrorTransportUnknown, HTTPStatus: 502, Err: errors.New("502")}
	if err := classify(bad, []byte(`{"code":"59999","msg":"x"}`)); !exchange.IsKind(err, exchange.ErrorTransportUnknown) {
		t.Fatalf("5xx 配不明确的错误码保持结果未知：%v", err)
	}
}

func TestInspectMissingPassphraseIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("缺 passphrase 不应发请求") }))
	defer server.Close()
	adapter := New(exchange.AccountConfig{Exchange: exchange.ExchangeOKX}, exchange.Credential{APIKey: "k", APISecret: "s"})
	adapter.client = httpclient.New(server.URL)
	result, err := inspectWith(context.Background(), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction, Credential: exchange.Credential{APIKey: "k", APISecret: "s"}}, adapter)
	if err != nil || !hasFinding(result.Blockers, exchange.FindingCredentialRejected) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInspectSimulatedTradingSendsDemoHeader(t *testing.T) {
	var header string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("x-simulated-trading")
		_, _ = w.Write(readSample(t, "account_config.json"))
	}))
	defer server.Close()
	request := exchange.InspectRequest{Environment: exchange.AccountEnvironmentTestnet, Credential: exchange.Credential{APIKey: "k", APISecret: "s", Passphrase: "p"}}
	adapter := New(exchange.AccountConfig{Exchange: exchange.ExchangeOKX, Environment: request.Environment}, request.Credential)
	adapter.client = httpclient.New(server.URL)
	if _, err := inspectWith(context.Background(), request, adapter); err != nil || header != "1" {
		t.Fatalf("模拟盘照常检测并带模拟盘请求头：header=%q err=%v", header, err)
	}
}
