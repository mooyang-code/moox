package service

import (
	"fmt"
	"math/big"
)

// svcDecimalPrec big.Float 运算精度，足够承载加密货币精度。
const svcDecimalPrec = 256

func normSvcDec(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func addSvc(a, b string) (string, error) {
	af, _, err := big.ParseFloat(normSvcDec(a), 10, svcDecimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", a, err)
	}
	bf, _, err := big.ParseFloat(normSvcDec(b), 10, svcDecimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", b, err)
	}
	return new(big.Float).SetPrec(svcDecimalPrec).Add(af, bf).Text('f', -1), nil
}

func subSvc(a, b string) (string, error) {
	af, _, err := big.ParseFloat(normSvcDec(a), 10, svcDecimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", a, err)
	}
	bf, _, err := big.ParseFloat(normSvcDec(b), 10, svcDecimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", b, err)
	}
	return new(big.Float).SetPrec(svcDecimalPrec).Sub(af, bf).Text('f', -1), nil
}

func mulSvc(a, b string) (string, error) {
	af, _, err := big.ParseFloat(normSvcDec(a), 10, svcDecimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", a, err)
	}
	bf, _, err := big.ParseFloat(normSvcDec(b), 10, svcDecimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", b, err)
	}
	return new(big.Float).SetPrec(svcDecimalPrec).Mul(af, bf).Text('f', -1), nil
}

// divSvcSafe 除法，除数为 0 时返回 "0" 而非报错（便于均价等聚合计算）。
func divSvcSafe(a, b string) (string, error) {
	bf, _, err := big.ParseFloat(normSvcDec(b), 10, svcDecimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", b, err)
	}
	if bf.Sign() == 0 {
		return "0", nil
	}
	af, _, err := big.ParseFloat(normSvcDec(a), 10, svcDecimalPrec, big.ToNearestEven)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", a, err)
	}
	return new(big.Float).SetPrec(svcDecimalPrec).Quo(af, bf).Text('f', -1), nil
}
