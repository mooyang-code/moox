package stream

import (
	"sort"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
)

// suspectAfterMisses 是判定疑似下架所需的连续缺失周期数（只看 1m，1h 跟随 1m 的判定，否则要等 3 小时）。
const suspectAfterMisses = 3

// delistTracker 按市场统计每个标的连续几个 1m 周期没有收盘推送，达到阈值或交易所推送状态变化时上报一次疑似下架。
type delistTracker struct {
	market  string
	report  func(Suspect)
	mu      sync.Mutex
	misses  map[string]int
	flagged map[string]bool
	planned map[string]bool
}

func newDelistTracker(market string, report func(Suspect)) *delistTracker {
	return &delistTracker{market: market, report: report, misses: map[string]int{}, flagged: map[string]bool{}, planned: map[string]bool{}}
}

// setPlan 记下计划里的标的；计划不再包含的标的清掉计数与标记（下架已生效）。
func (t *delistTracker) setPlan(plan *marketfetch.Plan) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.planned = map[string]bool{}
	if plan != nil {
		for _, dataset := range plan.Datasets {
			for _, copy := range dataset.Copies {
				for _, subject := range copy.Subjects {
					t.planned[subject.ID] = true
				}
			}
		}
	}
	for subject := range t.flagged {
		if !t.planned[subject] {
			delete(t.flagged, subject)
			delete(t.misses, subject)
		}
	}
}

// observe 用一期的统计更新连续缺失计数，并把已判定的标的数写进统计的 Suspected。
func (t *delistTracker) observe(stat *PeriodStat) {
	t.mu.Lock()
	missing := map[string]bool{}
	for _, subject := range stat.MissingSubjects {
		missing[subject] = true
	}
	var reports []Suspect
	if stat.Frequency == "1m" && stat.Arrived > 0 {
		// 整期零到达（断线、停机）不能说明是哪个标的下架了。
		for subject := range t.misses {
			if !missing[subject] {
				delete(t.misses, subject)
				delete(t.flagged, subject)
			}
		}
		subjects := append([]string(nil), stat.MissingSubjects...)
		sort.Strings(subjects)
		for _, subject := range subjects {
			t.misses[subject]++
			if t.misses[subject] >= suspectAfterMisses && !t.flagged[subject] {
				t.flagged[subject] = true
				reports = append(reports, Suspect{Market: t.market, SubjectID: subject, Reason: "连续 3 个 1m 周期没有收盘推送"})
			}
		}
	}
	for subject := range missing {
		if t.flagged[subject] {
			stat.Suspected++
		}
	}
	t.mu.Unlock()
	for _, suspect := range reports {
		if t.report != nil {
			t.report(suspect)
		}
	}
}

// flag 由交易所的状态推送直接判定。
func (t *delistTracker) flag(subject, reason string) {
	t.mu.Lock()
	already := t.flagged[subject]
	t.flagged[subject] = true
	t.mu.Unlock()
	if !already && t.report != nil {
		t.report(Suspect{Market: t.market, SubjectID: subject, Reason: reason})
	}
}
