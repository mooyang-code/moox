package rpc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// 秘钥迁移包：把全部秘钥连同明文用导出口令加密成一个 JSON 文本，在另一套环境用同一口令导入。
// 口令经 PBKDF2-SHA256 派生 AES-256-GCM 密钥；格式名与 KDF 参数作为附加数据参与认证，改动任何一项都解不开。

const (
	bundleFormat = "moox.secrets/v1"
	bundleKDF    = "pbkdf2-sha256"
	bundleCipher = "aes-256-gcm"
	// bundleIterations 按 OWASP 对 PBKDF2-SHA256 的建议取值；导入时只接受合理范围，防止伪造的迁移包让服务端空转。
	bundleIterations    = 600_000
	minBundleIterations = 100_000
	maxBundleIterations = 10_000_000
	// minPassphraseRunes 是导出口令的最少字符数：迁移包离开系统后只靠口令保护，太短的口令可以离线暴力破解。
	minPassphraseRunes = 12
)

var (
	errWeakPassphrase  = fmt.Errorf("导出口令至少 %d 个字符", minPassphraseRunes)
	errBundleInvalid   = errors.New("迁移包格式无效，请选择从秘钥管理导出的文件")
	errBundleDecrypt   = errors.New("口令错误或迁移包已损坏")
	errEmptyPassphrase = errors.New("口令不能为空")
)

// secretBundle 是迁移包的外层：除密文外都是明文元数据。
type secretBundle struct {
	Format     string          `json:"format"`
	ExportedAt string          `json:"exported_at"`
	Count      int             `json:"count"`
	KDF        bundleKDFParams `json:"kdf"`
	Cipher     string          `json:"cipher"`
	Nonce      string          `json:"nonce"`
	Ciphertext string          `json:"ciphertext"`
}

type bundleKDFParams struct {
	Name       string `json:"name"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
}

// bundleSecret 是迁移包里的一个秘钥（明文，只存在于密文里）。
type bundleSecret struct {
	SecretID    string `json:"secret_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category"`
	Provider    string `json:"provider,omitempty"`
	SecretType  string `json:"secret_type,omitempty"`
	KeyID       string `json:"key_id,omitempty"`
	SecretValue string `json:"secret_value"`
	ExtraConfig string `json:"extra_config,omitempty"`
	Status      string `json:"status,omitempty"`
}

// sealBundle 用口令加密秘钥列表，返回迁移包文本。
func sealBundle(secrets []bundleSecret, passphrase string, now time.Time) (string, error) {
	if utf8.RuneCountInString(passphrase) < minPassphraseRunes {
		return "", errWeakPassphrase
	}
	plaintext, err := json.Marshal(secrets)
	if err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	bundle := secretBundle{
		Format: bundleFormat, ExportedAt: now.UTC().Format(time.RFC3339), Count: len(secrets),
		KDF:    bundleKDFParams{Name: bundleKDF, Iterations: bundleIterations, Salt: base64.StdEncoding.EncodeToString(salt)},
		Cipher: bundleCipher,
	}
	aead, err := bundleAEAD(passphrase, salt, bundleIterations)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	bundle.Nonce = base64.StdEncoding.EncodeToString(nonce)
	bundle.Ciphertext = base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, plaintext, bundleAAD(bundle)))
	out, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// openBundle 用口令解开迁移包，返回其中的秘钥。
func openBundle(raw, passphrase string) ([]bundleSecret, error) {
	if passphrase == "" {
		return nil, errEmptyPassphrase
	}
	var bundle secretBundle
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &bundle); err != nil {
		return nil, errBundleInvalid
	}
	if bundle.Format != bundleFormat || bundle.KDF.Name != bundleKDF || bundle.Cipher != bundleCipher ||
		bundle.KDF.Iterations < minBundleIterations || bundle.KDF.Iterations > maxBundleIterations {
		return nil, errBundleInvalid
	}
	salt, err := base64.StdEncoding.DecodeString(bundle.KDF.Salt)
	if err != nil || len(salt) < 16 {
		return nil, errBundleInvalid
	}
	nonce, err := base64.StdEncoding.DecodeString(bundle.Nonce)
	if err != nil {
		return nil, errBundleInvalid
	}
	ciphertext, err := base64.StdEncoding.DecodeString(bundle.Ciphertext)
	if err != nil {
		return nil, errBundleInvalid
	}
	aead, err := bundleAEAD(passphrase, salt, bundle.KDF.Iterations)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, errBundleInvalid
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, bundleAAD(bundle))
	if err != nil {
		return nil, errBundleDecrypt
	}
	var secrets []bundleSecret
	if err := json.Unmarshal(plaintext, &secrets); err != nil {
		return nil, errBundleInvalid
	}
	return secrets, nil
}

func bundleAEAD(passphrase string, salt []byte, iterations int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iterations, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// bundleAAD 是参与认证的外层元数据：格式、导出时间、数量与 KDF 参数都不能被改动。
func bundleAAD(bundle secretBundle) []byte {
	return []byte(fmt.Sprintf("%s|%s|%d|%s|%d|%s|%s", bundle.Format, bundle.ExportedAt, bundle.Count,
		bundle.KDF.Name, bundle.KDF.Iterations, bundle.KDF.Salt, bundle.Cipher))
}
