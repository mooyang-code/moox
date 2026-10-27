// Package storagepolicy defines the Storage policy file (storage-policy.json):
// how long each time-series Dataset keeps its rows, by space and frequency,
// and how many bars a View keeps. moox-cli renders the file from moox.toml;
// storage-primary and storage-view load and validate it at startup.
package storagepolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
)

const (
	// Forever keeps every row of a Dataset.
	Forever = "forever"

	// SourceDefault, SourceSpace and SourceRecord name where a Dataset's
	// effective retention comes from.
	SourceDefault = "default"
	SourceSpace   = "space"
	SourceRecord  = "record"

	// DefaultViewBars is the number of bars a series View keeps.
	DefaultViewBars = 10_000

	// DefaultMaxViewFileBytes 是单个 View 文件的容量阈值：1 万根的 1m K 线 View 约 1.1GB，阈值要留出余量，
	// 否则会反复触发「达到容量上限、重建只保留最新」。
	DefaultMaxViewFileBytes = int64(4) << 30

	maxBars = 1_000_000
	// maxViewBars 是 View 每序列保留行数的上限，与读接口单次 latest_per_series 的上限一致，重建回填才能兑现配置。
	maxViewBars = 10_000
)

// Policy is the content of storage-policy.json.
type Policy struct {
	Retention Retention `json:"retention"`
	View      View      `json:"view"`
}

// Retention maps canonical frequencies to retention periods. A space entry
// overrides the default for that space; the defaults cover every frequency,
// so every time-series Dataset has a retention.
type Retention struct {
	Defaults map[string]string            `json:"defaults"`
	Spaces   map[string]map[string]string `json:"spaces,omitempty"`
}

// View holds the View maintenance limits.
type View struct {
	// Bars is the number of most recent bars a series View keeps.
	Bars uint64 `json:"bars"`
	// TrimBars is the per-series count above which a View is trimmed back to
	// Bars; see TrimBarsFor.
	TrimBars                 uint64 `json:"trim_bars"`
	MaintenanceCheckInterval string `json:"maintenance_check_interval"`
	CapacityCheckInterval    string `json:"capacity_check_interval"`
	CapacityCheckJitter      string `json:"capacity_check_jitter"`
	MaxViewFileBytes         int64  `json:"max_view_file_bytes"`
}

// Period is a retention period. A Forever period keeps all rows.
type Period struct {
	Duration time.Duration
	Forever  bool
}

// ParsePeriod parses "forever", "<n>d" or "<n>h". A period is a whole
// number of hours, at least one hour.
func ParsePeriod(raw string) (Period, error) {
	value := strings.TrimSpace(raw)
	if value == Forever {
		return Period{Forever: true}, nil
	}
	if len(value) < 2 {
		return Period{}, fmt.Errorf("retention %q must be <n>h, <n>d or forever", raw)
	}
	count, err := strconv.ParseUint(value[:len(value)-1], 10, 32)
	if err != nil || count == 0 {
		return Period{}, fmt.Errorf("retention %q must be <n>h, <n>d or forever", raw)
	}
	switch value[len(value)-1] {
	case 'h':
		return Period{Duration: time.Duration(count) * time.Hour}, nil
	case 'd':
		return Period{Duration: time.Duration(count) * 24 * time.Hour}, nil
	default:
		return Period{}, fmt.Errorf("retention %q must be <n>h, <n>d or forever", raw)
	}
}

// String renders the period canonically: "forever" or "<n>h".
func (p Period) String() string {
	if p.Forever {
		return Forever
	}
	return strconv.FormatInt(int64(p.Duration/time.Hour), 10) + "h"
}

// Resolve returns the effective retention of a time-series Dataset with the
// given space and canonical frequency, and whether it comes from the space
// override or the default.
func (r Retention) Resolve(spaceID, freq string) (Period, string, error) {
	if raw, ok := r.Spaces[spaceID][freq]; ok {
		period, err := ParsePeriod(raw)
		return period, SourceSpace, err
	}
	if raw, ok := r.Defaults[freq]; ok {
		period, err := ParsePeriod(raw)
		return period, SourceDefault, err
	}
	// 未单独配置的频率（例如合成出来的 45m）沿用时长不超过它的最大标准档的保留期：先空间覆盖，再默认。
	fallback, ok := fallbackStandard(freq)
	if !ok {
		return Period{}, "", fmt.Errorf("no retention for frequency %q", freq)
	}
	if raw, ok := r.Spaces[spaceID][fallback]; ok {
		period, err := ParsePeriod(raw)
		return period, SourceSpace, err
	}
	raw, ok := r.Defaults[fallback]
	if !ok {
		return Period{}, "", fmt.Errorf("no retention for frequency %q", freq)
	}
	period, err := ParsePeriod(raw)
	return period, SourceDefault, err
}

