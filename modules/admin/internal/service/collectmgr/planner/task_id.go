package planner

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
)

// GenerateStableTaskID 生成稳定的任务ID
// 基于 space_id + rule_id + task_params 的内容生成MD5哈希
// 注意：
//   - 包含 space_id，使不同空间的相同规则+参数产生不同 task_id，避免跨空间哈希碰撞
//   - 不包含 node_id，即使任务被分配到不同节点，TaskID 也保持不变，避免任务漂移
func GenerateStableTaskID(spaceID, ruleID, taskParams string) string {
	content := fmt.Sprintf("%s|%s|%s", spaceID, ruleID, taskParams)
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}
