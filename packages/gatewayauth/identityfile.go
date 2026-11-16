package gatewayauth

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// 身份公钥表是 access 校验外部身份用的文件：部署从 Admin 列出登记的身份公钥后写到目标机。
// 外部入口并入主机网关（批 4 T16）之后公钥随快照下发，这个文件与这里的读写一起删除。

type identityFile struct {
	Version    int             `json:"version"`
	Identities []identityEntry `json:"identities"`
}

type identityEntry struct {
	Identity  string `json:"identity"`
	PublicKey string `json:"public_key"`
}

// EncodeIdentityFile 生成身份公钥表（按身份名排序，同样的输入逐字节相同）。
func EncodeIdentityFile(identities map[string]ed25519.PublicKey) ([]byte, error) {
	file := identityFile{Version: 2, Identities: []identityEntry{}}
	for identity, public := range identities {
		if !validIdentifier(identity) || len(public) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("身份 %q 的公钥不合法", identity)
		}
		file.Identities = append(file.Identities, identityEntry{Identity: identity, PublicKey: EncodePublicKey(public)})
	}
	sort.Slice(file.Identities, func(i, j int) bool { return file.Identities[i].Identity < file.Identities[j].Identity })
	raw, err := json.Marshal(file)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// LoadIdentityFile 读身份公钥表，返回只含这些身份的校验表。表里没有秘密，但只认当前用户私有的普通文件，免得被别的用户改写。
func LoadIdentityFile(path string) (*Keys, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("读身份公钥表: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("身份公钥表 %s 必须是不超过 1MiB 的 0600 普通文件", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读身份公钥表: %w", err)
	}
	var file identityFile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil || decoder.More() || file.Version != 2 {
		return nil, errors.New("身份公钥表格式不对（要 version 2 的单个 JSON 文档）")
	}
	identities := map[string]ed25519.PublicKey{}
	for _, entry := range file.Identities {
		public, err := ParsePublicKey(entry.PublicKey)
		if _, duplicate := identities[entry.Identity]; err != nil || duplicate {
			return nil, fmt.Errorf("身份公钥表里 %q 的公钥不合法或重复", entry.Identity)
		}
		identities[entry.Identity] = public
	}
	return NewKeys(nil, identities)
}
