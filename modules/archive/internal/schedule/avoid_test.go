package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestComputeAvoidance(t *testing.T) {
	now := utc(2026, 11, 8, 10, 0, 0)
	earlier := utc(2026, 11, 8, 9, 40, 0)
	quiet := &[2]string{HourQuietFrom, HourQuietUntil}
	ok := func(space string, freqs ...string) SpaceFrequencies {
		return SpaceFrequencies{SpaceID: space, Freqs: freqs, FetchedAt: now}
	}
	for _, tc := range []struct {
		name   string
		spaces []SpaceFrequencies
		every  int
		quiet  *[2]string
		basis  []string // Basis 必须包含的片段
	}{
		{"只 1m", []SpaceFrequencies{ok("crypto", "1m")}, 1, nil, []string{"crypto：1m", "每分钟触发", "不避让整点"}},
		{"只 5m", []SpaceFrequencies{ok("stockcn", "5m", "5m")}, 5, nil, []string{"stockcn：5m", "每 5 分钟触发"}},
		{"3m 与 5m 得 1", []SpaceFrequencies{ok("a", "3m"), ok("b", "5m")}, 1, nil, []string{"a：3m；b：5m"}},
		{"1m 与 5m 得 1", []SpaceFrequencies{ok("crypto", "5m", "1m")}, 1, nil, []string{"crypto：1m、5m"}},
		{"9m 得 3", []SpaceFrequencies{ok("x", "9m")}, 3, nil, []string{"每 3 分钟触发"}},
		{"9m 与 16m 得 1", []SpaceFrequencies{ok("x", "9m", "16m")}, 1, nil, nil},
		{"不整除一天的 7m 不是合法频率，按最保守处理", []SpaceFrequencies{ok("x", "7m", "5m")}, 1, quiet, []string{"x：5m、7m", "有无法识别的频率 7m，按最保守处理", "每分钟触发", "整点避让"}},
		{"15m 与 30m 得 15", []SpaceFrequencies{ok("x", "30m", "15m")}, 15, nil, []string{"每 15 分钟触发"}},
		{"30s 按每分钟并注明第 30 秒", []SpaceFrequencies{ok("x", "30s")}, 1, nil, []string{"x：30s（30s 的第 30 秒触发不在避让范围内）"}},
		{"30s 与 5m 得 1 并注明", []SpaceFrequencies{ok("x", "5m", "30s")}, 1, nil, []string{"x：30s、5m（30s 的第 30 秒触发不在避让范围内）"}},
		{"整分钟频率不注明", []SpaceFrequencies{ok("x", "1m")}, 1, nil, []string{"x：1m（每分钟"}},
		{"有 1h", []SpaceFrequencies{ok("crypto", "1m", "1h", "1d")}, 1, quiet, []string{"crypto：1m、1h、1d", "有 1h 及以上，整点避让"}},
		{"只有 1h 以上不限秒", []SpaceFrequencies{ok("crypto", "4h", "1d")}, 0, quiet, []string{"不限秒"}},
		{"没有生效的采集函数", []SpaceFrequencies{ok("stockus")}, 0, nil, []string{"stockus：没有生效的采集函数"}},
		{"无法解析的频率按最保守处理", []SpaceFrequencies{ok("x", "bad", "5m", "bad")}, 1, quiet, []string{"x：5m、bad（有无法识别的频率 bad，按最保守处理）"}},
		{"无法解析只影响保守口径、不影响其他空间的显示", []SpaceFrequencies{ok("a", "1h"), ok("b", "??")}, 1, quiet, []string{"a：1h；b：??（有无法识别的频率 ??"}},
		{"没有归档任务", nil, 0, nil, []string{"没有归档任务"}},
		{
			"读取失败沿用上次结果",
			[]SpaceFrequencies{ok("crypto", "1h"), {SpaceID: "stockcn", Freqs: []string{"5m"}, FetchedAt: earlier, Failed: true}},
			5, quiet, []string{"stockcn：5m（读取失败，沿用 11-08 09:40 UTC 的结果）"},
		},
		{
			"从没成功过的空间按默认值",
			[]SpaceFrequencies{ok("stockcn", "5m"), {SpaceID: "crypto", Failed: true}},
			1, quiet, []string{"crypto：读取失败，按默认值"},
		},
		{
			"全部从没成功过用默认值",
			[]SpaceFrequencies{{SpaceID: "crypto", Failed: true}, {SpaceID: "stockcn", Failed: true}},
			1, quiet, []string{"未取到采集频率，使用默认值", "crypto、stockcn"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeAvoidance(tc.spaces, now)
			if got.EveryMinutes != tc.every {
				t.Fatalf("EveryMinutes = %d, want %d（%s）", got.EveryMinutes, tc.every, got.Basis)
			}
			if tc.every > 0 && (got.BeforeSeconds != AvoidBeforeSeconds || got.AfterSeconds != AvoidAfterSeconds) {
				t.Fatalf("秒窗口 = %d/%d", got.BeforeSeconds, got.AfterSeconds)
			}
			if tc.every == 0 && (got.BeforeSeconds != 0 || got.AfterSeconds != 0) {
				t.Fatalf("不限秒时秒窗口应为 0: %d/%d", got.BeforeSeconds, got.AfterSeconds)
			}
			if (got.HourQuiet == nil) != (tc.quiet == nil) || (got.HourQuiet != nil && *got.HourQuiet != *tc.quiet) {
				t.Fatalf("HourQuiet = %v, want %v", got.HourQuiet, tc.quiet)
			}
			for _, part := range tc.basis {
				if !strings.Contains(got.Basis, part) {
					t.Fatalf("Basis %q 缺少 %q", got.Basis, part)
				}
			}
			if !got.ComputedAt.Equal(now) {
				t.Fatalf("ComputedAt = %s", got.ComputedAt)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("结果必须合法: %v", err)
			}
		})
	}
}

func TestDefaultAvoidanceMatchesTheFixedRules(t *testing.T) {
	d := DefaultAvoidance()
	if d.EveryMinutes != 1 || d.BeforeSeconds != 5 || d.AfterSeconds != 25 || d.HourQuiet == nil || *d.HourQuiet != [2]string{"58:00", "04:00"} {
		t.Fatalf("默认值 = %+v", d)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAvoidanceValidate(t *testing.T) {
	for name, a := range map[string]Avoidance{
		"不整除 60":   {EveryMinutes: 7},
		"超过 60":    {EveryMinutes: 120},
		"负秒数":      {EveryMinutes: 1, BeforeSeconds: -1},
		"秒数合计过大":   {EveryMinutes: 1, BeforeSeconds: 30, AfterSeconds: 30},
		"整点窗口格式错误": {HourQuiet: &[2]string{"58", "04:00"}},
	} {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: 必须报错", name)
		}
	}
	if err := (Avoidance{}).Validate(); err != nil {
		t.Fatalf("零值（不避让）应合法: %v", err)
	}
	if _, err := ExportEstimate(Timing{Freq: "1m", Avoid: &Avoidance{}}, time.Unix(0, 0), 0); err != nil {
		t.Fatal(err)
	}
}
