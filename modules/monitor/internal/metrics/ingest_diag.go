package metrics

import (
	"sort"
	"sync"
	"time"
)

// maxDiagServices 限制诊断表里记录的服务数，上报方的服务名失控时诊断表不会跟着无限增长。
const maxDiagServices = 256

// SeriesDrop 是某个服务因新序列限额被丢弃的序列统计（进程启动以来累计）。
type SeriesDrop struct {
	Service    string
	LastMetric string
	Total      int64
	LastAt     time.Time
}

// IngestReject 是某个服务最近一次被拒收的快照。
type IngestReject struct {
	Service    string
	LastReason string
	LastAt     time.Time
	ByReason   map[string]int64
}

// ingestDiagnostics 在内存里记录入库时被丢弃、被拒收的情况，供健康概览与 Doctor 解释“为什么这个服务的指标不见了”。
// 只在进程内保存：这些是排障线索，不是需要持久化的事实，重启后从零累计。
type ingestDiagnostics struct {
	mu       sync.Mutex
	dropped  map[string]*SeriesDrop
	rejected map[string]*IngestReject
}

func newIngestDiagnostics() *ingestDiagnostics {
	return &ingestDiagnostics{dropped: make(map[string]*SeriesDrop), rejected: make(map[string]*IngestReject)}
}

func (d *ingestDiagnostics) recordDropped(service, metric string, count int, at time.Time) {
	if count <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	item := d.dropped[service]
	if item == nil {
		if len(d.dropped) >= maxDiagServices {
			return
		}
		item = &SeriesDrop{Service: service}
		d.dropped[service] = item
	}
	item.Total += int64(count)
	item.LastMetric = metric
	item.LastAt = at
}

func (d *ingestDiagnostics) recordRejected(service, reason string, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	item := d.rejected[service]
	if item == nil {
		if len(d.rejected) >= maxDiagServices {
			return
		}
		item = &IngestReject{Service: service, ByReason: make(map[string]int64)}
		d.rejected[service] = item
	}
	item.ByReason[reason]++
	item.LastReason = reason
	item.LastAt = at
}

// drops 返回 since 之后仍有丢弃的服务，丢得最多的在前。
func (d *ingestDiagnostics) drops(since time.Time) []SeriesDrop {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]SeriesDrop, 0, len(d.dropped))
	for _, item := range d.dropped {
		if !item.LastAt.Before(since) {
			out = append(out, *item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Service < out[j].Service
	})
	return out
}

// rejects 返回被拒收过的服务，最近被拒的在前。
func (d *ingestDiagnostics) rejects() []IngestReject {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]IngestReject, 0, len(d.rejected))
	for _, item := range d.rejected {
		copied := *item
		copied.ByReason = make(map[string]int64, len(item.ByReason))
		for k, v := range item.ByReason {
			copied.ByReason[k] = v
		}
		out = append(out, copied)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastAt.Equal(out[j].LastAt) {
			return out[i].LastAt.After(out[j].LastAt)
		}
		return out[i].Service < out[j].Service
	})
	return out
}
