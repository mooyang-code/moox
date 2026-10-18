// Package configcrypt 加密保存 moox.toml。
//
// 整份配置用 AES-256-GCM 加密；32 字节的随机密钥放在系统钥匙串里（macOS 钥匙串、Windows 凭据管理器、
// Linux Secret Service），没有可用钥匙串的机器退回到操作员目录下仅本人可读的密钥文件。
// 加密后的文件仍叫 moox.toml，内容是纯文本信封：一行标识、一行密钥指纹、其余是 base64 密文，不含任何明文配置。
package configcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	magic   = "MOOX-ENCRYPTED-CONFIG v1"
	keySize = 32
	// lineWidth 是密文 base64 每行的宽度，便于查看与比对。
	lineWidth = 76
)

// ErrKeyMissing 表示这台电脑上没有能解开这份配置的密钥（配置可能来自另一台电脑，或钥匙串条目被删了）。
var ErrKeyMissing = errors.New("这台电脑上没有解密 moox.toml 的密钥：配置可能来自另一台电脑，或者钥匙串里的条目被删除了")

// IsEncrypted 判断内容是不是加密信封。
func IsEncrypted(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte(magic+"\n"))
}

// Encrypt 用本机密钥加密配置；本机还没有密钥时生成一把并保存。
func Encrypt(plain []byte, store KeyStore) ([]byte, error) {
	key, err := store.LoadOrCreate()
	if err != nil {
		return nil, err
	}
	header := magic + "\nkey: " + fingerprint(key) + "\n"
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := aead.Seal(nonce, nonce, plain, []byte(header))
	encoded := base64.StdEncoding.EncodeToString(sealed)
	var out strings.Builder
	out.WriteString(header)
	for len(encoded) > lineWidth {
		out.WriteString(encoded[:lineWidth] + "\n")
		encoded = encoded[lineWidth:]
	}
	out.WriteString(encoded + "\n")
	return []byte(out.String()), nil
}

// Decrypt 解开加密信封。信封被改动、密钥不对时报错，不返回任何部分内容。
func Decrypt(raw []byte, store KeyStore) ([]byte, error) {
	if !IsEncrypted(raw) {
		return nil, errors.New("moox.toml 不是加密格式")
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[1], "key: ") {
		return nil, errors.New("moox.toml 的加密格式不完整")
	}
	header := lines[0] + "\n" + lines[1] + "\n"
	want := strings.TrimPrefix(lines[1], "key: ")
	key, err := store.Load()
	if err != nil {
		return nil, err
	}
	if fingerprint(key) != want {
		return nil, ErrKeyMissing
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.Join(lines[2:], ""))
	if err != nil {
		return nil, errors.New("moox.toml 的密文已损坏")
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize() {
		return nil, errors.New("moox.toml 的密文已损坏")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(header))
	if err != nil {
		return nil, errors.New("moox.toml 解密失败：文件被改动过，或者不是用这台电脑的密钥加密的")
	}
	return plain, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("加密密钥长度不对")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// fingerprint 是密钥的短指纹，用来在解密前判断密钥对不对，不泄露密钥本身。
func fingerprint(key []byte) string {
	sum := sha256.Sum256(append([]byte("moox-config-key\x00"), key...))
	return hex.EncodeToString(sum[:8])
}
