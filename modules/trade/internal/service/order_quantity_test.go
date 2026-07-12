package service

import (
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeQuantityToLotSizeFloorsToStep(t *testing.T) {
	got, ok, err := normalizeQuantityToLotSize("9.35663400", "0.00100000", "0.00100000")
	if err != nil {
		t.Fatalf("normalizeQuantityToLotSize returned error: %v", err)
	}
	if !ok {
		t.Fatalf("normalizeQuantityToLotSize returned ok=false")
	}
	if got != "9.356" {
		t.Fatalf("quantity = %q, want %q", got, "9.356")
	}
}

func TestNormalizeQuantityToLotSizeHandlesIntegerStep(t *testing.T) {
	got, ok, err := normalizeQuantityToLotSize("21627666.80", "1.00000000", "1.00000000")
	if err != nil {
		t.Fatalf("normalizeQuantityToLotSize returned error: %v", err)
	}
	if !ok {
		t.Fatalf("normalizeQuantityToLotSize returned ok=false")
	}
	if got != "21627666" {
		t.Fatalf("quantity = %q, want %q", got, "21627666")
	}
}

func TestNormalizeQuantityToLotSizeRejectsDustBelowMinQty(t *testing.T) {
	got, ok, err := normalizeQuantityToLotSize("0.0007", "0.00100000", "0.00100000")
	if err != nil {
		t.Fatalf("normalizeQuantityToLotSize returned error: %v", err)
	}
	if ok {
		t.Fatalf("normalizeQuantityToLotSize returned ok=true, quantity=%q", got)
	}
}

func TestNormalizePlaceOrderQuantityRejectsBelowMinNotional(t *testing.T) {
	req := &exchange.PlaceOrderReq{
		Symbol:   "GALAUSDT",
		Side:     exchange.SideSell,
		Type:     exchange.TypeMarket,
		Quantity: "1225.77300000",
	}
	err := normalizePlaceOrderQuantity(req, []exchange.Instrument{{
		Symbol:      "GALAUSDT",
		LotSize:     "1.00000000",
		MinQty:      "1.00000000",
		MinNotional: "5.00000000",
		LastPrice:   "0.00233400",
	}})
	if err == nil {
		t.Fatalf("normalizePlaceOrderQuantity returned nil error")
	}
	if !strings.Contains(err.Error(), "below min notional") {
		t.Fatalf("error = %q, want below min notional", err.Error())
	}
}

func TestNormalizePlaceOrderQuantity_NoQuantityOrInstrument_ShouldNoop(t *testing.T) {
	req := &exchange.PlaceOrderReq{Symbol: "BTCUSDT", Quantity: ""}
	require.NoError(t, normalizePlaceOrderQuantity(req, nil))
	assert.Equal(t, "", req.Quantity)

	req = &exchange.PlaceOrderReq{Symbol: "BTCUSDT", Quantity: "1"}
	require.NoError(t, normalizePlaceOrderQuantity(req, []exchange.Instrument{{Symbol: "ETHUSDT", LotSize: "0.1"}}))
	assert.Equal(t, "1", req.Quantity)

	req = &exchange.PlaceOrderReq{Symbol: "BTCUSDT", Quantity: "1"}
	require.NoError(t, normalizePlaceOrderQuantity(req, []exchange.Instrument{{Symbol: "BTCUSDT"}}))
	assert.Equal(t, "1", req.Quantity)
}

func TestNormalizePlaceOrderQuantity_ValidRules_ShouldFloorQuantity(t *testing.T) {
	req := &exchange.PlaceOrderReq{Symbol: "btc-usdt", Quantity: "1.234", Price: "10"}
	err := normalizePlaceOrderQuantity(req, []exchange.Instrument{{
		Symbol: "BTC/USDT", LotSize: "0.01", MinQty: "0.01", MinNotional: "10",
	}})
	require.NoError(t, err)
	assert.Equal(t, "1.23", req.Quantity)
}

func TestNormalizePlaceOrderQuantity_InvalidRuleValues_ShouldReturnError(t *testing.T) {
	req := &exchange.PlaceOrderReq{Symbol: "BTCUSDT", Quantity: "1"}
	err := normalizePlaceOrderQuantity(req, []exchange.Instrument{{Symbol: "BTCUSDT", LotSize: "bad"}})
	assert.Error(t, err)

	req = &exchange.PlaceOrderReq{Symbol: "BTCUSDT", Quantity: "1", Price: "bad"}
	err = normalizePlaceOrderQuantity(req, []exchange.Instrument{{Symbol: "BTCUSDT", LotSize: "1", MinNotional: "10"}})
	assert.Error(t, err)
}

func TestQuantityHelpers_EdgeCases_ShouldReturnExpectedValues(t *testing.T) {
	assert.Equal(t, "BTCUSDT", symbolKey(" btc-usdt "))

	ins, ok := findInstrument([]exchange.Instrument{{Symbol: "ETH_USDT"}}, "eth/usdt")
	assert.True(t, ok)
	assert.Equal(t, "ETH_USDT", ins.Symbol)

	_, ok = findInstrument([]exchange.Instrument{{Symbol: "ETH_USDT"}}, "BTCUSDT")
	assert.False(t, ok)

	got, valid, err := normalizeQuantityToLotSize("1.23", "0", "")
	require.NoError(t, err)
	assert.True(t, valid)
	assert.Equal(t, "1.23", got)

	got, valid, err = normalizeQuantityToLotSize("-1", "0.1", "")
	require.NoError(t, err)
	assert.False(t, valid)
	assert.Equal(t, "0", got)

	ok, notional, err := quantityMeetsMinNotional("1", "", "10")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, notional)

	ok, notional, err = quantityMeetsMinNotional("2", "5", "10")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "10", notional)

	assert.Equal(t, 2, decimalScale("0.0100"))
	assert.Equal(t, 0, decimalScale("10"))
	assert.Equal(t, "-", trimDecimalText("-"))
	assert.Equal(t, "1.23", trimDecimalText(" 1.2300 "))
}
