package binance

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"math/big"
)

var (
	errNotImplemented = errors.New("binance: not implemented for this market")
	errInvalidParam   = errors.New("binance: invalid parameter")
)

func hmacSha256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// decAdd/decSub 用 big.Float 做字符串加减，返回定点的最简表示。
const binDecPrec = 128

func decAdd(a, b string) string {
	af, _, _ := big.ParseFloat(normStr(a), 10, binDecPrec, big.ToNearestEven)
	bf, _, _ := big.ParseFloat(normStr(b), 10, binDecPrec, big.ToNearestEven)
	return new(big.Float).SetPrec(binDecPrec).Add(af, bf).Text('f', -1)
}

func decSub(a, b string) string {
	af, _, _ := big.ParseFloat(normStr(a), 10, binDecPrec, big.ToNearestEven)
	bf, _, _ := big.ParseFloat(normStr(b), 10, binDecPrec, big.ToNearestEven)
	return new(big.Float).SetPrec(binDecPrec).Sub(af, bf).Text('f', -1)
}

func normStr(s string) string {
	if s == "" {
		return "0"
	}
	return s
}
