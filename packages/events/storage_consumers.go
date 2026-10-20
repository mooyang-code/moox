package events

import "strings"

// Storage View consumer names are part of the EventBus topology contract.
// Keep them in the events package so Storage, Admin and operational tooling
// cannot silently drift to different durable names.
const (
	StorageViewConsumerStream = "MOOX_STORAGE"
	StorageViewFactorConsumer = "storage_view_factor"
	StorageViewMiscConsumer   = "storage_view_misc"
)

var StorageViewConsumerDurables = []string{
	StorageViewFactorConsumer,
	StorageViewMiscConsumer,
}

// StorageDatasetEvents 是随数据集产生的四类 Storage 事件：清除某个数据集的历史事件、对齐 View 消费者的过滤主题时，
// 都按这四类处理。放在这里让 Storage 命令行与 Admin 的 EventBus 维护接口不会各记一份。
var StorageDatasetEvents = []Event{DatasetRowsUpserted, CollectorPeriodCompleted, FactorPeriodComputed, DatasetSyncPoint}

// IsStorageViewDurable 报告 name 是否是 Storage View 的消费者：两个固定消费者，
// 或 View 清单协调器按数据集动态创建的 storage_view_misc_<12 位十六进制>、storage_view_factor_<12 位十六进制>
// （名称由数据集身份哈希得到，见 storage-view 的 dynamicDatasetConsumerIdentityForPartition）。
func IsStorageViewDurable(name string) bool {
	for _, durable := range StorageViewConsumerDurables {
		if name == durable {
			return true
		}
	}
	for _, prefix := range []string{StorageViewMiscConsumer, StorageViewFactorConsumer} {
		if suffix, ok := strings.CutPrefix(name, prefix+"_"); ok && len(suffix) == 12 && isLowerHex(suffix) {
			return true
		}
	}
	return false
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
