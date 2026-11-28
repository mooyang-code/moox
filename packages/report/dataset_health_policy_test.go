package report

import (
	"testing"
	"time"
)

func TestDefaultRealtimeTimeSeriesPolicyIsValid(t *testing.T) {
	policy := DefaultRealtimeTimeSeriesPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatalf("默认策略应当合法：%v", err)
	}
	if policy.Defaults.RunMissedIntervals != 2 || policy.Defaults.SuccessMissedIntervals != 3 ||
		policy.Defaults.MinuteLateness != 10*time.Second || policy.Defaults.HourLateness != 20*time.Second {
		t.Fatalf("默认阈值不符：%+v", policy.Defaults)
	}
}

func TestRealtimeTimeSeriesPolicyValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RealtimeTimeSeriesPolicy)
	}{
		{"运行漏报倍数非正", func(p *RealtimeTimeSeriesPolicy) { p.Defaults.RunMissedIntervals = 0 }},
		{"成功漏报倍数非正", func(p *RealtimeTimeSeriesPolicy) { p.Defaults.SuccessMissedIntervals = 0 }},
		{"分钟迟到容忍非正", func(p *RealtimeTimeSeriesPolicy) { p.Defaults.MinuteLateness = 0 }},
		{"小时迟到容忍非正", func(p *RealtimeTimeSeriesPolicy) { p.Defaults.HourLateness = 0 }},
		{"覆盖项缺少标识", func(p *RealtimeTimeSeriesPolicy) {
			p.Overrides = []RealtimeTimeSeriesOverride{{SpaceID: "crypto", Frequency: "1m"}}
		}},
		{"覆盖项周期无效", func(p *RealtimeTimeSeriesPolicy) {
			p.Overrides = []RealtimeTimeSeriesOverride{{SpaceID: "crypto", DatasetID: "ds", Frequency: "bad"}}
		}},
		{"覆盖项重复", func(p *RealtimeTimeSeriesPolicy) {
			p.Overrides = []RealtimeTimeSeriesOverride{
				{SpaceID: "crypto", DatasetID: "ds", Frequency: "1h", Lateness: time.Minute},
				{SpaceID: "crypto", DatasetID: "ds", Frequency: "1H", Lateness: 2 * time.Minute},
			}
		}},
		{"覆盖项迟到容忍为负", func(p *RealtimeTimeSeriesPolicy) {
			p.Overrides = []RealtimeTimeSeriesOverride{{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", Lateness: -time.Second}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := DefaultRealtimeTimeSeriesPolicy()
			tc.mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("非法数据集健康策略被接受")
			}
		})
	}
}

// 默认策略：分钟级允许迟到 10 秒、小时级 20 秒，日线及以上 1 个周期；按数据集的覆盖优先。
func TestRealtimeTimeSeriesPolicyLatenessByFrequency(t *testing.T) {
	policy := DefaultRealtimeTimeSeriesPolicy()
	for freq, want := range map[string]time.Duration{
		"30s": 10 * time.Second, "1m": 10 * time.Second, "5m": 10 * time.Second, "30m": 10 * time.Second,
		"1h": 20 * time.Second, "1H": 20 * time.Second, "4h": 20 * time.Second,
		"1d": 24 * time.Hour, "1w": 7 * 24 * time.Hour, "bad": 0,
	} {
		if got := policy.Lateness("crypto", "dataset_x", freq); got != want {
			t.Fatalf("lateness(%s) = %s, want %s", freq, got, want)
		}
	}
	policy.Overrides = []RealtimeTimeSeriesOverride{{SpaceID: "crypto", DatasetID: "dataset_x", Frequency: "1H", Lateness: 5 * time.Minute}}
	if got := policy.Lateness("crypto", "dataset_x", "1h"); got != 5*time.Minute {
		t.Fatalf("override lateness = %s", got)
	}
	if got := policy.Lateness("crypto", "dataset_y", "1h"); got != 20*time.Second {
		t.Fatalf("another dataset must keep the default: %s", got)
	}
}
