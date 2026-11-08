package report

import (
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
)

// ArchiveObjectStoreStateMetric 是 archive-mgr 上报的归档 worker 对象存储可用状态（标签 worker、probe_error），
// Monitor 据此生成“归档对象存储”检查。probe_error 是 worker 最近一次探测失败的原因，放在标签里供告警原因引用。
const ArchiveObjectStoreStateMetric = "moox_archive_object_store_state"

// 对象存储可用状态的取值。
const (
	ObjectStoreAvailable   = 0
	ObjectStoreUnavailable = 1 // 不可用未满 ObjectStoreDownAfter
	ObjectStoreDown        = 2 // 不可用超过 ObjectStoreDownAfter
)

// ObjectStoreDownAfter 是连续不可用多久后升为告警。
const ObjectStoreDownAfter = 5 * time.Minute

// maxProbeErrorLabel 是 probe_error 标签的长度上限（字节）：Monitor 拒收超过 512 字节的标签值。
const maxProbeErrorLabel = 400

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

// ArchiveObjectStore 是对象存储可用状态的指标，同一时刻最多一条序列。
type ArchiveObjectStore struct {
	gauge *prometheus.GaugeVec
}

// NewArchiveObjectStore 注册指标。
func NewArchiveObjectStore(registerer prometheus.Registerer) (*ArchiveObjectStore, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	s := &ArchiveObjectStore{gauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: ArchiveObjectStoreStateMetric, Help: "归档对象存储可用状态：0 可用，1 不可用未满 5 分钟，2 不可用超过 5 分钟。",
	}, []string{"worker", "probe_error"})}
	if err := registerer.Register(s.gauge); err != nil {
		return nil, err
	}
	return s, nil
}

// Set 用本次评估结果替换旧序列。
func (s *ArchiveObjectStore) Set(worker string, state int, probeError string) {
	if len(probeError) > maxProbeErrorLabel {
		cut := maxProbeErrorLabel
		for !utf8.RuneStart(probeError[cut]) {
			cut--
		}
		probeError = probeError[:cut]
	}
	s.gauge.Reset()
	s.gauge.WithLabelValues(worker, probeError).Set(float64(state))
}

// Clear 不报：worker 没有配置对象存储或在线状态不新鲜。
func (s *ArchiveObjectStore) Clear() { s.gauge.Reset() }
