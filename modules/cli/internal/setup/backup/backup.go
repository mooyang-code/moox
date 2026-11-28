// Package backup 读写 moox.toml 同目录的加密备份文件（moox.backup），
// 合成本机派生的密钥迁移包（SecretMgr.ExportSecrets）与加密后的告警渠道（MonitorMgr.ExportNotificationChannels）。
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

const (
	// Format 是备份文件的外层格式标识。
	Format = "moox.backup/v1"
	// keyPurpose 是从本机钥匙串密钥派生备份子密钥时使用的域分离标识。
	keyPurpose    = "setup-backup-v1"
	channelCipher = "aes-256-gcm"
	// maxBackupBytes 限制备份文件大小，防止读取异常大文件。
	maxBackupBytes = 16 << 20
)

// Channel 是加密保存在备份文件里的告警渠道，含完整 Webhook URL 与签名密钥。
type Channel struct {
	ChannelID   string `json:"channel_id,omitempty"`
	ChannelType string `json:"channel_type"`
	WebhookURL  string `json:"webhook_url"`
	Secret      string `json:"secret,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"`
}

// ErrPendingImport 表示备份文件带有“卸载后待导入”标记，任何自动或手动备份都不得覆盖。
var ErrPendingImport = errors.New("backup_pending_import")

// ErrKeyFingerprintMismatch 表示当前本机钥匙串密钥与备份文件记录的指纹不一致，重装后无法解密导入。
var ErrKeyFingerprintMismatch = errors.New("backup_key_fingerprint_mismatch")

// File 是落盘的备份文件外层结构：不含任何明文密钥、口令或 Webhook。
type File struct {
	Format         string `json:"format"`
	ExportedAt     string `json:"exported_at"`
	KeyFingerprint string `json:"key_fingerprint"`
	// PendingImport 表示该备份处于“卸载后待导入”状态：卸载开始清空前写上，安装自动导入成功后清掉。
	// 标记为 true 时，任何备份（自动或手动）都不得覆盖这份文件，防止中途失败再卸载时用半成品覆盖完好备份。
	PendingImport        bool                `json:"pending_import,omitempty"`
	ImportedAt           string              `json:"imported_at,omitempty"`
	Secrets              SecretsSection      `json:"secrets"`
	NotificationChannels NotificationSection `json:"notification_channels"`
}

// SecretsSection 保存 SecretMgr.ExportSecrets 返回的加密迁移包及条数。
type SecretsSection struct {
	Count  int    `json:"count"`
	Bundle string `json:"bundle"`
}

