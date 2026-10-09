package gatewayroute

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// MetadataVerifiedCaller 是主机网关转发给本机服务时写入的可信调用方身份。网关在转发前丢弃入站的
// 全部 x-moox- 元数据，因此本机服务看到的这个值只可能来自网关自己。
const MetadataVerifiedCaller = "x-moox-verified-caller"

// MetadataSpaceID 是调用方写入的 space 元数据：控制台转发浏览器请求、组件按 space 调用时写入，
// 服务从 tRPC 元数据中读取。主机网关原样透传。
const MetadataSpaceID = "x-space-id"

// MetadataAccessPrincipal 是外部接入转发时写入的外部调用方名称（例如 scf-collector），只用于日志和指标；
// 鉴权以主机网关校验的 access 身份为准。
const MetadataAccessPrincipal = "x-access-principal"

// VerificationKey 是快照中的一把调用方校验密钥。
type VerificationKey struct {
	KeyID  string `json:"key_id"`
	Caller string `json:"caller"`
	Secret string `json:"secret"`
}

// StateHash 计算一台主机网关完整快照的哈希：覆盖路由哈希、服务目录版本和校验密钥。
// 网关控制据此判断是否需要下发，主机网关据此校验收到的快照，并在心跳中上报已应用的哈希。
func StateHash(routeHash, directoryVersion string, keys []VerificationKey) (string, error) {
	sorted := append([]VerificationKey(nil), keys...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].KeyID < sorted[j].KeyID })
	encoded, err := json.Marshal(struct {
		RouteHash        string            `json:"route_hash"`
		DirectoryVersion string            `json:"directory_version"`
		Keys             []VerificationKey `json:"keys"`
	}{RouteHash: routeHash, DirectoryVersion: directoryVersion, Keys: sorted})
	if err != nil {
		return "", fmt.Errorf("序列化快照: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
