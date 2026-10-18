package engine

import "testing"

// 启用后的第一根 ok bar 上建立全部批次（不再等各自的 offset 才建仓、空仓一个周期），建仓序号按各自的 offset 对齐，
// 之后各批次在自己的 offset 上轮换。
func TestHoldingBuildsAllBatchesOnFirstOkBar(t *testing.T) {
	program := compile(t, `name: staged
rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 1}
    holding: {bars: 24, offsets: [0, 6, 12, 18]}
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "close")
	rows := map[string]Row{"A": values("close", 1), "B": values("close", 2)}
	frame := frameOf(program, rows)
	frame.BarIndex = 3
	first := evaluate(t, program, frame, State{})
	assertOK(t, first)
	assertWeights(t, first, map[string]string{"B": "1"})
	want := map[int]int64{0: 0, 6: -18, 12: -12, 18: -6}
	batches := first.State.Rules["r"].Batches
	if len(batches) != 4 {
		t.Fatalf("第一根 ok bar 应建立全部 4 个批次：%+v", batches)
	}
	for _, batch := range batches {
		if want[batch.Offset] != batch.EstablishedBar || batch.BaseWeights["B"] != "1" {
			t.Fatalf("批次 %d 的建仓序号应对齐到 %d：%+v", batch.Offset, want[batch.Offset], batch)
		}
	}
	// 下一根不在任何 offset 上、也没有批次到期：只延续，不重建。
	rows["A"], rows["B"] = values("close", 3), values("close", 2)
	frame = frameOf(program, rows)
	frame.BarIndex = 4
	next := evaluate(t, program, frame, first.State)
	assertOK(t, next)
	assertWeights(t, next, map[string]string{"B": "1"})
	// offset 6 上按本期名次轮换这一批。
	frame.BarIndex = 6
	rotated := evaluate(t, program, frame, next.State)
	assertOK(t, rotated)
	assertWeights(t, rotated, map[string]string{"A": "0.25", "B": "0.75"})
}
