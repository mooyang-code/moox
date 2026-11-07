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
	result, err := inspectAgainst(t, http.StatusOK, string(readSample(t, "account_config.json")), exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction})
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
	// 官方：HTTP 200 里 code 非 0 表示失败；50111 是密钥无效。
	body := `{"code":"50111","msg":"Invalid OK-ACCESS-KEY","data":[]}`
	result, err := inspectAgainst(t, http.StatusOK, body, exchange.InspectRequest{Environment: exchange.AccountEnvironmentProduction})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Blockers, exchange.FindingCredentialRejected) {
		t.Fatalf("鉴权类错误码应是确定结论：%+v", result)
	}
}

func TestInspectTransportErrorsAreInconclusive(t *testing.T) {
	tests := map[string]struct {
		status int
		body   string
	}{
		"限频 HTTP 429": {http.StatusTooManyRequests, `{"code":"50011","msg":"Too Many Requests","data":[]}`},
		"服务端 5xx":     {http.StatusBadGateway, `bad gateway`},
		"系统繁忙 50001":  {http.StatusOK, `{"code":"50001","msg":"Service temporarily unavailable","data":[]}`},
		"超时 50004":    {http.StatusOK, `{"code":"50004","msg":"Endpoint request timeout","data":[]}`},
		"无法识别的错误码":    {http.StatusOK, `{"code":"59999","msg":"unknown","data":[]}`},
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
