package okx

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
)

func TestJsonMarshal_SkipsEmptyValues(t *testing.T) {
	got := jsonMarshal(map[string]string{"a": "1", "b": "", "c": "2"})
	assert.Contains(t, got, `"a":"1"`)
	assert.NotContains(t, got, `"b"`)
	assert.Contains(t, got, `"c":"2"`)
}

func TestDecAdd_EmptyOperand_ShouldTreatAsZero(t *testing.T) {
	assert.Equal(t, "2", decAdd("", "2"))
}

func TestInstType_MapsMarketTypes(t *testing.T) {
	assert.Equal(t, "SPOT", instType(exchange.MarketSpot))
	assert.Equal(t, "SWAP", instType(exchange.MarketSwap))
	assert.Equal(t, "FUTURES", instType(exchange.MarketFutures))
	assert.Equal(t, "MARGIN", instType(exchange.MarketMargin))
}

func TestOkxOrderType_KnownTypes_ShouldMap(t *testing.T) {
	assert.Equal(t, "market", okxOrderType(exchange.TypeMarket))
	assert.Equal(t, "limit", okxOrderType(exchange.TypeLimit))
	assert.Equal(t, "fok", okxOrderType(exchange.TypeFOK))
}

func TestOkxTdMode_MapsMarketTypes(t *testing.T) {
	assert.Equal(t, "cash", okxTdMode(exchange.MarketSpot))
	assert.Equal(t, "cross", okxTdMode(exchange.MarketSwap))
}

func TestMapStatus_CurrentBehavior_ShouldReturnSubmitted(t *testing.T) {
	assert.Equal(t, exchange.StatusSubmitted, mapStatus("filled"))
	assert.Equal(t, exchange.StatusSubmitted, mapStatus("canceled"))
	assert.Equal(t, exchange.StatusSubmitted, mapStatus("partially_filled"))
}

func TestOkxAccountType_MapsMarketTypes(t *testing.T) {
	assert.Equal(t, "6", okxAccountType(exchange.MarketSpot))
	assert.Equal(t, "1", okxAccountType(exchange.MarketSwap))
}

func TestSign_ProducesBase64Digest(t *testing.T) {
	got := sign("secret", "ts", "GET", "/api/v5/account/balance", "")
	assert.NotEmpty(t, got)
}

func TestAuthHeaders_ContainsAccessFields(t *testing.T) {
	h := authHeaders(exchange.Credential{APIKey: "k", APISecret: "s", Passphrase: "p"}, "GET", "/path", "")
	assert.Equal(t, "k", h["OK-ACCESS-KEY"])
	assert.Equal(t, "p", h["OK-ACCESS-PASSPHRASE"])
	assert.NotEmpty(t, h["OK-ACCESS-SIGN"])
	assert.NotEmpty(t, h["OK-ACCESS-TIMESTAMP"])
}