// fallbackStandard 返回时长不超过 freq 的最大标准档（45m → 30m，2h → 1h，12h → 4h）；标准档自身没有回落。
func fallbackStandard(freq string) (string, bool) {
	parsed, err := frequencypkg.Parse(freq)
	if err != nil || string(parsed) != freq {
		return "", false
	}
	target := parsed.NominalDuration()
	best := ""
	var bestDuration time.Duration
	for _, candidate := range frequencypkg.Standard() {
		duration := candidate.NominalDuration()
		if duration <= target && duration > bestDuration {
			best, bestDuration = string(candidate), duration
		}
	}
	if best == "" || best == freq {
		return "", false
	}
	return best, true
}

// TrimBarsFor returns ceil(1.2 × bars), the count at which a View is trimmed.
func TrimBarsFor(bars uint64) uint64 {
	return (bars*6 + 4) / 5
}

// Default returns the generic policy: every standard frequency has a default
// retention. Space overrides are deployment configuration (moox.toml), not
// part of the package.
func Default() Policy {
	policy := Policy{
		Retention: Retention{
			Defaults: map[string]string{
				"30s": "90h", "1m": "35d", "5m": "35d", "15m": "105d", "30m": "210d",
				"1h": "420d", "4h": "1700d", "1d": Forever, "1w": Forever, "1mo": Forever,
			},
		},
		View: View{
			Bars:                     DefaultViewBars,
			TrimBars:                 TrimBarsFor(DefaultViewBars),
			MaintenanceCheckInterval: "1m",
			CapacityCheckInterval:    "1h",
			CapacityCheckJitter:      "1h",
			MaxViewFileBytes:         DefaultMaxViewFileBytes,
		},
	}
	normalized, err := policy.normalized()
	if err != nil {
		panic(err)
	}
	return normalized
}

// Validate reports whether the policy is complete and within limits.
func (p Policy) Validate() error {
	_, err := p.normalized()
	return err
}

// Coverage checks whether retention can supply view.bars by calendar time.
// A default that cannot is an error; a space override that cannot is only a
// warning, since a space may deliberately keep less history.
func (p Policy) Coverage() (errs, warnings []string) {
	normalized, err := p.normalized()
	if err != nil {
		return []string{err.Error()}, nil
	}
	check := func(path, raw, freq string, isDefault bool) {
		period, _ := ParsePeriod(raw)
		if period.Forever {
			return
		}
		bars := barsWithin(period, freq)
		if bars >= float64(normalized.View.Bars) {
			return
		}
		message := fmt.Sprintf("%s = %s provides approximately %.0f bars, fewer than view.bars = %d", path, raw, bars, normalized.View.Bars)
		if isDefault {
			errs = append(errs, message)
		} else {
			warnings = append(warnings, message)
		}
	}
	for _, freq := range sortedKeys(normalized.Retention.Defaults) {
		check("retention.defaults."+freq, normalized.Retention.Defaults[freq], freq, true)
	}
	spaces := make([]string, 0, len(normalized.Retention.Spaces))
	for space := range normalized.Retention.Spaces {
		spaces = append(spaces, space)
	}
	sort.Strings(spaces)
	for _, space := range spaces {
		for _, freq := range sortedKeys(normalized.Retention.Spaces[space]) {
			check("retention.spaces."+space+"."+freq, normalized.Retention.Spaces[space][freq], freq, false)
		}
	}
	return errs, warnings
}

// barsWithin 按自然时间估算保留期能装下多少根 bar（不考虑交易时段：交易时段市场的实际根数更少，配置时自行留余量）。
func barsWithin(period Period, freq string) float64 {
	f, _ := frequencypkg.Parse(freq)
	barLength := f.Duration()
	if barLength == 0 {
		barLength = 31 * 24 * time.Hour
	}
	return float64(period.Duration) / float64(barLength)
}

// RetentionBars 返回空间 × 频率的保留期按自然时间大约能装下多少根 bar；永久保留时 forever 为真。
// 保留期要覆盖 View 的根数，也要大于因子的最大回看根数（引擎要从 Storage 预载这么多历史）。
func (p Policy) RetentionBars(spaceID, freq string) (bars float64, forever bool, err error) {
	period, _, err := p.Retention.Resolve(spaceID, freq)
	if err != nil {
		return 0, false, err
	}
	if period.Forever {
		return 0, true, nil
	}
	return barsWithin(period, freq), false, nil
}

