package view

import (
	"log"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

// SetMaxAge 设置 View 按空间 × 频率的最长保留历史：data_time 早于“当前时间 − 期限”的行不写入 View。
// 不调用（零值）表示不限制。
func (s *Service) SetMaxAge(maxAge storagepolicy.Retention) {
	s.mu.Lock()
	s.maxAge = maxAge
	s.maxAgeEnabled = len(maxAge.Defaults) > 0
	s.mu.Unlock()
}

// dropExpiredWrites 丢弃早于最长保留期的时序行，返回保留的行；全部保留时原样返回切片，不分配。
// 补采、导入、因子历史重算写进来的旧行与实时行一视同仁，主存 Dataset 不经过这里。
// 键里没有 data_time（记录型行）、时间解析失败、没有对应期限（或永久保留）的行都保留。
func (s *Service) dropExpiredWrites(indexID string, writes []viewindex.RowWrite) []viewindex.RowWrite {
	s.mu.RLock()
	maxAge, enabled := s.maxAge, s.maxAgeEnabled
	s.mu.RUnlock()
	if !enabled || len(writes) == 0 {
		return writes
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	type seriesKey struct{ space, freq string }
	cutoffs := make(map[seriesKey]time.Time)
	cutoffOf := func(key seriesKey) time.Time {
		if cutoff, ok := cutoffs[key]; ok {
			return cutoff
		}
		var cutoff time.Time
		// 没配置的频率（Resolve 报错）不限制：宁可多留，不能误删。
		if period, _, err := maxAge.Resolve(key.space, key.freq); err == nil && !period.Forever {
			cutoff = now.Add(-period.Duration)
		}
		cutoffs[key] = cutoff
		return cutoff
	}
	var kept []viewindex.RowWrite
	dropped := map[seriesKey]int{}
	for i, write := range writes {
		expired := false
		if series := write.Key.Key.GetTimeSeries(); series != nil {
			key := seriesKey{write.Key.Key.GetSpaceId(), series.GetFreq()}
			if cutoff := cutoffOf(key); !cutoff.IsZero() {
				if dataTime, err := time.Parse(time.RFC3339Nano, series.GetDataTime()); err == nil && dataTime.Before(cutoff) {
					expired = true
					dropped[key]++
				}
			}
		}
		if expired {
			if kept == nil {
				kept = append(make([]viewindex.RowWrite, 0, len(writes)), writes[:i]...)
			}
			continue
		}
		if kept != nil {
			kept = append(kept, write)
		}
	}
	if kept == nil {
		return writes
	}
	total := 0
	for key, count := range dropped {
		total += count
		s.metrics.ObserveViewExpiredRowsDropped(key.space, key.freq, count)
	}
	log.Printf("storage view 丢弃超过最长保留期的行 index=%s 丢弃=%d 保留=%d", indexID, total, len(kept))
	return kept
}
