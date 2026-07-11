package rebalance

import (
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"sort"
)

var ErrInvalidRebalance = errors.New("trade: invalid rebalance")

type Target struct {
	Symbol   string
	Quantity shared.Decimal
}
type Current struct {
	Symbol   string
	Quantity shared.Decimal
}
type Action string

const (
	Close    Action = "CLOSE"
	Reduce   Action = "REDUCE"
	Open     Action = "OPEN"
	Increase Action = "INCREASE"
	Reverse  Action = "REVERSE"
)

type Leg struct {
	Sequence   int
	Symbol     string
	Action     Action
	Quantity   shared.Decimal
	ReduceOnly bool
	DependsOn  []int
}
type Planner struct{}
type TargetMode string

const (
	FullTarget  TargetMode = "FULL"
	PatchTarget TargetMode = "PATCH"
)

func (Planner) Build(targets []Target, currents []Current) ([]Leg, error) {
	return (Planner{}).BuildMode(FullTarget, targets, currents)
}
func (Planner) BuildMode(mode TargetMode, targets []Target, currents []Current) ([]Leg, error) {
	if mode != FullTarget && mode != PatchTarget {
		return nil, ErrInvalidRebalance
	}
	cm := map[string]shared.Decimal{}
	for _, c := range currents {
		cm[c.Symbol] = c.Quantity
	}
	tm := map[string]shared.Decimal{}
	for _, t := range targets {
		if t.Symbol == "" {
			return nil, ErrInvalidRebalance
		}
		tm[t.Symbol] = t.Quantity
	}
	symbolSet := map[string]bool{}
	for s := range tm {
		symbolSet[s] = true
	}
	if mode == FullTarget {
		for s := range cm {
			symbolSet[s] = true
		}
	}
	symbols := make([]string, 0, len(symbolSet))
	for s := range symbolSet {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	var first, second []Leg
	for _, s := range symbols {
		cur, tgt := cm[s], tm[s]
		delta := tgt.Sub(cur)
		if delta.IsZero() {
			continue
		}
		a := Increase
		reduce := false
		if cur.IsZero() {
			a = Open
		} else if tgt.IsZero() {
			a = Close
			reduce = true
		} else if cur.IsNegative() != tgt.IsNegative() {
			first = append(first, Leg{Symbol: s, Action: Close, Quantity: cur.Abs(), ReduceOnly: true})
			second = append(second, Leg{Symbol: s, Action: Open, Quantity: tgt.Abs(), ReduceOnly: false})
			continue
		} else if tgt.Abs().Cmp(cur.Abs()) < 0 {
			a = Reduce
			reduce = true
		}
		l := Leg{Symbol: s, Action: a, Quantity: delta.Abs(), ReduceOnly: reduce}
		if reduce {
			first = append(first, l)
		} else {
			second = append(second, l)
		}
	}
	out := append(first, second...)
	barrier := make([]int, len(first))
	for i := range first {
		barrier[i] = i + 1
	}
	for i := range out {
		out[i].Sequence = i + 1
		if i >= len(first) {
			out[i].DependsOn = append([]int(nil), barrier...)
		}
	}
	return out, nil
}
