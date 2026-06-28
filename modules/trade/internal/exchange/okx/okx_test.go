package okx

import (
	"encoding/base64"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func credFixture() exchange.Credential {
	return exchange.Credential{APIKey: "okx-key", APISecret: "okx-secret", Passphrase: "okx-pass"}
}

// TestSign_Base64 验证签名为 base64(HMAC-SHA256(secret, ts+method+path+body))。
func TestSign_Base64(t *testing.T) {
	cred := credFixture()
	sig := sign(cred.APISecret, "2026-06-28T00:00:00.000Z", "GET", "/api/v5/account/balance", "")
	// 不为空且可解码为 base64
	require.NotEmpty(t, sig)
	dec, err := base64.StdEncoding.DecodeString(sig)
	require.NoError(t, err)
	// HMAC-SHA256 输出 32 字节
	require.Len(t, dec, 32)
}

func TestAuthHeaders(t *testing.T) {
	cred := credFixture()
	h := authHeaders(cred, "GET", "/api/v5/account/balance", "")
	require.Equal(t, cred.APIKey, h["OK-ACCESS-KEY"])
	require.Equal(t, cred.Passphrase, h["OK-ACCESS-PASSPHRASE"])
	require.NotEmpty(t, h["OK-ACCESS-SIGN"])
	require.NotEmpty(t, h["OK-ACCESS-TIMESTAMP"])
}

func TestOkxOrderTypeMapping(t *testing.T) {
	require.Equal(t, "market", okxOrderType(exchange.TypeMarket))
	require.Equal(t, "limit", okxOrderType(exchange.TypeLimit))
	require.Equal(t, "post_only", okxOrderType(exchange.TypePostOnly))
	require.Equal(t, "ioc", okxOrderType(exchange.TypeIOC))
	require.Equal(t, "fok", okxOrderType(exchange.TypeFOK))
}

func TestInstTypeMapping(t *testing.T) {
	require.Equal(t, "SPOT", instType(exchange.MarketSpot))
	require.Equal(t, "SWAP", instType(exchange.MarketSwap))
	require.Equal(t, "FUTURES", instType(exchange.MarketFutures))
	require.Equal(t, "MARGIN", instType(exchange.MarketMargin))
}
