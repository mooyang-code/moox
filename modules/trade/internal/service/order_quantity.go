package service

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func normalizePlaceOrderQuantity(req *exchange.PlaceOrderReq, instruments []exchange.Instrument) error {
	if req == nil || req.Quantity == "" || req.Quantity == "0" {
		return nil
	}
	ins, ok := findInstrument(instruments, req.Symbol)
	if !ok || ins.LotSize == "" {
		return nil
	}
	quantity, valid, err := normalizeQuantityToLotSize(req.Quantity, ins.LotSize, ins.MinQty)
	if err != nil {
		return fmt.Errorf("normalize quantity: %w", err)
	}
	if !valid {
		return fmt.Errorf("quantity %s below min qty %s for %s", req.Quantity, ins.MinQty, req.Symbol)
	}
	price := req.Price
	if strings.TrimSpace(price) == "" || strings.TrimSpace(price) == "0" {
		price = ins.LastPrice
	}
	if ok, notional, err := quantityMeetsMinNotional(quantity, price, ins.MinNotional); err != nil {
		return fmt.Errorf("validate min notional: %w", err)
	} else if !ok {
		return fmt.Errorf("quantity %s notional %s below min notional %s for %s", quantity, notional, ins.MinNotional, req.Symbol)
	}
	req.Quantity = quantity
	return nil
}

func findInstrument(instruments []exchange.Instrument, symbol string) (exchange.Instrument, bool) {
	want := symbolKey(symbol)
	for _, ins := range instruments {
		if symbolKey(ins.Symbol) == want {
			return ins, true
		}
	}
	return exchange.Instrument{}, false
}

func symbolKey(symbol string) string {
	replacer := strings.NewReplacer("-", "", "_", "", "/", "")
	return replacer.Replace(strings.ToUpper(strings.TrimSpace(symbol)))
}

func normalizeQuantityToLotSize(quantity, lotSize, minQty string) (string, bool, error) {
	q, err := parsePositiveRat(quantity)
	if err != nil {
		return "", false, err
	}
	if q.Sign() <= 0 {
		return "0", false, nil
	}
	step, err := parsePositiveRat(lotSize)
	if err != nil {
		return "", false, err
	}
	if step.Sign() <= 0 {
		return trimDecimalText(quantity), true, nil
	}

	units := floorRat(new(big.Rat).Quo(q, step))
	if units.Sign() <= 0 {
		return "0", false, nil
	}
	adjusted := new(big.Rat).Mul(new(big.Rat).SetInt(units), step)
	if strings.TrimSpace(minQty) != "" {
		min, err := parsePositiveRat(minQty)
		if err != nil {
			return "", false, err
		}
		if adjusted.Cmp(min) < 0 {
			return ratToDecimal(adjusted, decimalScale(lotSize)), false, nil
		}
	}
	return ratToDecimal(adjusted, decimalScale(lotSize)), true, nil
}

func quantityMeetsMinNotional(quantity, price, minNotional string) (bool, string, error) {
	if strings.TrimSpace(minNotional) == "" || strings.TrimSpace(price) == "" || strings.TrimSpace(price) == "0" {
		return true, "", nil
	}
	q, err := parsePositiveRat(quantity)
	if err != nil {
		return false, "", err
	}
	p, err := parsePositiveRat(price)
	if err != nil {
		return false, "", err
	}
	min, err := parsePositiveRat(minNotional)
	if err != nil {
		return false, "", err
	}
	if q.Sign() <= 0 || p.Sign() <= 0 || min.Sign() <= 0 {
		return true, "", nil
	}
	notional := new(big.Rat).Mul(q, p)
	return notional.Cmp(min) >= 0, ratToDecimal(notional, decimalScale(minNotional)+decimalScale(price)), nil
}

func parsePositiveRat(value string) (*big.Rat, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		raw = "0"
	}
	rat, ok := new(big.Rat).SetString(raw)
	if !ok {
		return nil, fmt.Errorf("invalid decimal %q", value)
	}
	return rat, nil
}

func floorRat(r *big.Rat) *big.Int {
	q := new(big.Int).Quo(r.Num(), r.Denom())
	if r.Sign() < 0 && new(big.Int).Mod(r.Num(), r.Denom()).Sign() != 0 {
		q.Sub(q, big.NewInt(1))
	}
	return q
}

func decimalScale(value string) int {
	raw := strings.TrimSpace(value)
	if idx := strings.IndexByte(raw, '.'); idx >= 0 {
		frac := strings.TrimRight(raw[idx+1:], "0")
		return len(frac)
	}
	return 0
}

func ratToDecimal(value *big.Rat, scale int) string {
	if scale < 0 {
		scale = 0
	}
	return trimDecimalText(value.FloatString(scale))
}

func trimDecimalText(value string) string {
	raw := strings.TrimSpace(value)
	if !strings.Contains(raw, ".") {
		return raw
	}
	raw = strings.TrimRight(raw, "0")
	raw = strings.TrimRight(raw, ".")
	if raw == "" || raw == "-" {
		return "0"
	}
	return raw
}
