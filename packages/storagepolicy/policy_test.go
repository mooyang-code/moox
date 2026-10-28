package storagepolicy

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestParsePeriod(t *testing.T) {
	cases := map[string]Period{
		"forever": {Forever: true},
		"48h":     {Duration: 48 * time.Hour},
		"7d":      {Duration: 7 * 24 * time.Hour},
	}
	for raw, want := range cases {
		got, err := ParsePeriod(raw)
		if err != nil || got != want {
			t.Errorf("ParsePeriod(%q) = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "0h", "0d", "1.5h", "90m", "-1d", "1w", "7"} {
		if _, err := ParsePeriod(raw); err == nil {
			t.Errorf("ParsePeriod(%q) succeeded; want an error", raw)
		}
	}
	if got := (Period{Duration: 7 * 24 * time.Hour}).String(); got != "168h" {
		t.Fatalf("7d renders as %q, want 168h", got)
	}
}

func TestResolvePrefersSpaceOverride(t *testing.T) {
	retention := Default().Retention
	cases := []struct {
		space, freq, want, source string
	}{
		{"crypto", "1m", "840h", SourceDefault},
		{"crypto", "1d", Forever, SourceDefault},
		{"stockcn", "1m", "1440h", SourceSpace},
		{"stockcn", "1d", Forever, SourceDefault},
		{"newspace", "5m", "840h", SourceDefault},
	}
	retention.Spaces = map[string]map[string]string{"stockcn": {"1m": "1440h"}}
	for _, tc := range cases {
		period, source, err := retention.Resolve(tc.space, tc.freq)
		if err != nil || period.String() != tc.want || source != tc.source {
			t.Errorf("Resolve(%s, %s) = %s/%s, %v; want %s/%s", tc.space, tc.freq, period, source, err, tc.want, tc.source)
		}
	}
	if _, _, err := retention.Resolve("crypto", "7m"); err == nil {
		t.Fatal("a frequency outside the grammar must not resolve")
	}
}

func TestResolveFallsBackToLargestStandardFrequencyNotLonger(t *testing.T) {
	retention := Default().Retention
	cases := []struct{ freq, standard string }{
		{"45m", "30m"}, {"2h", "1h"}, {"12h", "4h"}, {"3m", "1m"}, {"10m", "5m"}, {"20m", "15m"},
	}
	for _, tc := range cases {
		got, source, err := retention.Resolve("crypto", tc.freq)
		want, _, wantErr := retention.Resolve("crypto", tc.standard)
		if err != nil || wantErr != nil || got != want || source != SourceDefault {
			t.Errorf("Resolve(crypto, %s) = %v/%s, %v; want the %s retention %v", tc.freq, got, source, err, tc.standard, want)
		}
	}
	// 精确配置优先于回落。
	exact := Default().Retention
	exact.Defaults = copyMap(exact.Defaults)
	exact.Defaults["45m"] = "99h"
	if got, _, err := exact.Resolve("crypto", "45m"); err != nil || got.String() != "99h" {
		t.Fatalf("an exact default must win over the fallback, got %v, %v", got, err)
	}
	// 空间覆盖的标准档先于默认的回落；回落只取时长不超过自己的标准档，20m 不会取 30m。
	spaced := Default().Retention
	spaced.Spaces = map[string]map[string]string{"crypto": {"30m": "11h"}}
	if got, source, err := spaced.Resolve("crypto", "45m"); err != nil || got.String() != "11h" || source != SourceSpace {
		t.Fatalf("45m should use the space 30m override, got %v/%s, %v", got, source, err)
	}
	want, _, _ := spaced.Resolve("crypto", "15m")
	if got, _, err := spaced.Resolve("crypto", "20m"); err != nil || got != want {
		t.Fatalf("20m should fall back to 15m, got %v, %v", got, err)
	}
}

func TestValidateAcceptsNonStandardCanonicalFrequencies(t *testing.T) {
	policy := Default()
	policy.Retention.Defaults = copyMap(policy.Retention.Defaults)
	policy.Retention.Defaults["45m"] = "30d"
	if _, err := Parse(mustMarshal(t, policy)); err != nil {
		t.Fatalf("a canonical non-standard frequency should be accepted: %v", err)
	}
	policy.Retention.Defaults["60m"] = "30d"
	if _, err := Parse(mustMarshal(t, policy)); err == nil {
		t.Fatal("an alias must be rejected as a retention key")
	}
}

func TestValidateRequiresCompleteDefaultsAndCanonicalKeys(t *testing.T) {
	mutate := func(edit func(*Policy)) Policy {
		policy := Default()
		policy.Retention.Defaults = copyMap(policy.Retention.Defaults)
		policy.Retention.Spaces = map[string]map[string]string{"stockcn": {"1m": "60d"}}
		edit(&policy)
		return policy
	}
	cases := map[string]Policy{
		"missing default":        mutate(func(p *Policy) { delete(p.Retention.Defaults, "4h") }),
		"alias default":          mutate(func(p *Policy) { p.Retention.Defaults["1H"] = "1d" }),
		"alias space override":   mutate(func(p *Policy) { p.Retention.Spaces["stockcn"]["1M"] = "30d" }),
		"invalid period":         mutate(func(p *Policy) { p.Retention.Defaults["1m"] = "7 days" }),
		"blank space":            mutate(func(p *Policy) { p.Retention.Spaces[" "] = map[string]string{"1m": "1d"} }),
		"trim not above bars":    mutate(func(p *Policy) { p.View.TrimBars = p.View.Bars }),
		"no bars":                mutate(func(p *Policy) { p.View.Bars = 0 }),
		"fast maintenance check": mutate(func(p *Policy) { p.View.MaintenanceCheckInterval = "10s" }),
		"jitter over interval":   mutate(func(p *Policy) { p.View.CapacityCheckJitter = "2h" }),
		"no file limit":          mutate(func(p *Policy) { p.View.MaxViewFileBytes = 0 }),
	}
	for name, policy := range cases {
		if err := policy.Validate(); err == nil {
			t.Errorf("%s: Validate() succeeded", name)
		}
	}
	if err := Default().Validate(); err != nil {
		t.Fatalf("default policy: %v", err)
	}
}

func TestEncodeParseRoundTripIsCanonical(t *testing.T) {
	policy := Default()
	policy.Retention.Defaults = copyMap(policy.Retention.Defaults)
	policy.Retention.Defaults["5m"] = "30d"
	raw, err := policy.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"5m": "720h"`) || !strings.HasSuffix(string(raw), "}\n") {
		t.Fatalf("encoded policy is not canonical:\n%s", raw)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := parsed.Encode()
	if err != nil || string(again) != string(raw) {
		t.Fatalf("round trip changed the encoding: %v\n%s", err, again)
	}
	if _, err := Parse([]byte(`{"retention":{"defaults":{}},"view":{},"extra":1}`)); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
}

func TestTrimBarsFor(t *testing.T) {
	for bars, want := range map[uint64]uint64{5000: 6000, 1: 2, 7: 9, 10: 12} {
		if got := TrimBarsFor(bars); got != want {
			t.Errorf("TrimBarsFor(%d) = %d, want %d", bars, got, want)
		}
	}
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func TestDefaultPolicyCoversViewBars(t *testing.T) {
	errs, warnings := Default().Coverage()
	if len(errs) != 0 || len(warnings) != 0 {
		t.Fatalf("recommended policy coverage: errors=%v warnings=%v", errs, warnings)
	}
}

func TestCoverageRejectsShortDefaultsAndOnlyWarnsForSpaceOverrides(t *testing.T) {
	policy := Default()
	policy.Retention.Defaults = copyMap(policy.Retention.Defaults)
	policy.Retention.Defaults["1m"] = "3d" // 4,320 bars < view.bars
	policy.Retention.Spaces = map[string]map[string]string{"stockcn": {"1m": "5d"}}
	errs, warnings := policy.Coverage()
	if len(errs) != 1 || !strings.Contains(errs[0], "retention.defaults.1m = 72h") {
		t.Fatalf("errors = %v", errs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "retention.spaces.stockcn.1m") {
		t.Fatalf("warnings = %v", warnings)
	}
}

// 保留期按自然时间能装下的 bar 数；永久保留不限；向上取整的最小保留期能装下指定根数。
func TestRetentionBarsAndMinRetention(t *testing.T) {
	policy := Default()
	bars, forever, err := policy.RetentionBars("crypto", "1h")
	if err != nil || forever || bars != 420*24 {
		t.Fatalf("crypto 1h: bars=%v forever=%v err=%v", bars, forever, err)
	}
	bars, forever, err = policy.RetentionBars("crypto", "1m")
	if err != nil || forever || bars != 35*24*60 {
		t.Fatalf("crypto 1m: bars=%v forever=%v err=%v", bars, forever, err)
	}
	if _, forever, err = policy.RetentionBars("crypto", "1d"); err != nil || !forever {
		t.Fatalf("crypto 1d should be forever: forever=%v err=%v", forever, err)
	}
	if _, _, err = policy.RetentionBars("crypto", "7m"); err == nil {
		t.Fatal("unknown frequency should error")
	}

	need := MinRetentionFor("1h", 5000)
	if need.Duration != 5000*time.Hour {
		t.Fatalf("5000 1h bars need 5000h, got %v", need.Duration)
	}
	need = MinRetentionFor("1m", 5000)
	if want := time.Duration(math.Ceil(5000.0/60)) * time.Hour; need.Duration != want {
		t.Fatalf("5000 1m bars need %v, got %v", want, need.Duration)
	}
}

func mustMarshal(t *testing.T, policy Policy) []byte {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// view.max_age 要装得下 view.bars 根：默认值不足报错，空间覆盖不足只警告（空间可以有意少留历史）。
func TestCoverageChecksViewMaxAge(t *testing.T) {
	policy := Default()
	policy.View.MaxAge.Defaults = copyMap(policy.View.MaxAge.Defaults)
	policy.View.MaxAge.Defaults["5m"] = "10d"
	policy.View.MaxAge.Spaces = map[string]map[string]string{"stockcn": {"1m": "5d"}, "mooxsys": {"1m": "1d"}}
	errs, warnings := policy.Coverage()
	if len(errs) != 1 || !strings.Contains(errs[0], "view.max_age.defaults.5m = 240h") {
		t.Fatalf("errors = %v", errs)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "view.max_age.spaces.mooxsys.1m") || !strings.Contains(warnings[1], "view.max_age.spaces.stockcn.1m") {
		t.Fatalf("warnings = %v", warnings)
	}
}

// 代码里的默认值不含任何业务空间：没有空间覆盖时按默认值解析，空间覆盖来自部署配置。
func TestDefaultViewMaxAgeResolvesPerFrequency(t *testing.T) {
	policy := Default()
	for space, want := range map[string]map[string]string{
		"crypto":  {"1m": "168h", "5m": "840h", "1d": "forever", "45m": "5040h"},
		"stockcn": {"1m": "168h", "5m": "840h"},
		"mooxsys": {"1m": "168h", "30s": "90h"},
	} {
		for freq, expected := range want {
			period, _, err := policy.View.MaxAge.Resolve(space, freq)
			if err != nil || period.String() != expected {
				t.Fatalf("%s %s = %v, %v; want %s", space, freq, period, err, expected)
			}
		}
	}
	policy.View.MaxAge.Spaces = map[string]map[string]string{"stockcn": {"1m": "60d"}}
	if period, _, err := policy.View.MaxAge.Resolve("stockcn", "1m"); err != nil || period.String() != "1440h" {
		t.Fatalf("空间覆盖 stockcn 1m = %v, %v; want 1440h", period, err)
	}
}

func TestViewMaxAgeValidationAndRoundTrip(t *testing.T) {
	policy := Default()
	policy.View.MaxAge.Defaults = copyMap(policy.View.MaxAge.Defaults)
	delete(policy.View.MaxAge.Defaults, "15m")
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "view.max_age.defaults must cover every standard frequency") {
		t.Fatalf("缺频率应报错: %v", err)
	}
	policy = Default()
	policy.View.MaxAge.Spaces = map[string]map[string]string{"crypto": {"1M": "7d"}}
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "view.max_age.spaces.crypto") {
		t.Fatalf("非规范频率应报错: %v", err)
	}
	withSpace := Default()
	withSpace.View.MaxAge.Spaces = map[string]map[string]string{"mooxsys": {"1m": "7d"}}
	raw, err := withSpace.Encode()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.View.MaxAge.Defaults["5m"] != "840h" || parsed.View.MaxAge.Spaces["mooxsys"]["1m"] != "168h" {
		t.Fatalf("round trip lost view.max_age: %+v", parsed.View.MaxAge)
	}
}
