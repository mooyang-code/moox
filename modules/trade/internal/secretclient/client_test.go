package secretclient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/codec"
)

// fakeGateway 记录转发参数并返回预设的 JSON 响应。
type fakeGateway struct {
	calls         int
	servicePath   string
	method        string
	serialization int
	body          []byte
	response      string
	err           error
}

func (f *fakeGateway) Forward(_ context.Context, servicePath, method string, serialization int, body []byte, _ ...gatewayclient.CallOption) ([]byte, error) {
	f.calls++
	f.servicePath, f.method, f.serialization, f.body = servicePath, method, serialization, body
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.response), nil
}

// 响应体与 tRPC JSON 序列化的输出一致：字段用 proto 名，枚举输出数值，未赋值的字段也输出。
const activeSecretResponse = `{
	"ret_info": {"code": 0, "msg": ""},
	"secret": {
		"secret_id": "sec_1", "name": "Binance", "description": "", "category": "exchange",
		"pro` + `vider": "binance", "secret_type": "api_key", "key_id": "api-key",
		"secret_value": "plain-secret", "extra_config": "{\"market_type\":\"swap\"}",
		"status": "active", "created_at": "0", "updated_at": "0"
	}
}`

func TestGetExchangeSecretForwardsJSONToSecretMgr(t *testing.T) {
	gateway := &fakeGateway{response: activeSecretResponse}
	secret, err := New(gateway, time.Second).GetExchangeSecret(context.Background(), "sec_1")
	if err != nil {
		t.Fatalf("GetExchangeSecret() error = %v", err)
	}
	if gateway.calls != 1 || gateway.servicePath != "trpc.moox.ops.SecretMgr" ||
		gateway.method != "GetSecretValue" || gateway.serialization != codec.SerializationTypeJSON {
		t.Fatalf("forward = %d %s/%s serialization %d", gateway.calls, gateway.servicePath, gateway.method, gateway.serialization)
	}
	var body getSecretValueReq
	if err := json.Unmarshal(gateway.body, &body); err != nil || body.SecretID != "sec_1" {
		t.Fatalf("request body = %s, err = %v", gateway.body, err)
	}
	if secret.SecretValue != "plain-secret" ||
		secret.KeyID != "api-key" ||
		secret.Exchange != exchange.ExchangeBinance ||
		secret.ExtraConfig != `{"market_type":"swap"}` {
		t.Fatalf("secret = %+v", secret)
	}
}

func TestGetExchangeSecretRejectsInvalidInputAndMetadata(t *testing.T) {
	gateway := &fakeGateway{}
	if _, err := New(gateway, 0).GetExchangeSecret(context.Background(), " "); !errors.Is(err, account.ErrInvalidCredential) {
		t.Fatalf("empty secret ID error = %v", err)
	}
	if gateway.calls != 0 {
		t.Fatalf("空的 secret ID 不应发出请求")
	}

	gateway.response = `{"ret_info": {"code": 0}, "secret": {"secret_id": "different", "category": "cloud",
		"pro` + `vider": "binance", "status": "active", "key_id": "key", "secret_value": "value"}}`
	if _, err := New(gateway, 0).GetExchangeSecret(context.Background(), "secret-1"); !errors.Is(err, account.ErrInvalidCredential) {
		t.Fatalf("metadata error = %v, want ErrInvalidCredential", err)
	}
}

func TestGetExchangeSecretReportsFailures(t *testing.T) {
	for name, gateway := range map[string]*fakeGateway{
		"业务错误":      {response: `{"ret_info": {"code": 404, "msg": "秘钥不存在"}, "secret": null}`},
		"缺少返回码":     {response: `{"ret_info": null, "secret": null}`},
		"响应不是 JSON": {response: `not json`},
		"网关拒绝":      {err: errors.New("调用方 trade 不能调用该方法")},
	} {
		if _, err := New(gateway, 0).GetExchangeSecret(context.Background(), "sec_1"); err == nil || errors.Is(err, account.ErrInvalidCredential) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
	if _, err := New(nil, 0).GetExchangeSecret(context.Background(), "sec_1"); err == nil {
		t.Fatal("未配置 gatewayclient 的客户端应当报错")
	}
}
