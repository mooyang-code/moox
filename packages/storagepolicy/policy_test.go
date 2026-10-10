package storagepolicy

import (
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
		{"crypto", "1m", "168h", SourceDefault},
		{"crypto", "1d", Forever, SourceDefault},
		{"stockcn", "1m", "1080h", SourceSpace},
		{"stockhk", "1m", "720h", SourceSpace},
		{"stockcn", "1d", Forever, SourceDefault},
		{"mooxsys", "30s", "48h", SourceSpace},
		{"mooxsys", "1m", "4320h", SourceSpace},
		{"newspace", "5m", "720h", SourceDefault},
	}
	for _, tc := range cases {
		period, source, err := retention.Resolve(tc.space, tc.freq)
		if err != nil || period.String() != tc.want || source != tc.source {
			t.Errorf("Resolve(%s, %s) = %s/%s, %v; want %s/%s", tc.space, tc.freq, period, source, err, tc.want, tc.source)
		}
	}
	if _, _, err := retention.Resolve("crypto", "2h"); err == nil {
		t.Fatal("an unknown frequency must not resolve")
	}
}

func TestValidateRequiresCompleteDefaultsAndCanonicalKeys(t *testing.T) {
	mutate := func(edit func(*Policy)) Policy {
		policy := Default()
		policy.Retention.Defaults = copyMap(policy.Retention.Defaults)
		policy.Retention.Spaces = map[string]map[string]string{"stockcn": copyMap(policy.Retention.Spaces["stockcn"])}
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

func TestCoverageRejectsShort7x24RetentionAndWarnsForSessionMarkets(t *testing.T) {
	policy := Default()
	policy.Retention.Defaults = copyMap(policy.Retention.Defaults)
	policy.Retention.Defaults["1m"] = "3d" // 4,320 bars < 5,000
	policy.Retention.Spaces = map[string]map[string]string{"stockcn": {"1m": "20d"}}
	errs, warnings := policy.Coverage()
	if len(errs) != 1 || !strings.Contains(errs[0], "retention.defaults.1m = 72h") {
		t.Fatalf("errors = %v", errs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "retention.spaces.stockcn.1m") {
		t.Fatalf("warnings = %v", warnings)
	}
}

// 日线每个交易日一根：stockcn 保留 300 天约 214 个交易日，少于 view.bars=300 时应当告警。
func TestCoverageCountsOneDailyBarPerTradingDay(t *testing.T) {
	policy := Default()
	policy.Retention.Spaces = map[string]map[string]string{"stockcn": {"1d": "7200h"}}
	_, warnings := policy.Coverage()
	found := false
	for _, warning := range warnings {
		if strings.Contains(warning, "retention.spaces.stockcn.1d") {
			found = true
		}
	}
	if !found {
		t.Fatalf("300 天的日线保留应当告警，warnings = %v", warnings)
	}
}