// MinRetentionFor 返回按自然时间装下 bars 根 bar 至少需要的保留期，向上取整到整小时。
func MinRetentionFor(freq string, bars int) Period {
	one := barsWithin(Period{Duration: time.Hour * 24 * 365}, freq)
	if one <= 0 {
		return Period{}
	}
	perYear := time.Hour * 24 * 365
	need := time.Duration(float64(perYear) * float64(bars) / one)
	return Period{Duration: (need + time.Hour - 1) / time.Hour * time.Hour}
}

// normalized validates the policy and renders every period canonically.
func (p Policy) normalized() (Policy, error) {
	out := Policy{View: p.View, Retention: Retention{Defaults: map[string]string{}}}
	for _, freq := range frequencypkg.StandardStrings() {
		raw, ok := p.Retention.Defaults[freq]
		if !ok {
			return Policy{}, fmt.Errorf("retention.defaults must cover every standard frequency; %q is missing", freq)
		}
		period, err := ParsePeriod(raw)
		if err != nil {
			return Policy{}, fmt.Errorf("retention.defaults.%s: %w", freq, err)
		}
		out.Retention.Defaults[freq] = period.String()
	}
	for freq, raw := range p.Retention.Defaults {
		if !frequencypkg.IsCanonical(freq) {
			return Policy{}, fmt.Errorf("retention.defaults: %q is not a canonical frequency", freq)
		}
		period, err := ParsePeriod(raw)
		if err != nil {
			return Policy{}, fmt.Errorf("retention.defaults.%s: %w", freq, err)
		}
		out.Retention.Defaults[freq] = period.String()
	}
	if len(p.Retention.Spaces) > 0 {
		out.Retention.Spaces = make(map[string]map[string]string, len(p.Retention.Spaces))
	}
	for spaceID, overrides := range p.Retention.Spaces {
		if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(spaceID) != spaceID {
			return Policy{}, fmt.Errorf("retention.spaces: invalid space id %q", spaceID)
		}
		space := make(map[string]string, len(overrides))
		for freq, raw := range overrides {
			if !frequencypkg.IsCanonical(freq) {
				return Policy{}, fmt.Errorf("retention.spaces.%s: %q is not a canonical frequency", spaceID, freq)
			}
			period, err := ParsePeriod(raw)
			if err != nil {
				return Policy{}, fmt.Errorf("retention.spaces.%s.%s: %w", spaceID, freq, err)
			}
			space[freq] = period.String()
		}
		out.Retention.Spaces[spaceID] = space
	}
	if err := p.View.validate(); err != nil {
		return Policy{}, err
	}
	return out, nil
}

func (v View) validate() error {
	if v.Bars == 0 || v.Bars > maxViewBars {
		return fmt.Errorf("view.bars must be between 1 and %d", maxViewBars)
	}
	if v.TrimBars <= v.Bars || v.TrimBars > maxBars {
		return fmt.Errorf("view.trim_bars must be greater than view.bars and at most %d", maxBars)
	}
	interval, err := time.ParseDuration(strings.TrimSpace(v.MaintenanceCheckInterval))
	if err != nil || interval < 30*time.Second {
		return errors.New("view.maintenance_check_interval must be at least 30s")
	}
	capacityInterval, err := time.ParseDuration(strings.TrimSpace(v.CapacityCheckInterval))
	if err != nil || capacityInterval <= 0 || capacityInterval > 24*time.Hour {
		return errors.New("view.capacity_check_interval must be greater than 0 and at most 24h")
	}
	capacityJitter, err := time.ParseDuration(strings.TrimSpace(v.CapacityCheckJitter))
	if err != nil || capacityJitter <= 0 || capacityJitter > capacityInterval {
		return errors.New("view.capacity_check_jitter must be greater than 0 and at most view.capacity_check_interval")
	}
	if v.MaxViewFileBytes <= 0 {
		return errors.New("view.max_view_file_bytes must be positive")
	}
	return nil
}

// Parse decodes and validates storage-policy.json. Unknown fields are
// rejected, and periods are returned in canonical form.
func Parse(raw []byte) (Policy, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode storage policy: %w", err)
	}
	normalized, err := policy.normalized()
	if err != nil {
		return Policy{}, fmt.Errorf("invalid storage policy: %w", err)
	}
	return normalized, nil
}

// Load reads and validates the policy file at path.
func Load(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("read storage policy: %w", err)
	}
	return Parse(raw)
}

// Encode renders the policy as indented JSON with a trailing newline. Equal
// policies encode to identical bytes, so a rendered file can be compared
// with the one on a host.
func (p Policy) Encode() ([]byte, error) {
	normalized, err := p.normalized()
	if err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, _ := frequencypkg.Parse(keys[i])
		right, _ := frequencypkg.Parse(keys[j])
		if left.NominalDuration() != right.NominalDuration() {
			return left.NominalDuration() < right.NominalDuration()
		}
		return keys[i] < keys[j]
	})
	return keys
}
