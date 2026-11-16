package gatewayauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/mooyang-code/moox/packages/security"
)

// 私钥文件的内容是一行 base64（标准编码）的 32 字节 ed25519 种子；公钥在登记表、快照与命令行里也用 base64 的 32 字节。

// GenerateSigningKey 生成一把新的签名私钥。
func GenerateSigningKey() (ed25519.PrivateKey, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, errors.New("生成签名私钥失败")
	}
	return private, nil
}

// EncodeSigningKey 返回私钥文件（或 SCF 环境变量 MOOX_CALLER_KEY）的内容，不带换行。
func EncodeSigningKey(private ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(private.Seed())
}

// ParseSigningKey 解析私钥文件的内容。
func ParseSigningKey(text string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("签名私钥必须是 base64 编码的 32 字节 ed25519 种子")
	}
	defer clear(seed)
	return ed25519.NewKeyFromSeed(seed), nil
}

// EncodePublicKey 返回公钥的 base64 写法。
func EncodePublicKey(public ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(public)
}

// ParsePublicKey 解析 base64 的 32 字节公钥。
func ParsePublicKey(text string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(raw) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(raw) != text {
		return nil, errors.New("签名公钥必须是 base64 编码的 32 字节 ed25519 公钥")
	}
	return ed25519.PublicKey(raw), nil
}

// ReadSigningKey 读私钥文件：不跟随符号链接，文件必须只有当前用户可读（Unix 上 0600，Windows 上 ACL 只允许当前用户）。
func ReadSigningKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("签名私钥必须是普通文件，不能是符号链接")
	}
	file, err := openSecretFile(path)
	if err != nil {
		return nil, errors.New("打不开签名私钥文件")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("签名私钥文件在打开时被替换")
	}
	if err := security.CheckPrivateFile(path); err != nil {
		return nil, errors.New("签名私钥文件必须只有当前用户可读（0600；Windows 上 ACL 只允许当前用户）")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || len(raw) > 256 {
		return nil, errors.New("签名私钥文件读不出来或超过大小上限")
	}
	defer clear(raw)
	return ParseSigningKey(string(raw))
}
