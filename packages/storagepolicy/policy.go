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

	"github.com/mooyang-code/moox/packages/barclock"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
)

const (
	// Forever keeps every row of a Dataset.
	Forever = "forever"

	// SourceDefault、SourceSpace、SourceRecord、SourceDerived 与 SourceRetention 标识生效保留期的来源。
	SourceDefault   = "default"
	SourceSpace     = "space"
	SourceRecord    = "record"
	SourceDerived   = "derived"
	SourceRetention = "retention"

	// DefaultViewBars is the number of bars a series View keeps.
	DefaultViewBars = 10_000

	// DefaultMaxViewFileBytes 是单个 View 文件的容量阈值：1 万根的 1m K 线 View 约 1.1GB，阈值要留出余量，
	// 否则会反复触发「达到容量上限、重建只保留最新」。
	DefaultMaxViewFileBytes = int64(4) << 30

	// maxViewBars 是 View 每序列保留行数的上限，与读接口单次 latest_per_series 的上限一致，重建回填才能兑现配置。
	maxViewBars = 10_000
)

// Policy is the content of storage-policy.json.
type Policy struct {
	Retention Retention `json:"retention"`
	View      View      `json:"view"`
}

// Retention maps canonical frequencies to retention periods. A space entry
// overrides the default for that space; for Dataset retention, defaults cover
// every standard frequency, while View.MaxAge stores only explicit overrides.
type Retention struct {
	Defaults map[string]string            `json:"defaults,omitempty"`
	Spaces   map[string]map[string]string `json:"spaces,omitempty"`
}

// View holds the View maintenance limits.
type View struct {
	// MaxAge 是 View 按空间 × 频率的写入年龄上限例外：data_time 早于“当前时间 − 最长保留期”的行不写入 View，
	// 补采、导入和因子历史重算带来的旧行同样丢弃，Dataset 与因子本地存储不受影响。
	// 配置里只写需要覆盖默认推导的例外：未配置时，无交易时段空间按 Bars × 周期向上取整到整天推导（日线及以上永久），
	// 有交易时段的空间（barclock 登记为 session 的市场）直接取对应数据集保留期。
	MaxAge Retention `json:"max_age,omitempty"`
	// Bars is the number of most recent bars a series View keeps.
	Bars                     uint64 `json:"bars"`
	MaintenanceCheckInterval string `json:"maintenance_check_interval"`
	// MaxViewFileBytes 是索引文件大小的告警线：超过只告警，不动数据。
	MaxViewFileBytes int64 `json:"max_view_file_bytes"`
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

// Default returns the generic policy: every standard frequency has a default
// retention, while View.MaxAge stays empty so durations are derived from
// View.Bars (or Dataset retention for session-based markets). Space overrides
// belong to deployment configuration (moox.toml), not this package.
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
			MaintenanceCheckInterval: "1m",
			MaxViewFileBytes:         DefaultMaxViewFileBytes,
		},
	}
	normalized, err := policy.normalized()
	if err != nil {
		panic(err)
	}
	return normalized
}

// DeriveViewMaxAge 由每序列保留根数推导无交易时段空间（7×24）在规范频率 freq 下的 View 最长保留期：
//   - 日线及以上（1d、1w、1mo）：永久（forever）；
//   - 日内周期（30s、1m、5m、15m、30m、45m、1h、4h 等）：bars × 周期长度，向上取整到整天（24h 的整数倍）。
//
// 向上取整到整天保证自然时间能装下的根数不少于 bars（例如 1m 的 10000 根约 6.94 天，推导为 7 天），与 Coverage() 一致。
func DeriveViewMaxAge(bars uint64, freq string) (Period, error) {
	if bars == 0 || bars > maxViewBars {
		return Period{}, fmt.Errorf("view.bars 必须在 1～%d 之间，当前为 %d", maxViewBars, bars)
	}
	parsed, err := frequencypkg.Parse(freq)
	if err != nil || string(parsed) != freq {
		return Period{}, fmt.Errorf("频率 %q 不是规范频率", freq)
	}
	if !parsed.IsIntraday() {
		return Period{Forever: true}, nil
	}
	const secondsPerDay = 24 * 60 * 60
	barSeconds := uint64(parsed.Duration() / time.Second)
	totalSeconds := bars * barSeconds
	days := (totalSeconds + secondsPerDay - 1) / secondsPerDay
	return Period{Duration: time.Duration(days) * 24 * time.Hour}, nil
}

func isSessionSpace(spaceID string) bool {
	market, err := barclock.MarketFor(spaceID)
	return err == nil && market.Alignment == barclock.AlignmentSession
}

// ResolveViewMaxAge 返回 View 在 (spaceID, freq) 下生效的写入年龄上限及来源：
//  1. 显式空间例外（view.max_age.spaces[spaceID][freq]）最优先；
//  2. 有交易时段的空间（barclock 登记的 session 市场，如 stockcn/stockhk/stockus）直接取数据集保留期；
//  3. 其余空间优先取显式默认例外（view.max_age.defaults[freq]），未配置时按 view.bars 推导。
func (p Policy) ResolveViewMaxAge(spaceID, freq string) (Period, string, error) {
	if !frequencypkg.IsCanonical(freq) {
		return Period{}, "", fmt.Errorf("频率 %q 不是规范频率", freq)
	}
	if raw, ok := p.View.MaxAge.Spaces[spaceID][freq]; ok {
		period, err := ParsePeriod(raw)
		return period, SourceSpace, err
	}
	if isSessionSpace(spaceID) {
		period, _, err := p.Retention.Resolve(spaceID, freq)
		if err != nil {
			return Period{}, "", err
		}
		return period, SourceRetention, nil
	}
	if raw, ok := p.View.MaxAge.Defaults[freq]; ok {
		period, err := ParsePeriod(raw)
		return period, SourceDefault, err
	}
	period, err := DeriveViewMaxAge(p.View.Bars, freq)
	if err != nil {
		return Period{}, "", err
	}
	return period, SourceDerived, nil
}

