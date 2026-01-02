package planner

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
)

// GenerateStableTaskID 生成稳定的任务ID
// 基于 rule_id + task_params 的内容生成MD5哈希
// 注意：不包含 node_id，这样即使任务被分配到不同节点，TaskID 也保持不变
// 这样可以避免任务漂移问题：已存在的非失败任务会被保留在原节点
func GenerateStableTaskID(ruleID, taskParams string) string {
	content := fmt.Sprintf("%s|%s", ruleID, taskParams)
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}
