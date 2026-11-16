// Package requestauth 只剩生成请求 nonce，供 packages/gatewayauth 使用；批 4 的 T14 连同这一层一起删掉。
// 浏览器请求签名已经换成 Cookie 会话，原来的签名材料、签名与校验都已删除。
package requestauth

import (
	"fmt"

	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

// NewNonce 返回 32 字节随机数的小写十六进制。
func NewNonce() (string, error) {
	nonce, err := mooxsecurity.RandomHex(32)
	if err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return nonce, nil
}
