package okx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"math/big"
)

// hmacSha256 返回 HMAC-SHA256 摘要。
func hmacSha256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// jsonMarshal 把 map 序列化为 JSON 字符串；忽略 map 中空值以减少请求体噪声。
func jsonMarshal(m map[string]string) string {
	cleaned := make(map[string]string, len(m))
	for k, v := range m {
		if v != "" {
			cleaned[k] = v
		}
	}
	b, err := json.Marshal(cleaned)
	if err != nil {
		return "{}"
	}
	return string(b)
}

const okxDecPrec = 128

func decAdd(a, b string) string {
	af, _, _ := big.ParseFloat(normStr(a), 10, okxDecPrec, big.ToNearestEven)
	bf, _, _ := big.ParseFloat(normStr(b), 10, okxDecPrec, big.ToNearestEven)
	return new(big.Float).SetPrec(okxDecPrec).Add(af, bf).Text('f', -1)
}

func normStr(s string) string {
	if s == "" {
		return "0"
	}
	return s
}
