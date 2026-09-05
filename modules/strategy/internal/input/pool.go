package input

import (
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
)

type Subject struct {
	SubjectID    string
	InstrumentID string
	Exchange     string
	Market       string
	QuoteAsset   string
	SeriesTag    string
	Active       bool
}

type PoolResult struct {
	Items      []PoolItem
	Ineligible map[string]string
}

// BuildPool applies the compiled instrument-pool rule to active metadata and
// performs the minimum-history check without making history failures fatal.
func BuildPool(rule config.InstrumentPoolRule, subjects []Subject, history map[string]int) PoolResult {
	include, exclude := tokenSet(rule.Include), tokenSet(rule.Exclude)
	byInstrument := make(map[string]Subject)
	for _, subject := range subjects {
		if !subject.Active || strings.TrimSpace(subject.InstrumentID) == "" || !matches(rule, subject, include, exclude) {
			continue
		}
		current, exists := byInstrument[subject.InstrumentID]
		if !exists || venueRank(rule.Exchanges, subject.Exchange) < venueRank(rule.Exchanges, current.Exchange) || (venueRank(rule.Exchanges, subject.Exchange) == venueRank(rule.Exchanges, current.Exchange) && subject.SubjectID < current.SubjectID) {
			byInstrument[subject.InstrumentID] = subject
		}
	}
	result := PoolResult{Ineligible: map[string]string{}}
	for instrumentID, preferred := range byInstrument {
		selected := preferred
		eligible := rule.MinHistoryPeriods <= 0 || historyCount(history, preferred, instrumentID) >= rule.MinHistoryPeriods
		if !eligible {
			// Venue order is a preference, not a hard requirement: if the
			// preferred subject has no history, choose the next eligible venue.
			candidates := make([]Subject, 0)
			for _, subject := range subjects {
				if subject.Active && subject.InstrumentID == instrumentID && matches(rule, subject, include, exclude) && (rule.MinHistoryPeriods <= 0 || historyCount(history, subject, instrumentID) >= rule.MinHistoryPeriods) {
					candidates = append(candidates, subject)
				}
			}
			sort.Slice(candidates, func(i, j int) bool {
				ri, rj := venueRank(rule.Exchanges, candidates[i].Exchange), venueRank(rule.Exchanges, candidates[j].Exchange)
				if ri != rj {
					return ri < rj
				}
				return candidates[i].SubjectID < candidates[j].SubjectID
			})
			if len(candidates) == 0 {
				result.Ineligible[instrumentID] = "insufficient_history"
				continue
			}
			selected, eligible = candidates[0], true
		}
		if !eligible {
			result.Ineligible[instrumentID] = "insufficient_history"
			continue
		}
		result.Items = append(result.Items, PoolItem{InstrumentID: instrumentID, SubjectID: selected.SubjectID, Exchange: selected.Exchange, Market: selected.Market, QuoteAsset: selected.QuoteAsset, SeriesTag: selected.SeriesTag})
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].InstrumentID < result.Items[j].InstrumentID })
	return result
}

func historyCount(history map[string]int, subject Subject, instrumentID string) int {
	if count, ok := history[subject.SubjectID]; ok {
		return count
	}
	return history[instrumentID]
}

func matches(rule config.InstrumentPoolRule, subject Subject, include, exclude map[string]struct{}) bool {
	instrumentID := strings.ToUpper(strings.TrimSpace(subject.InstrumentID))
	if _, ok := exclude[instrumentID]; ok {
		return false
	}
	if rule.IncludeSet || len(include) > 0 {
		if _, ok := include[instrumentID]; !ok {
			return false
		}
	}
	if !containsFold(rule.Exchanges, subject.Exchange) && len(rule.Exchanges) > 0 {
		return false
	}
	if !containsFold(rule.Markets, subject.Market) && len(rule.Markets) > 0 {
		return false
	}
	if !containsFold(rule.QuoteAssets, subject.QuoteAsset) && len(rule.QuoteAssets) > 0 {
		return false
	}
	return true
}

func tokenSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[strings.ToUpper(strings.TrimSpace(value))] = struct{}{}
	}
	return result
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func venueRank(preferred []string, venue string) int {
	for i, value := range preferred {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(venue)) {
			return i
		}
	}
	return len(preferred) + 1
}
