package factorudf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// Hash 是 wasm 字节的 sha256（十六进制小写），也是版本的身份。
func Hash(wasm []byte) string {
	sum := sha256.Sum256(wasm)
	return hex.EncodeToString(sum[:])
}
