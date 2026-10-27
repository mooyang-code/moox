package events

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Storage View 消费者的名字是 EventBus 拓扑契约的一部分，放在 events 包里，
// 让 Storage、Admin 与运维工具不会各记一份而悄悄漂移。
const (
	StorageViewConsumerStream = "MOOX_STORAGE"
	// StorageViewConsumerPrefix 是每个数据集各一个的 View 消费者名前缀：view_<数据集哈希>。名字里不带任何业务含义。
	StorageViewConsumerPrefix = "view_"
)

// StorageViewConsumerName 返回数据集的 View 消费者名：view_<空间与数据集身份哈希的前 12 位十六进制>。
func StorageViewConsumerName(spaceID, datasetID string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(datasetID)))
	return StorageViewConsumerPrefix + hex.EncodeToString(hash[:])[:12]
}

// StorageDatasetEvents 是随数据集产生的四类 Storage 事件：清除某个数据集的历史事件、对齐 View 消费者的过滤主题时，
// 都按这四类处理。放在这里让 Storage 命令行与 Admin 的 EventBus 维护接口不会各记一份。
var StorageDatasetEvents = []Event{DatasetRowsUpserted, CollectorPeriodCompleted, FactorPeriodComputed, DatasetSyncPoint}

// IsStorageViewDurable 报告 name 是否是 Storage View 的消费者：view_<12 位十六进制>。
func IsStorageViewDurable(name string) bool {
	suffix, ok := strings.CutPrefix(name, StorageViewConsumerPrefix)
	return ok && len(suffix) == 12 && isLowerHex(suffix)
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// IsStorageDatasetSubject 报告 subject 是否是某个空间某个数据集在 StorageDatasetEvents 里的确切主题：
// 前缀为事件族，其后恰好两个由 EncodeSubjectToken 产生的 token（空间、数据集），不允许通配符。
func IsStorageDatasetSubject(subject string) bool {
	for _, event := range StorageDatasetEvents {
		prefix := strings.TrimSuffix(eventSubject(event), "<space>.<subject>")
		rest, ok := strings.CutPrefix(subject, prefix)
		if !ok {
			continue
		}
		tokens := strings.Split(rest, ".")
		if len(tokens) != 2 {
			return false
		}
		for _, token := range tokens {
			if !isEncodedSubjectToken(token) {
				return false
			}
		}
		return true
	}
	return false
}

// isEncodedSubjectToken 报告 token 是否只由 EncodeSubjectToken 的字母表（小写字母与 2-7）构成且非空。
func isEncodedSubjectToken(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			return false
		}
	}
	return true
}
