package replay

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
)

// presence 记录每个标的在哪些 bar（按日历序号）有行，用于回放中的年龄判定。
// 判定与实时探针一致：目标是 T − (N−1) 根，向前容忍两根，三根中任一有行即满足。
type presence struct {
	indexes map[string]map[int64]struct{}
	cache   map[int64]int64
}

func newPresence() *presence {
	return &presence{indexes: make(map[string]map[int64]struct{}), cache: make(map[int64]int64)}
}

// add 登记一段读取到的行。
func (p *presence) add(resolved input.Resolved, rows input.RangeRows) error {
	for unix, bySubject := range rows.Bars {
		index, ok := p.cache[unix]
		if !ok {
			for _, row := range bySubject {
				boundary, err := input.FromStorageStart(resolved.Calendar, resolved.Bar, row.DataTime)
				if err != nil {
					return err
				}
				index = boundary.BarIndex
				break
			}
			p.cache[unix] = index
		}
		for subjectID := range bySubject {
			if p.indexes[subjectID] == nil {
				p.indexes[subjectID] = make(map[int64]struct{})
			}
			p.indexes[subjectID][index] = struct{}{}
		}
	}
	return nil
}

// probe 返回本期的年龄探针。
func (p *presence) probe(resolved input.Resolved, bar input.PeriodBoundaries, instrumentOf map[string]string) input.AgeProbe {
	return func(_ context.Context, instruments []string) (map[string]struct{}, error) {
		wanted := make(map[string]struct{}, len(instruments))
		for _, id := range instruments {
			wanted[id] = struct{}{}
		}
		target := bar.BarIndex - int64(resolved.MinAgeBars-1)
		satisfied := make(map[string]struct{}, len(instruments))
		for subjectID, indexes := range p.indexes {
			instrument := instrumentOf[subjectID]
			if _, ok := wanted[instrument]; !ok {
				continue
			}
			for offset := int64(0); offset <= 2; offset++ {
				if _, ok := indexes[target-offset]; ok {
					satisfied[instrument] = struct{}{}
					break
				}
			}
		}
		return satisfied, nil
	}
}
