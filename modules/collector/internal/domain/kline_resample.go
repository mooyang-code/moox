package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
)

// MaxResampleBackfillBuckets 是单个历史合成作业最多覆盖的目标桶数。
const MaxResampleBackfillBuckets = 10080

// CanonicalJSON persists the normalized public collect-params contract.
func (p *CollectParams) CanonicalJSON() (string, error) {
	if p == nil {
		return "", fmt.Errorf("collect params are required")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal collect params: %w", err)
	}
	return string(raw), nil
}

// normalizeFrequency returns the canonical frequency for raw, or raw itself
// so validation can report an unsupported value.
func normalizeFrequency(raw string) string {
	parsed, err := frequency.Parse(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return string(parsed)
}

// ResampleFrequencyDuration returns the bar length of a frequency that can be
// resampled: a fixed whole-minute bar from 1m to 1d. Weeks and months are
// calendar-aligned, and epoch_utc buckets cannot represent them.
func ResampleFrequencyDuration(raw string) (time.Duration, error) {
	parsed, err := frequency.Parse(raw)
	if err != nil {
		return 0, err
	}
	if parsed.Duration() < time.Minute || parsed.Duration() > 24*time.Hour {
		return 0, fmt.Errorf("frequency %q cannot be resampled; use 1m to 1d", raw)
	}
	return parsed.Duration(), nil
}
