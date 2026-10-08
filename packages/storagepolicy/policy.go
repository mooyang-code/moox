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
	DefaultViewBars = 5000

	maxBars = 1_000_000
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
	raw, ok := r.Defaults[freq]
	if !ok {
		return Period{}, "", fmt.Errorf("no retention for frequency %q", freq)
	}
	period, err := ParsePeriod(raw)
	return period, SourceDefault, err
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
			"1m": minute, "5m": "240d", "15m": "730d",
			"30m": Forever, "1h": Forever, "4h": Forever,
		}
	}
	policy := Policy{
		Retention: Retention{
			Defaults: map[string]string{
				"30s": "48h", "1m": "7d", "5m": "30d", "15m": "90d", "30m": "180d",
				"1h": "365d", "4h": "1095d", "1d": Forever, "1w": Forever, "1mo": Forever,
			},
			Spaces: map[string]map[string]string{
				"stockcn": sessionMarket("45d"),
				"stockhk": sessionMarket("30d"),
				"stockus": sessionMarket("30d"),
				"mooxsys": {"30s": "48h", "1m": "180d"},
			},
		},
		View: View{
			Bars:                     DefaultViewBars,
			TrimBars:                 TrimBarsFor(DefaultViewBars),
			MaintenanceCheckInterval: "1m",
			CapacityCheckInterval:    "1h",
			CapacityCheckJitter:      "1h",
			MaxViewFileBytes:         1 << 30,
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

// normalized validates the policy and renders every period canonically.
func (p Policy) normalized() (Policy, error) {
	out := Policy{View: p.View, Retention: Retention{Defaults: map[string]string{}}}
	for _, freq := range frequencypkg.Strings() {
		raw, ok := p.Retention.Defaults[freq]
		if !ok {
			return Policy{}, fmt.Errorf("retention.defaults must cover every frequency; %q is missing", freq)
		}
		period, err := ParsePeriod(raw)
		if err != nil {
			return Policy{}, fmt.Errorf("retention.defaults.%s: %w", freq, err)
		}
		out.Retention.Defaults[freq] = period.String()
	}
	for freq := range p.Retention.Defaults {
		if !frequencypkg.IsCanonical(freq) {
			return Policy{}, fmt.Errorf("retention.defaults: %q is not a canonical frequency", freq)
		}
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
	if v.Bars == 0 || v.Bars > maxBars {
		return fmt.Errorf("view.bars must be between 1 and %d", maxBars)
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
