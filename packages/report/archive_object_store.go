package report

import "time"

// 对象存储可用状态的取值：archive-mgr 按 worker 报来的连续不可用起点评估，不可用超过 5 分钟上报关注项 object_store_down。
const (
	ObjectStoreAvailable   = 0
	ObjectStoreUnavailable = 1 // 不可用未满 ObjectStoreDownAfter
	ObjectStoreDown        = 2 // 不可用超过 ObjectStoreDownAfter
)

// ObjectStoreDownAfter 是连续不可用多久后升为告警。
const ObjectStoreDownAfter = 5 * time.Minute

// EvaluateObjectStore 按连续不可用的起点（零值表示可用）得出状态。
func EvaluateObjectStore(unavailableSince, now time.Time) int {
	switch {
	case unavailableSince.IsZero():
		return ObjectStoreAvailable
	case now.Sub(unavailableSince) >= ObjectStoreDownAfter:
		return ObjectStoreDown
	}
	return ObjectStoreUnavailable
}
