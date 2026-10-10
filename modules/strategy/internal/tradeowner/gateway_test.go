package tradeowner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 网关响应多出当前协议没有的字段（Trade 先于 Strategy 升级）时照常解析；响应无法解析时按“结果未知”处理，说明写“响应无法解析”，
// 而不是“不可达”，原始的解析错误留在 Unwrap 里。
func TestGatewayDecodesUnknownFieldsAndReportsUnreadableResponses(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_CALLER", "strategy")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-key")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-secret")
	body := `{"ret_info":{"code":0,"msg":"ok"},"logical_account":{"logical_account_id":"acc","space_id":"space","owner_instance_id":"i1","owner_session_id":"s1"},"field_from_a_newer_trade":"x"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	owner := New(Config{GatewayURL: server.URL, TargetNode: "node-a", Timeout: 3 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := owner.ValidateSession(ctx, "space", "acc", "i1", "s1"); err != nil {
		t.Fatalf("响应多出未知字段时应照常解析：%v", err)
	}
	body = `<html>bad gateway</html>`
	err := owner.ValidateSession(ctx, "space", "acc", "i1", "s1")
	var transport *TransportError
	if !errors.As(err, &transport) || !transport.Unreadable {
		t.Fatalf("无法解析的响应应按传输失败（结果未知）处理：%v", err)
	}
	if !strings.Contains(err.Error(), "Trade 网关响应无法解析") || strings.Contains(err.Error(), "不可达") || errors.Unwrap(err) == nil {
		t.Fatalf("说明应写响应无法解析、不能说不可达，原文留在 Unwrap 里：%v", err)
	}
	// 未知的错误码不能被读成 SUCCESS（DiscardUnknown 会把未知枚举值丢成零值）。
	body = `{"ret_info":{"code":"SOME_NEW_ERROR","msg":"boom"},"logical_account":{"logical_account_id":"acc","space_id":"space","owner_instance_id":"i1","owner_session_id":"s1"}}`
	if err := owner.ValidateSession(ctx, "space", "acc", "i1", "s1"); !errors.As(err, &transport) || !transport.Unreadable {
		t.Fatalf("未知的错误码应按无法解析处理：%v", err)
	}
}
