package dao

import (
	"fmt"
	"math/big"
)

// decimalPrec 是 big.Float 的运算精度（足够承载加密货币精度）。
const decimalPrec = 256

// addDec 返回 a + b 的字符串（decimal）。空串视为 "0"。
func addDec(a, b string) (string, error) {
	af, _, err := big.ParseFloat(normDec(a), 10, decimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", a, err)
	}
	bf, _, err := big.ParseFloat(normDec(b), 10, decimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", b, err)
	}
	r := new(big.Float).SetPrec(decimalPrec).Add(af, bf)
	return r.Text('f', -1), nil
}

// subDec 返回 a - b 的字符串。
func subDec(a, b string) (string, error) {
	af, _, err := big.ParseFloat(normDec(a), 10, decimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", a, err)
	}
	bf, _, err := big.ParseFloat(normDec(b), 10, decimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", b, err)
	}
	r := new(big.Float).SetPrec(decimalPrec).Sub(af, bf)
	return r.Text('f', -1), nil
}

// applyDirection 按 direction(1/-1) 把 amount 叠加到 base：base + direction*amount。
func applyDirection(base, amount string, direction int) (string, error) {
	if direction == 0 {
		return base, nil
	}
	if direction > 0 {
		return addDec(base, amount)
	}
	return subDec(base, amount)
}

func normDec(s string) string {
	if s == "" {
		return "0"
	}
	return s
}
