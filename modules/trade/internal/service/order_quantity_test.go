package service

import (
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
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
