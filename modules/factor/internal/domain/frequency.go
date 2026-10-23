package domain

import (
	"fmt"
	"time"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
)

// ParseFrequency 返回因子支持的规范频率的时长：1m～1d 的定长周期（含合成出来的 45m、2h 等整除一天的周期）。
// 别名（如 60m、1H）不接受，因子只认规范写法。
func ParseFrequency(value string) (time.Duration, error) {
	if !frequencypkg.IsCanonical(value) {
		return 0, fmt.Errorf("unsupported factor frequency %q", value)
	}
	duration := frequencypkg.Frequency(value).Duration()
	if duration < time.Minute || duration > 24*time.Hour {
		return 0, fmt.Errorf("unsupported factor frequency %q", value)
	}
	return duration, nil
}