// NotificationSection 保存用本机派生密钥加密后的告警渠道密文及条数。
type NotificationSection struct {
	Count      int    `json:"count"`
	Cipher     string `json:"cipher"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// Opened 是解密校验通过后的备份文件。
type Opened struct {
	File     File
	Channels []Channel
}

// Credentials 保存由本机钥匙串主密钥派生出的备份子密钥与主密钥指纹。
// 同一把 32 字节子密钥既编码成 SecretMgr 迁移包的口令，也用于加密告警渠道部分。
type Credentials struct {
	key         []byte
	Fingerprint string
}

// DeriveCredentials 从本机钥匙串密钥派生备份凭证。
// create 为 true 时若本机还没有主密钥则生成一把并保存；读回校验或恢复时应传 false。
func DeriveCredentials(store configcrypt.KeyStore, create bool) (*Credentials, error) {
	if store == nil {
		store = configcrypt.Default()
	}
	key, fp, err := configcrypt.DeriveKey(store, keyPurpose, create)
	if err != nil {
		return nil, err
	}
	return &Credentials{key: key, Fingerprint: fp}, nil
}

// Passphrase 返回传给 SecretMgr.ExportSecrets / ImportSecrets 的导出口令（64 位十六进制，仅在进程内使用）。
func (c *Credentials) Passphrase() string {
	if c == nil || len(c.key) == 0 {
		return ""
	}
	return hex.EncodeToString(c.key)
}

// Clear 清除内存里的派生密钥字节。
func (c *Credentials) Clear() {
	if c == nil {
		return
	}
	clear(c.key)
	c.key = nil
}

// Seal 把加密的密钥迁移包与告警渠道清单合成为加密备份文件内容（写出时不带待导入标记；
// 待导入标记只在卸载真正开始清空主机前通过 MarkPendingImport 写入）。
func Seal(secretsBundle string, secretCount int, channels []Channel, creds *Credentials, now time.Time) ([]byte, error) {
	if creds == nil || len(creds.key) == 0 {
		return nil, errors.New("缺少备份派生密钥")
	}
	if strings.TrimSpace(secretsBundle) == "" {
		return nil, errors.New("密钥迁移包为空")
	}
	if secretCount < 0 {
		return nil, errors.New("密钥条数无效")
	}
	if channels == nil {
		channels = []Channel{}
	}
	plain, err := json.Marshal(channels)
	if err != nil {
		return nil, fmt.Errorf("编码告警渠道失败：%w", err)
	}
	defer clear(plain)

	aead, err := newBackupAEAD(creds.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成加密随机数失败：%w", err)
	}
	file := File{
		Format:         Format,
		ExportedAt:     now.UTC().Format(time.RFC3339),
		KeyFingerprint: creds.Fingerprint,
		Secrets:        SecretsSection{Count: secretCount, Bundle: secretsBundle},
		NotificationChannels: NotificationSection{
			Count:  len(channels),
			Cipher: channelCipher,
			Nonce:  base64.StdEncoding.EncodeToString(nonce),
		},
	}
	sealed := aead.Seal(nil, nonce, plain, channelAAD(file))
	file.NotificationChannels.Ciphertext = base64.StdEncoding.EncodeToString(sealed)

	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化备份文件失败：%w", err)
	}
	return append(encoded, '\n'), nil
}

// VerifyKeyFingerprint 核对备份文件记录的主密钥指纹与当前本机钥匙串密钥是否一致（不自动生成新密钥）。
func VerifyKeyFingerprint(meta *File, store configcrypt.KeyStore) error {
	if meta == nil {
		return errors.New("缺少备份元数据")
	}
	creds, err := DeriveCredentials(store, false)
	if err != nil {
		if errors.Is(err, configcrypt.ErrKeyMissing) {
			return fmt.Errorf("%w: 本机钥匙串里没有与该备份对应的密钥，重装后将无法解密导入", ErrKeyFingerprintMismatch)
		}
		return fmt.Errorf("读取本机钥匙串密钥失败：%w", err)
	}
	defer creds.Clear()
	if meta.KeyFingerprint == "" || meta.KeyFingerprint != creds.Fingerprint {
		return fmt.Errorf("%w: 备份文件不是用当前这台电脑的钥匙串密钥生成的（指纹不匹配），重装后将无法解密导入", ErrKeyFingerprintMismatch)
	}
	return nil
}

// ReadMetadata 只读解析备份文件外层元数据（不解密告警渠道，也不访问钥匙串）。
// 文件不存在时返回 (nil, false, nil)。
func ReadMetadata(path string) (*File, bool, error) {
	raw, err := ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	file, err := parseFileHeader(raw)
	if err != nil {
		return nil, false, err
	}
	return &file, true, nil
}

func parseFileHeader(raw []byte) (File, error) {
	var file File
	if err := json.Unmarshal(raw, &file); err != nil {
		return File{}, errors.New("备份文件不是合法的 JSON 格式")
	}
	if file.Format != Format || strings.TrimSpace(file.Secrets.Bundle) == "" ||
		file.NotificationChannels.Cipher != channelCipher || file.Secrets.Count < 0 || file.NotificationChannels.Count < 0 {
		return File{}, errors.New("备份文件格式无效或已损坏")
	}
	return file, nil
}

// WriteGuarded 在写入前检查目标备份是否带有“卸载后待导入”标记：有则拒绝覆盖并返回 ErrPendingImport。
func WriteGuarded(path string, data []byte) error {
	meta, exists, err := ReadMetadata(path)
	if err != nil {
		return err
	}
	if exists && meta.PendingImport {
		return fmt.Errorf("%w: 本机备份 %s 带有“卸载后待导入”标记（含 %d 把密钥、%d 个告警渠道），拒绝覆盖；请先完成安装导入该备份",
			ErrPendingImport, path, meta.Secrets.Count, meta.NotificationChannels.Count)
	}
	return WriteAtomic(path, data)
}

// MarkPendingImport 给本机已有备份打上“卸载后待导入”标记（在卸载真正开始清空主机前调用）。
// 备份文件不存在时返回 (nil, false, nil)。
func MarkPendingImport(path string) (*File, bool, error) {
	meta, exists, err := ReadMetadata(path)
	if err != nil || !exists {
		return nil, false, err
	}
	if meta.PendingImport {
		return meta, true, nil
	}
	meta.PendingImport = true
	encoded, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, true, fmt.Errorf("序列化备份文件失败：%w", err)
	}
	if err := WriteAtomic(path, append(encoded, '\n')); err != nil {
		return nil, true, err
	}
	return meta, true, nil
}

// ClearPendingImport 清除本机备份上过期的“卸载后待导入”标记（例如系统仍处于已装好状态并完成 --keep 更新后）。
// 备份文件不存在或未带待导入标记时不做改动，返回 (false, nil)。
func ClearPendingImport(path string) (bool, error) {
	meta, exists, err := ReadMetadata(path)
	if err != nil || !exists || !meta.PendingImport {
		return false, err
	}
	meta.PendingImport = false
	encoded, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return false, fmt.Errorf("序列化备份文件失败：%w", err)
	}
	if err := WriteAtomic(path, append(encoded, '\n')); err != nil {
		return false, err
	}
	return true, nil
}

// MarkImported 在安装自动导入成功后清掉“卸载后待导入”标记并记录导入时刻。
func MarkImported(path string, now time.Time) error {
	meta, exists, err := ReadMetadata(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("读不到备份文件 %s：%w", path, os.ErrNotExist)
	}
	meta.PendingImport = false
	meta.ImportedAt = now.UTC().Format(time.RFC3339)
	encoded, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化备份文件失败：%w", err)
	}
	return WriteAtomic(path, append(encoded, '\n'))
}

// Open 解析并解密备份文件的告警渠道部分，同时核对格式、主密钥指纹与条数一致性。
func Open(raw []byte, creds *Credentials) (*Opened, error) {
	if creds == nil || len(creds.key) == 0 {
		return nil, errors.New("缺少备份派生密钥")
	}
	file, err := parseFileHeader(raw)
	if err != nil {
		return nil, err
	}
	if file.KeyFingerprint != creds.Fingerprint {
		return nil, configcrypt.ErrKeyMissing
	}
	nonce, err := base64.StdEncoding.DecodeString(file.NotificationChannels.Nonce)
	if err != nil {
		return nil, errors.New("备份文件渠道密文随机数已损坏")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(file.NotificationChannels.Ciphertext)
	if err != nil {
		return nil, errors.New("备份文件渠道密文已损坏")
	}
	aead, err := newBackupAEAD(creds.key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, errors.New("备份文件渠道密文随机数长度无效")
	}
	plain, err := aead.Open(nil, nonce, ciphertext, channelAAD(file))
	if err != nil {
		return nil, errors.New("备份文件解密失败：文件被改动过，或者不是用这台电脑的密钥生成的")
	}
	defer clear(plain)

	var channels []Channel
	if err := json.Unmarshal(plain, &channels); err != nil {
		return nil, errors.New("备份文件里的告警渠道数据已损坏")
	}
	if len(channels) != file.NotificationChannels.Count {
		return nil, fmt.Errorf("备份文件告警渠道条数不一致：头记录 %d 条，解密得到 %d 条", file.NotificationChannels.Count, len(channels))
	}
	return &Opened{File: file, Channels: channels}, nil
}

// WriteAtomic 以 0600 权限原子写入备份文件，拒绝覆盖非普通文件（如符号链接或目录）。
func WriteAtomic(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("备份文件路径不能为空")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("备份目标 %s 必须是普通文件，拒绝覆盖", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查备份文件 %s 失败：%w", path, err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("在 %s 创建备份临时文件失败：%w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func(cause error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("写入备份文件 %s 失败：%w", path, cause)
	}
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(0o600); err != nil {
			return cleanup(err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("写入备份文件 %s 失败：%w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("保存备份文件 %s 失败：%w", path, err)
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

// ReadFile 读取备份文件：要求普通文件（不跟随符号链接）、非 Windows 权限 0600，且不超过上限。
func ReadFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("读不到备份文件 %s：%w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("备份文件 %s 必须是普通文件", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("备份文件 %s 的权限必须是 0600", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开备份文件 %s 失败：%w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("备份文件 %s 在读取时被替换", path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBackupBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取备份文件 %s 失败：%w", path, err)
	}
	if len(raw) > maxBackupBytes {
		return nil, fmt.Errorf("备份文件 %s 超过大小上限", path)
	}
	return raw, nil
}

func newBackupAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// channelAAD 把外层元数据绑定进告警渠道密文的认证附加数据，改动任意一项都解不开。
func channelAAD(file File) []byte {
	return []byte(fmt.Sprintf("%s|%s|%s|%d|%s",
		file.Format, file.ExportedAt, file.KeyFingerprint, file.NotificationChannels.Count, file.NotificationChannels.Cipher))
}
