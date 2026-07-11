package pipeline

import (
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type QualityPolicy struct {
	ProviderPriority          []marketdata.ProviderID
	AuthoritativeSingleSource bool
}
type QualityDecision struct {
	Row           *marketdata.ResolvedKline
	Events        []QualityEvent
	NoWriteReason string
}
type QualityEvent struct {
	Type        string
	ProviderIDs []marketdata.ProviderID
	Reason      string
}
type QualityResolver struct {
	Policy QualityPolicy
	Now    func() time.Time
}

func (r QualityResolver) Resolve(candidates []marketdata.ProviderKline, existing *marketdata.ResolvedKline) (QualityDecision, error) {
	valid := make([]marketdata.ProviderKline, 0, len(candidates))
	for _, candidate := range candidates {
		if err := candidate.Validate(); err != nil {
			return QualityDecision{}, fmt.Errorf("invalid candidate %s: %w", candidate.ProviderID, err)
		}
		valid = append(valid, candidate)
	}
	if len(valid) == 0 {
		return QualityDecision{NoWriteReason: "no_source_candidates"}, nil
	}
	priority := make(map[marketdata.ProviderID]int, len(r.Policy.ProviderPriority))
	for i, id := range r.Policy.ProviderPriority {
		priority[id] = i
	}
	sort.SliceStable(valid, func(i, j int) bool {
		pi, oki := priority[valid[i].ProviderID]
		pj, okj := priority[valid[j].ProviderID]
		if !oki {
			pi = len(priority) + 1
		}
		if !okj {
			pj = len(priority) + 1
		}
		if pi != pj {
			return pi < pj
		}
		if !valid[i].FetchedAt.Equal(valid[j].FetchedAt) {
			return valid[i].FetchedAt.After(valid[j].FetchedAt)
		}
		return valid[i].ProviderID < valid[j].ProviderID
	})
	winner := valid[0]
	status := "provisional"
	if r.Policy.AuthoritativeSingleSource || len(valid) > 1 {
		status = "confirmed"
	}
	revision := int64(1)
	resolvedAt := r.now()
	if r.Now == nil {
		resolvedAt = time.Now().UTC()
	}
	if existing != nil {
		revision = existing.Revision
		if sameBusinessRow(existing.ProviderKline, winner) && existing.QualityStatus == status {
			resolvedAt = existing.ResolvedAt
		} else {
			revision++
		}
	}
	row := &marketdata.ResolvedKline{ProviderKline: winner, QualityStatus: status, Revision: revision, ResolvedAt: resolvedAt}
	return QualityDecision{Row: row, Events: []QualityEvent{{Type: "kline_resolved", ProviderIDs: providerIDs(valid), Reason: status}}}, nil
}
func (r QualityResolver) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
func sameBusinessRow(left, right marketdata.ProviderKline) bool {
	return left.ProviderID == right.ProviderID && left.SubjectID == right.SubjectID && left.Frequency == right.Frequency && left.DataTime.Equal(right.DataTime) && left.Open.Cmp(right.Open) == 0 && left.High.Cmp(right.High) == 0 && left.Low.Cmp(right.Low) == 0 && left.Close.Cmp(right.Close) == 0 && decimalEqual(left.Volume, right.Volume) && decimalEqual(left.Amount, right.Amount)
}
func decimalEqual(left, right *marketdata.Decimal) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Cmp(*right) == 0
}
func providerIDs(rows []marketdata.ProviderKline) []marketdata.ProviderID {
	ids := make([]marketdata.ProviderID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ProviderID)
	}
	return ids
}
