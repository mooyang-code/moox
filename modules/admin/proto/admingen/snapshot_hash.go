package adminpb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"google.golang.org/protobuf/proto"
)

// SnapshotHash 对整份快照（含校验公钥，不含它自己的 hash 字段）算哈希。控制面按固定顺序给出路由与公钥，
// 确定性的 protobuf 编码也会给目录里的 map 排序。接收方在接受快照之前用同一个函数核对。
// 它不修改传入的消息，也不代替快照的结构校验。
func SnapshotHash(snapshot *HostGatewaySnapshot) (string, error) {
	if snapshot == nil {
		return "", errors.New("gateway snapshot is nil")
	}
	owned := proto.Clone(snapshot).(*HostGatewaySnapshot)
	owned.Hash = ""
	raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(owned)
	if err != nil {
		return "", errors.New("encode gateway snapshot hash")
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}
