package configcrypt

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// 服务端备份用管理员密码加密：密码经 Argon2id 派生出 32 字节密钥，再用 AES-256-GCM 加密整份配置。
// 服务端只保存这个信封，没有管理员密码就解不开。
const (
	backupMagic   = "MOOX-CONFIG-BACKUP v1"
	backupTime    = 3
	backupMemory  = 64 * 1024 // KiB
	backupThreads = 4
	backupSaltLen = 16
)

// ErrBackupPassword 表示密码解不开这份备份：备份用的是初始化时设置的管理员密码（设定后不能修改）。
var ErrBackupPassword = errors.New("密码解不开这份备份：请用初始化时设置的管理员账号和密码")

// SealBackup 用管理员密码加密配置，生成上传到服务端的备份信封。
func SealBackup(plain []byte, password string) (string, error) {
	if password == "" {
		return "", errors.New("管理员密码为空，无法加密备份")
	}
	salt := make([]byte, backupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	header := backupHeader(salt)
	aead, err := newAEAD(backupKey(password, salt))
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
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
	return out.String(), nil
}

// OpenBackup 用管理员密码解开备份信封。
func OpenBackup(envelope, password string) ([]byte, error) {
	lines := strings.Split(strings.TrimRight(envelope, "\n"), "\n")
	if len(lines) < 3 || lines[0] != backupMagic || !strings.HasPrefix(lines[1], "kdf: ") {
		return nil, errors.New("服务端的配置备份格式不对")
	}
	salt, err := parseBackupKDF(lines[1])
	if err != nil {
		return nil, err
	}
	header := lines[0] + "\n" + lines[1] + "\n"
	sealed, err := base64.StdEncoding.DecodeString(strings.Join(lines[2:], ""))
	if err != nil {
		return nil, errors.New("服务端的配置备份已损坏")
	}
	aead, err := newAEAD(backupKey(password, salt))
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize() {
		return nil, errors.New("服务端的配置备份已损坏")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(header))
	if err != nil {
		return nil, ErrBackupPassword
	}
	return plain, nil
}

func backupKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, backupTime, backupMemory, backupThreads, keySize)
}

func backupHeader(salt []byte) string {
	return fmt.Sprintf("%s\nkdf: argon2id t=%d m=%d p=%d salt=%s\n", backupMagic, backupTime, backupMemory, backupThreads, base64.StdEncoding.EncodeToString(salt))
}

// parseBackupKDF 只接受当前这一组参数，参数被改动的信封直接拒绝。
func parseBackupKDF(line string) ([]byte, error) {
	prefix := fmt.Sprintf("kdf: argon2id t=%d m=%d p=%d salt=", backupTime, backupMemory, backupThreads)
	encoded, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return nil, errors.New("服务端的配置备份使用了不认识的加密参数")
	}
	salt, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(salt) != backupSaltLen {
		return nil, errors.New("服务端的配置备份已损坏")
	}
	return salt, nil
}
