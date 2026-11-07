package trigger

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
)

// heldRecorder 记下处理器每次装配输入时传进来的参数。
type heldRecorder struct {
	*fakeLoader
	bars []input.Bar
}

func (l *heldRecorder) LoadBar(ctx context.Context, spaceID string, resolved input.Resolved, program *dsl.Program, bar input.Bar) (input.Loaded, error) {
	l.bars = append(l.bars, bar)
	return l.fakeLoader.LoadBar(ctx, spaceID, resolved, program, bar)
}

func heldSet(market string, ids ...string) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for _, id := range ids {
		if out[market] == nil {
			out[market] = map[string]struct{}{}
		}
		out[market][id] = struct{}{}
	}
	return out
}

// 处理器把上期状态里的持仓传进输入装配（判断因子失败比例时持仓豁免年龄剔除）：没有上期状态时为空；中间一期被跳过时，
// 仍是最近一条带状态的结果里的持仓。
func TestHandlerPassesPreviousHoldingsToLoadBar(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	rec := &heldRecorder{fakeLoader: h.loader}
	h.handler.Loader = rec
	h.frame(2, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 2}, "C": {"m": 1}})
	expectAck(t, h.deliver(2))
	if len(rec.bars) != 1 || len(rec.bars[0].Held) != 0 {
		t.Fatalf("首期没有上期状态，Held 应为空：%+v", rec.bars)
	}
	// 第 3 期因子被跳过（不装配），第 4 期的 Held 仍是第 2 期的持仓。
	h.now = start.Add(time.Hour)
	message, payload := h.event(3, map[string]string{"f": "h1"}, "skipped")
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	h.now = start.Add(2 * time.Hour)
	h.frame(4, map[string]map[string]float64{"A": {"m": 1}, "B": {"m": 2}, "C": {"m": 3}})
	expectAck(t, h.deliver(4))
	if len(rec.bars) != 2 || !reflect.DeepEqual(rec.bars[1].Held, heldSet("spot", "A", "B")) {
		t.Fatalf("第 4 期应带第 2 期的持仓：%+v", rec.bars)
	}
}

// 策略池：每个成员装配输入时拿到的是自己的上期持仓，不是池的状态。
func TestPoolMembersLoadBarWithTheirOwnHoldings(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	rec := &heldRecorder{fakeLoader: h.loader}
	h.handler.Loader = rec
	h.warm(barStart(1), "")
	h.frame(2, abcFrame(3, 2, 1))
	expectAck(t, h.deliverWith(2, bothFactors))
	h.now = start.Add(time.Hour)
	h.frame(3, map[string]map[string]float64{"A": {"m": 1}, "B": {"m": 0.5}, "C": {"m": 3}, "D": {"m": 4}, "E": {"m": 5}})
	expectAck(t, h.deliverWith(3, map[string]string{"f": "h1"}))
	h.now = start.Add(2 * time.Hour)
	h.frame(4, map[string]map[string]float64{"A": {"m": 1}, "B": {"m": 0.5}, "C": {"m": 3}, "D": {"m": 4}, "E": {"m": 5}})
	rec.bars = nil
	expectAck(t, h.deliverWith(4, bothFactors))
	if len(rec.bars) != 2 {
		t.Fatalf("第 4 期两个成员各装配一次：%+v", rec.bars)
	}
	if !reflect.DeepEqual(rec.bars[0].Held, heldSet("spot", "D", "E")) || !reflect.DeepEqual(rec.bars[1].Held, heldSet("spot", "A", "B")) {
		t.Fatalf("成员应各用自己的上期持仓：a=%v b=%v", rec.bars[0].Held, rec.bars[1].Held)
	}
}
