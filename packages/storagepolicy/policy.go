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

	// systemSpace 是 MooX 系统数据空间，保留期不受 view.bars 覆盖校验约束。
	systemSpace = "mooxsys"

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
	// MaxAge 是 View 按空间 × 频率保留的最长历史：data_time 早于“当前时间 − 最长保留期”的行不写入 View，
	// 补采、导入和因子历史重算带来的旧行同样丢弃。Dataset 与因子本地存储不受影响。
	MaxAge Retention `json:"max_age"`
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

// Default returns the recommended policy for an online quant system: 7×24
// markets as the default, trading-session markets keep more calendar days
// for the same number of bars, daily and longer bars are kept forever.
func Default() Policy {
	sessionMarket := func(minute string) map[string]string {
		return map[string]string{
			"1m": minute, "5m": "300d", "15m": "900d",
			"30m": Forever, "1h": Forever, "4h": Forever,
		}
	}
	policy := Policy{
		Retention: Retention{
			Defaults: map[string]string{
				"30s": "90h", "1m": "35d", "5m": "35d", "15m": "105d", "30m": "210d",
				"1h": "420d", "4h": "1700d", "1d": Forever, "1w": Forever, "1mo": Forever,
			},
			Spaces: map[string]map[string]string{
				"stockcn": sessionMarket("60d"),
				"stockhk": sessionMarket("45d"),
				"stockus": sessionMarket("40d"),
				// 主机资源历史（1m）只保留短期：排障看最近几天，长期趋势不靠它。
				"mooxsys": {"30s": "48h", "1m": "7d"},
			},
		},
		View: View{
			MaxAge:                   defaultViewMaxAge(),
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

// defaultViewMaxAge 是 View 最长保留期的推荐值：恰好装下 DefaultViewBars 根（7×24 市场精确，交易时段市场按日历估算），
// 与 Dataset 保留期的区别只是 1m 不必留到因子的最大回看（35d），View 只服务截面与展示。
func defaultViewMaxAge() Retention {
	sessionMarket := func(minute string) map[string]string {
		return map[string]string{
			"1m": minute, "5m": "300d", "15m": "900d",
			"30m": Forever, "1h": Forever, "4h": Forever,
		}
	}
	return Retention{
		Defaults: map[string]string{
			"30s": "90h", "1m": "7d", "5m": "35d", "15m": "105d", "30m": "210d",
			"1h": "420d", "4h": "1700d", "1d": Forever, "1w": Forever, "1mo": Forever,
		},
		Spaces: map[string]map[string]string{
			"stockcn": sessionMarket("60d"),
			"stockhk": sessionMarket("45d"),
			"stockus": sessionMarket("40d"),
			"mooxsys": {"30s": "48h", "1m": "7d"},
		},
	}
}

// Validate reports whether the policy is complete and within limits.
func (p Policy) Validate() error {
	_, err := p.normalized()
	return err
}

// Coverage checks whether retention and view.max_age can supply view.bars. Defaults and 7×24
// markets have a deterministic rate; session markets are calendar estimates
// and produce warnings rather than rejecting an otherwise valid policy.
func (p Policy) Coverage() (errs, warnings []string) {
	normalized, err := p.normalized()
	if err != nil {
		return []string{err.Error()}, nil
	}
	check := func(path, raw, freq, space string) {
		period, _ := ParsePeriod(raw)
		if period.Forever {
			return
		}
		bars, estimated := barsWithin(period, freq, space)
		if bars >= float64(normalized.View.Bars) {
			return
		}
		message := fmt.Sprintf("%s = %s provides approximately %.0f bars, fewer than view.bars = %d", path, raw, bars, normalized.View.Bars)
		if estimated {
			warnings = append(warnings, message)
		} else {
			errs = append(errs, message)
		}
	}
	checkAll := func(prefix string, retention Retention) {
		for _, freq := range sortedKeys(retention.Defaults) {
			check(prefix+".defaults."+freq, retention.Defaults[freq], freq, "")
		}
		spaces := make([]string, 0, len(retention.Spaces))
		for space := range retention.Spaces {
			spaces = append(spaces, space)
		}
		sort.Strings(spaces)
		for _, space := range spaces {
			// 系统数据空间（主机资源历史）的保留期独立于 K 线：只保留短期供排障，不要求装满 View 的根数。
			if space == systemSpace {
				continue
			}
			for _, freq := range sortedKeys(retention.Spaces[space]) {
				check(prefix+".spaces."+space+"."+freq, retention.Spaces[space][freq], freq, space)
			}
		}
	}
	checkAll("retention", normalized.Retention)
	checkAll("view.max_age", normalized.View.MaxAge)
	return errs, warnings
}

// barsWithin 估算保留期能装下多少根 bar。7×24 市场按固定速率精确计算；交易时段市场（股票）按日历估算，estimated 为真。
func barsWithin(period Period, freq, space string) (bars float64, estimated bool) {
	f, _ := frequencypkg.Parse(freq)
	barLength := f.Duration()
	if barLength == 0 {
		barLength = 31 * 24 * time.Hour
	}
	bars = float64(period.Duration) / float64(barLength)
	minutes := map[string]float64{"stockcn": 240, "stockhk": 330, "stockus": 390}[space]
	if minutes > 0 {
		if barLength < 24*time.Hour {
			bars *= minutes / (24 * 60) * 5 / 7
		} else if barLength == 24*time.Hour {
			bars *= 5.0 / 7
		}
	}
	return bars, minutes > 0
}

// RetentionBars 返回空间 × 频率的保留期大约能装下多少根 bar；永久保留时 forever 为真。
// 保留期要覆盖 View 的根数，也要大于因子的最大回看根数（引擎要从 Storage 预载这么多历史）。
func (p Policy) RetentionBars(spaceID, freq string) (bars float64, forever, estimated bool, err error) {
	period, _, err := p.Retention.Resolve(spaceID, freq)
	if err != nil {
		return 0, false, false, err
	}
	if period.Forever {
		return 0, true, false, nil
	}
	bars, estimated = barsWithin(period, freq, spaceID)
	return bars, false, estimated, nil
}

// MinRetentionFor 返回装下 bars 根 bar 至少需要的保留期（7×24 市场精确，交易时段市场按日历估算折回自然时间），向上取整到整小时。
func MinRetentionFor(spaceID, freq string, bars int) Period {
	one, _ := barsWithin(Period{Duration: time.Hour * 24 * 365}, freq, spaceID)
	if one <= 0 {
		return Period{}
	}
	perYear := time.Hour * 24 * 365
	need := time.Duration(float64(perYear) * float64(bars) / one)
	return Period{Duration: (need + time.Hour - 1) / time.Hour * time.Hour}
}

// normalized validates the policy and renders every period canonically.
func (p Policy) normalized() (Policy, error) {
	retention, err := p.Retention.normalized("retention")
	if err != nil {
		return Policy{}, err
	}
	if err := p.View.validate(); err != nil {
		return Policy{}, err
	}
	out := Policy{View: p.View, Retention: retention}
	if out.View.MaxAge, err = p.View.MaxAge.normalized("view.max_age"); err != nil {
		return Policy{}, err
	}
	return out, nil
}

// normalized 校验并把每个期限渲染成规范形式；path 是错误信息里的配置路径前缀。
func (r Retention) normalized(path string) (Retention, error) {
	out := Retention{Defaults: map[string]string{}}
	for _, freq := range frequencypkg.StandardStrings() {
		if _, ok := r.Defaults[freq]; !ok {
			return Retention{}, fmt.Errorf("%s.defaults must cover every standard frequency; %q is missing", path, freq)
		}
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
