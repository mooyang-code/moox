package planner

import (
	"crypto/md5"
	"encoding/binary"
	"sort"
)

// RendezvousHash 基于 Rendezvous (HRW) 一致性哈希选择节点
// 对每个候选节点计算 hash(taskID + nodeID)，取分数最高的节点
// 特性：
//   - 相同输入（taskID + 候选节点集合）始终映射到同一节点，消除输入顺序敏感性
//   - 候选节点集合变化时（扩缩容），只有受影响任务会迁移，迁移代价最小
//   - 无需维护哈希环，实现简单
//
// candidateNodeIDs 必须非空；调用方保证已排序去重以保证稳定性。
func RendezvousHash(taskID string, candidateNodeIDs []string) string {
	if len(candidateNodeIDs) == 0 {
		return ""
	}
	if len(candidateNodeIDs) == 1 {
		return candidateNodeIDs[0]
	}

	bestNode := candidateNodeIDs[0]
	bestScore := uint64(0)

	for _, nodeID := range candidateNodeIDs {
		score := hashScore(taskID, nodeID)
		if score > bestScore {
			bestScore = score
			bestNode = nodeID
		}
	}

	return bestNode
}

// hashScore 计算任务ID与节点ID组合的哈希分数
// 使用 MD5 前 8 字节转为 uint64，分布均匀且稳定
func hashScore(taskID, nodeID string) uint64 {
	h := md5.New()
	h.Write([]byte(taskID))
	h.Write([]byte(nodeID))
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}

// SortNodeIDs 对节点ID列表做字典序稳定排序（去重）
// 用于消除节点输入顺序不确定性，保证 Rendezvous/least-loaded 结果可复现
func SortNodeIDs(nodeIDs []string) []string {
	if len(nodeIDs) == 0 {
		return nodeIDs
	}
	seen := make(map[string]struct{}, len(nodeIDs))
	unique := make([]string, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	sort.Strings(unique)
	return unique
}
