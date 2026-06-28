package binance

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSign_HMACSHA256Hex(t *testing.T) {
	// RFC 4231 Test Case 2: key="Jefe", data="what do ya want for nothing?"
	got := sign("Jefe", "what do ya want for nothing?")
	require.Equal(t, "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843", got)
}

func TestSignedQuery_AppendsTimestampAndSignature(t *testing.T) {
	cred := credFixture()
	base := url.Values{}
	base.Set("symbol", "BTCUSDT")
	base.Set("side", "BUY")
	q := signedQuery(cred, base)

	require.NotEmpty(t, q.Get("timestamp"))
	require.Equal(t, "5000", q.Get("recvWindow"))
	require.NotEmpty(t, q.Get("signature"))

	// 重算签名应与 query 中一致（去掉 signature 后 encode）
	q2 := url.Values{}
	for k, v := range q {
		if k == "signature" {
			continue
		}
		q2[k] = v
	}
	require.Equal(t, sign(cred.APISecret, q2.Encode()), q.Get("signature"))
}

func TestBinanceOrderTypeMapping(t *testing.T) {
	cases := map[string]string{
		"market":    "MARKET",
		"limit":     "LIMIT",
		"post_only": "LIMIT_MAKER",
		"ioc":       "LIMIT",
		"fok":       "LIMIT",
		"stop_limit": "STOP_LOSS_LIMIT",
	}
	for in, want := range cases {
		require.Equal(t, want, binanceOrderType(orderTypeOf(in)), in)
	}
}

func TestMapStatus(t *testing.T) {
	require.Equal(t, 3, int(mapStatus("FILLED")))
	require.Equal(t, 4, int(mapStatus("CANCELED")))
	require.Equal(t, 6, int(mapStatus("REJECTED")))
	require.Equal(t, 2, int(mapStatus("PARTIALLY_FILLED")))
}