// Validate reports whether the policy is complete and within limits.
func (p Policy) Validate() error {
	_, err := p.normalized()
	return err
}

// Coverage checks whether retention and view.max_age can supply view.bars by calendar time.
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
	checkAll := func(prefix string, retention Retention) {
		for _, freq := range sortedKeys(retention.Defaults) {
			check(prefix+".defaults."+freq, retention.Defaults[freq], freq, true)
		}
		spaces := make([]string, 0, len(retention.Spaces))
		for space := range retention.Spaces {
			spaces = append(spaces, space)
		}
		sort.Strings(spaces)
		for _, space := range spaces {
			for _, freq := range sortedKeys(retention.Spaces[space]) {
				check(prefix+".spaces."+space+"."+freq, retention.Spaces[space][freq], freq, false)
			}
		}
	}
	checkAll("retention", normalized.Retention)
	// 对 view.max_age 检查生效的默认值（显式覆盖优先，未覆盖的标准频率用推导值）以及所有空间例外，
	// 保证推导规则与显式覆盖都与 Coverage() 口径一致。
	effectiveDefaults := make(map[string]string, len(frequencypkg.StandardStrings())+len(normalized.View.MaxAge.Defaults))
	for _, freq := range frequencypkg.StandardStrings() {
		if raw, ok := normalized.View.MaxAge.Defaults[freq]; ok {
			effectiveDefaults[freq] = raw
			continue
		}
		derived, deriveErr := DeriveViewMaxAge(normalized.View.Bars, freq)
		if deriveErr != nil {
			errs = append(errs, fmt.Sprintf("view.max_age.defaults.%s: %v", freq, deriveErr))
			continue
		}
		effectiveDefaults[freq] = derived.String()
	}
	for freq, raw := range normalized.View.MaxAge.Defaults {
		effectiveDefaults[freq] = raw
	}
	checkAll("view.max_age", Retention{Defaults: effectiveDefaults, Spaces: normalized.View.MaxAge.Spaces})
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
	retention, err := p.Retention.normalized("retention", true)
	if err != nil {
		return Policy{}, err
	}
	if err := p.View.validate(); err != nil {
		return Policy{}, err
	}
	out := Policy{View: p.View, Retention: retention}
	if out.View.MaxAge, err = p.View.MaxAge.normalized("view.max_age", false); err != nil {
		return Policy{}, err
	}
	return out, nil
}

// normalized 校验并把每个期限渲染成规范形式；path 是错误信息里的配置路径前缀。
// requireStandardDefaults 为真时要求 defaults 覆盖全部标准频率（数据集保留期）；为假时只校验显式填写的例外项（View 最大保留期）。
func (r Retention) normalized(path string, requireStandardDefaults bool) (Retention, error) {
	var out Retention
	if requireStandardDefaults {
		out.Defaults = make(map[string]string, len(r.Defaults))
		for _, freq := range frequencypkg.StandardStrings() {
			if _, ok := r.Defaults[freq]; !ok {
				return Retention{}, fmt.Errorf("%s.defaults must cover every standard frequency; %q is missing", path, freq)
			}
		}
	} else if len(r.Defaults) > 0 {
		out.Defaults = make(map[string]string, len(r.Defaults))
	}
	for freq, raw := range r.Defaults {
		if !frequencypkg.IsCanonical(freq) {
			return Retention{}, fmt.Errorf("%s.defaults: %q is not a canonical frequency", path, freq)
		}
		period, err := ParsePeriod(raw)
		if err != nil {
			return Retention{}, fmt.Errorf("%s.defaults.%s: %w", path, freq, err)
		}
		out.Defaults[freq] = period.String()
	}
	if len(r.Spaces) > 0 {
		out.Spaces = make(map[string]map[string]string, len(r.Spaces))
	}
	for spaceID, overrides := range r.Spaces {
		if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(spaceID) != spaceID {
			return Retention{}, fmt.Errorf("%s.spaces: invalid space id %q", path, spaceID)
		}
		space := make(map[string]string, len(overrides))
		for freq, raw := range overrides {
			if !frequencypkg.IsCanonical(freq) {
				return Retention{}, fmt.Errorf("%s.spaces.%s: %q is not a canonical frequency", path, spaceID, freq)
			}
			period, err := ParsePeriod(raw)
			if err != nil {
				return Retention{}, fmt.Errorf("%s.spaces.%s.%s: %w", path, spaceID, freq, err)
			}
			space[freq] = period.String()
		}
		out.Spaces[spaceID] = space
	}
	return out, nil
}

func (v View) validate() error {
	if v.Bars == 0 || v.Bars > maxViewBars {
		return fmt.Errorf("view.bars must be between 1 and %d", maxViewBars)
	}
	interval, err := time.ParseDuration(strings.TrimSpace(v.MaintenanceCheckInterval))
	if err != nil || interval < 30*time.Second {
		return errors.New("view.maintenance_check_interval must be at least 30s")
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
