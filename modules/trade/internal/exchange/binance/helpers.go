package binance

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
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
