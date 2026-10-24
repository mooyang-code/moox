package model

import (
	"encoding/json"
	"errors"
	"fmt"
)

// 主机 SSH 凭据的秘钥类型：password 存密码，ssh_key 存私钥（可带口令）。
const (
	SSHSecretTypePassword = "password"
	SSHSecretTypeKey      = "ssh_key"
)

// SSHCredential 是主机 SSH 凭据的明文内容，整体序列化成 JSON 后加密存入 Secret.SecretValue。
type SSHCredential struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// Type 返回凭据对应的秘钥类型。
func (c SSHCredential) Type() string {
	if c.PrivateKey != "" {
		return SSHSecretTypeKey
	}
	return SSHSecretTypePassword
}

// Validate 要求密码与私钥恰好给一个，口令只能配合私钥。
func (c SSHCredential) Validate() error {
	switch {
	case c.Password == "" && c.PrivateKey == "":
		return errors.New("SSH 凭据必须提供密码或私钥")
	case c.Password != "" && c.PrivateKey != "":
		return errors.New("SSH 凭据不能同时提供密码和私钥")
	case c.Password != "" && c.Passphrase != "":
		return errors.New("私钥口令只能配合私钥使用")
	}
	return nil
}

// Encode 序列化为待加密的 JSON 文本。
func (c SSHCredential) Encode() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DecodeSSHCredential 解析 Encode 产生的 JSON 文本。
func DecodeSSHCredential(plain string) (SSHCredential, error) {
	var c SSHCredential
	if err := json.Unmarshal([]byte(plain), &c); err != nil {
		return SSHCredential{}, fmt.Errorf("SSH 凭据格式无效: %w", err)
	}
	return c, c.Validate()
}
