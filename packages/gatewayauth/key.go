package gatewayauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// CallerKey 是一个调用方身份的签名密钥文件内容：secrets/caller-<身份>.key，JSON，权限 0600。
type CallerKey struct {
	Caller string `json:"caller"`
	KeyID  string `json:"key_id"`
	Secret string `json:"secret"`
}

// Credentials 把密钥文件内容转换为签名凭据。
func (key CallerKey) Credentials() (Credentials, error) {
	credentials := Credentials{KeyID: strings.TrimSpace(key.KeyID), Caller: strings.TrimSpace(key.Caller), Secret: strings.TrimSpace(key.Secret)}
	if credentials.Caller == "" || !validIdentifier(credentials.Caller) {
		return Credentials{}, errors.New("调用方密钥缺少合法的 caller")
	}
	if _, _, err := validateCredentials(credentials); err != nil {
		return Credentials{}, fmt.Errorf("调用方 %s 的密钥无效: %w", credentials.Caller, err)
	}
	return credentials, nil
}

// LoadCallerKey 读取调用方密钥文件。文件必须是权限为 0600 的普通文件，不接受符号链接。
func LoadCallerKey(path string) (Credentials, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Credentials{}, errors.New("调用方密钥文件路径为空")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("读取调用方密钥文件: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return Credentials{}, fmt.Errorf("调用方密钥文件 %s 必须是权限为 0600 的普通文件", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("读取调用方密钥文件: %w", err)
	}
	key, err := ParseCallerKey(raw)
	if err != nil {
		return Credentials{}, fmt.Errorf("调用方密钥文件 %s: %w", path, err)
	}
	return key.Credentials()
}

// ParseCallerKey 解析密钥文件内容，拒绝未知字段和多余内容。
func ParseCallerKey(raw []byte) (CallerKey, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var key CallerKey
	if err := decoder.Decode(&key); err != nil {
		return CallerKey{}, fmt.Errorf("解析调用方密钥: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return CallerKey{}, errors.New("解析调用方密钥: 只能包含一个 JSON 对象")
	}
	return key, nil
}

// MarshalCallerKey 生成密钥文件内容（末尾带换行）。
func MarshalCallerKey(key CallerKey) ([]byte, error) {
	if _, err := key.Credentials(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ParseCallerKeyValue 解析环境变量里的密钥，格式为 <key_id>:<secret>，供不读配置文件的 SCF 使用。
func ParseCallerKeyValue(caller, value string) (Credentials, error) {
	keyID, secret, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return Credentials{}, errors.New("调用方密钥格式应为 <key_id>:<secret>")
	}
	return CallerKey{Caller: caller, KeyID: keyID, Secret: secret}.Credentials()
}
