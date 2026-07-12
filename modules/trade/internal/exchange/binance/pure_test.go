package binance

import (
	"net/url"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
)

func TestDecAddAndSub_ShouldComputeDecimalStrings(t *testing.T) {
	assert.Equal(t, "3", decAdd("1", "2"))
	assert.Equal(t, "0", decSub("2", "2"))
	assert.Equal(t, "5", decAdd("", "5"))
}

func TestNormStr_Empty_ShouldReturnZero(t *testing.T) {
	assert.Equal(t, "0", normStr(""))
	assert.Equal(t, "1.5", normStr("1.5"))
}

func TestHmacSha256_DeterministicDigest(t *testing.T) {
	a := hmacSha256([]byte("secret"), []byte("payload"))
	b := hmacSha256([]byte("secret"), []byte("payload"))
	assert.Equal(t, a, b)
	assert.NotEmpty(t, a)
}

func TestSign_ProducesHexDigest(t *testing.T) {
	got := sign("secret", "symbol=BTCUSDT")
	assert.Len(t, got, 64)
}

func TestSignedQuery_AddsTimestampAndSignature(t *testing.T) {
	cred := exchange.Credential{APISecret: "secret"}
	q := signedQuery(cred, url.Values{"symbol": {"BTCUSDT"}})
	assert.NotEmpty(t, q.Get("timestamp"))
	assert.NotEmpty(t, q.Get("recvWindow"))
	assert.NotEmpty(t, q.Get("signature"))
	assert.Equal(t, "BTCUSDT", q.Get("symbol"))
}

func TestApiHeader_ContainsAPIKey(t *testing.T) {
	h := apiHeader(exchange.Credential{APIKey: "key-1"})
	assert.Equal(t, "key-1", h["X-MBX-APIKEY"])
}

func TestMarketPath_SelectsSwapOrSpot(t *testing.T) {
	assert.Equal(t, "/fapi/v1/order", marketPath(exchange.MarketSwap, "/api/v3/order", "/fapi/v1/order"))
	assert.Equal(t, "/api/v3/order", marketPath(exchange.MarketSpot, "/api/v3/order", "/fapi/v1/order"))
}

func TestMapStatus_KnownStatuses_ShouldMap(t *testing.T) {
	assert.Equal(t, exchange.StatusFilled, mapStatus("FILLED"))
	assert.Equal(t, exchange.StatusCanceled, mapStatus("CANCELED"))
	assert.Equal(t, exchange.StatusRejected, mapStatus("REJECTED"))
	assert.Equal(t, exchange.StatusSubmitted, mapStatus("UNKNOWN"))
}

func TestBinanceOrderType_KnownTypes_ShouldMap(t *testing.T) {
	assert.Equal(t, "MARKET", binanceOrderType(exchange.TypeMarket))
	assert.Equal(t, "LIMIT_MAKER", binanceOrderType(exchange.TypePostOnly))
	assert.Equal(t, "LIMIT", binanceOrderType(exchange.TypeLimit))
}

func TestBinanceTransferType_SpotToSwap_ShouldMap(t *testing.T) {
	assert.Equal(t, "MAIN_UMFUTURE", binanceTransferType(exchange.MarketSpot, exchange.MarketSwap))
	assert.Equal(t, "UMFUTURE_MAIN", binanceTransferType(exchange.MarketSwap, exchange.MarketSpot))
}

func TestAdapter_EnsureCache_UsesGlobalCache(t *testing.T) {
	a := &Adapter{}
	assert.Equal(t, globalInsCache, a.ensureCache())
}
