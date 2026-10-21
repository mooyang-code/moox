package marketdata

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
)

// Frequency is a canonical bar length; exchange API codes stay inside the
// source adapters.
type Frequency = frequency.Frequency

const (
	FrequencyMinute = frequency.Minute1
	Frequency5Min   = frequency.Minute5
	Frequency15Min  = frequency.Minute15
	Frequency30Min  = frequency.Minute30
	FrequencyHour   = frequency.Hour1
	FrequencyDay    = frequency.Day1
	FrequencyWeek   = frequency.Week1
	FrequencyMonth  = frequency.Month1
)

// klineFrequencies are the bar lengths market K-line collection supports.
var klineFrequencies = map[Frequency]struct{}{
	FrequencyMinute: {}, Frequency5Min: {}, Frequency15Min: {}, Frequency30Min: {},
	FrequencyHour: {}, FrequencyDay: {}, FrequencyWeek: {}, FrequencyMonth: {},
}

// ParseFrequency returns the canonical K-line frequency for input, accepting
// the aliases packages/frequency knows (for example "1H" and "1M").
func ParseFrequency(input string) (Frequency, error) {
	parsed, err := frequency.Parse(input)
	if err != nil {
		return "", err
	}
	if _, ok := klineFrequencies[parsed]; !ok {
		return "", fmt.Errorf("unsupported K-line frequency %q", input)
	}
	return parsed, nil
}

// SupportsStream 判断该频率能否由 WS 推送按固定步长切周期（月线、周线等日历频率不行）。
func SupportsStream(f Frequency) bool {
	step := f.Duration()
	return step > 0 && step <= 24*time.Hour
}
