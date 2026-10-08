package storagepolicy

import (
	"fmt"
	"sort"
	"time"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
)

// SessionMinutes is the approximate trading time per weekday of the markets
// that do not trade around the clock. Other spaces trade 7×24.
var SessionMinutes = map[string]int{
	"stockcn": 240,
	"stockhk": 330,
	"stockus": 390,
}

// Coverage checks that every finite retention keeps at least view.bars bars,
// so a View rebuild finds the history it needs. A 7×24 space is checked
// exactly and a shortfall is an error. A trading-session space is estimated
// from SessionMinutes and five trading days a week, ignoring holidays, so a
// shortfall is only a warning.
func (p Policy) Coverage() (errs []string, warnings []string) {
	check := func(spaceID string, sessionMinutes int, freq string, raw string) {
		period, err := ParsePeriod(raw)
		if err != nil || period.Forever {
			return
		}
		frequency, err := frequencypkg.Parse(freq)
		if err != nil {
			return
		}
		bars := coveredBars(period.Duration, frequency, sessionMinutes)
		if bars >= float64(p.View.Bars) {
			return
		}
		where := "retention.defaults"
		if spaceID != "" {
			where = "retention.spaces." + spaceID
		}
		message := fmt.Sprintf("%s.%s = %s keeps about %.0f bars, fewer than view.bars %d", where, freq, period, bars, p.View.Bars)
		if sessionMinutes > 0 {
			warnings = append(warnings, message)
		} else {
			errs = append(errs, message)
		}
	}
	for _, freq := range sortedKeys(p.Retention.Defaults) {
		check("", 0, freq, p.Retention.Defaults[freq])
	}
	for _, spaceID := range sortedKeys(p.Retention.Spaces) {
		for _, freq := range sortedKeys(p.Retention.Spaces[spaceID]) {
			check(spaceID, SessionMinutes[spaceID], freq, p.Retention.Spaces[spaceID][freq])
		}
	}
	return errs, warnings
}

// coveredBars estimates how many bars of a frequency fit in a retention.
func coveredBars(retention time.Duration, frequency frequencypkg.Frequency, sessionMinutes int) float64 {
	bar := frequency.NominalDuration()
	if sessionMinutes <= 0 {
		return float64(retention) / float64(bar)
	}
	tradingDays := retention.Hours() / 24 * 5 / 7
	if bar >= 24*time.Hour {
		return tradingDays / (bar.Hours() / 24 * 5 / 7)
	}
	barsPerDay := float64(sessionMinutes) / bar.Minutes()
	if barsPerDay < 1 {
		barsPerDay = 1
	}
	return tradingDays * barsPerDay
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
