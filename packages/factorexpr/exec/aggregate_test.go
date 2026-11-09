package exec

import (
	"context"
	"math"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// aggregatePanel 是一个时刻、两个 series_tag 的面板：venue:a 有 A、B、C、D，其中 C 的输入为 NaN，D 这一刻没有源行；
// venue:b 有 A、E。
func aggregatePanel(t *testing.T) *Panel {
	t.Helper()
	keys := []SeriesKey{{"A", "venue:a"}, {"B", "venue:a"}, {"C", "venue:a"}, {"D", "venue:a"}, {"A", "venue:b"}, {"E", "venue:b"}}
	panel, err := NewPanel(1, []string{"x"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	values := []float64{1, 3, math.NaN(), math.NaN(), 10, math.NaN()}
	for i, key := range keys {
		s, _ := panel.SeriesIndex(key)
		panel.Set(0, s, 0, values[i])
		if key.Subject != "D" {
			panel.MarkPresent(s, 0)
		}
	}
	return panel
}

func valueOf(t *testing.T, panel *Panel, result *Result, column int, subject, tag string) float64 {
	t.Helper()
	s, ok := panel.SeriesIndex(SeriesKey{Subject: subject, Tag: tag})
	if !ok {
		t.Fatalf("no series %s/%s", subject, tag)
	}
	return result.Value(column, s, 0)
}

// 聚合广播：统计量写给同一截面的每一行，包括输入为 NaN 的行（C）与这一刻没有源行的序列（D）；不同 series_tag 分别统计。
func TestBroadcastWritesEveryRow(t *testing.T) {
	panel := aggregatePanel(t)
	program := compileProgram(t, map[string]string{"m": "cs_mean(x)"})
	result := run(t, program, panel, 0, 0, 1)
	for _, subject := range []string{"A", "B", "C", "D"} {
		if got := valueOf(t, panel, result, 0, subject, "venue:a"); got != 2 {
			t.Errorf("venue:a %s: cs_mean = %v, want 2", subject, got)
		}
	}
	for _, subject := range []string{"A", "E"} {
		if got := valueOf(t, panel, result, 0, subject, "venue:b"); got != 10 {
			t.Errorf("venue:b %s: cs_mean = %v, want 10", subject, got)
		}
	}
	// 逐行变换类对照：NaN 行仍为 NaN。
	rank := run(t, compileProgram(t, map[string]string{"r": "cs_rank(x)"}), panel, 0, 0, 1)
	if got := valueOf(t, panel, rank, 0, "C", "venue:a"); !math.IsNaN(got) {
		t.Fatalf("cs_rank on NaN row = %v, want NaN", got)
	}
}

// 成员标的范围之外的行为 NaN，范围之内的统计只用范围内的行。
func TestBroadcastRespectsUniverse(t *testing.T) {
	panel := aggregatePanel(t)
	factor, err := factorexpr.Compile("output: cs_sum(x)\n")
	if err != nil {
		t.Fatal(err)
	}
	program, err := factorexpr.NewProgram([]factorexpr.ProgramInput{{FactorID: "s", Factor: factor, Universe: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	mask := make([]bool, len(panel.Series()))
	for i, key := range panel.Series() {
		mask[i] = key.Subject != "B"
	}
	result, err := Run(context.Background(), program, panel, Options{Start: 0, End: 0, Universes: map[string][]bool{"u": mask}})
	if err != nil {
		t.Fatal(err)
	}
	if got := valueOf(t, panel, result, 0, "B", "venue:a"); !math.IsNaN(got) {
		t.Fatalf("out-of-universe row = %v, want NaN", got)
	}
	for _, subject := range []string{"A", "C", "D"} {
		if got := valueOf(t, panel, result, 0, subject, "venue:a"); got != 1 {
			t.Errorf("venue:a %s: cs_sum = %v, want 1", subject, got)
		}
	}
}

// cs_ref：参考标的在本组里取它的值广播；参考标的不在组里、或它的值为 NaN 时整组为 NaN。
func TestBroadcastReference(t *testing.T) {
	panel := aggregatePanel(t)
	program := compileProgram(t, map[string]string{"b": `cs_ref(x, "B")`, "e": `cs_ref(x, "E")`, "a": `cs_ref(x, "A")`})
	result := run(t, program, panel, 0, 0, 1)
	b, e, a := columnByName(result, "b"), columnByName(result, "e"), columnByName(result, "a")
	for _, subject := range []string{"A", "B", "C", "D"} {
		if got := valueOf(t, panel, result, b, subject, "venue:a"); got != 3 {
			t.Errorf("cs_ref B at %s = %v, want 3", subject, got)
		}
		if got := valueOf(t, panel, result, a, subject, "venue:a"); got != 1 {
			t.Errorf("cs_ref A at venue:a %s = %v, want 1", subject, got)
		}
		// E 只在 venue:b 里，venue:a 整组为 NaN。
		if got := valueOf(t, panel, result, e, subject, "venue:a"); !math.IsNaN(got) {
			t.Errorf("cs_ref E at venue:a %s = %v, want NaN", subject, got)
		}
	}
	for _, subject := range []string{"A", "E"} {
		// venue:b 里 E 的值是 NaN：整组为 NaN；A 在 venue:b 里取 venue:b 的 A。
		if got := valueOf(t, panel, result, e, subject, "venue:b"); !math.IsNaN(got) {
			t.Errorf("cs_ref E at venue:b %s = %v, want NaN", subject, got)
		}
		if got := valueOf(t, panel, result, a, subject, "venue:b"); got != 10 {
			t.Errorf("cs_ref A at venue:b %s = %v, want 10", subject, got)
		}
	}
	// 两个参考标的不同的 cs_ref 不能被去重成同一个节点。
	refs := 0
	for _, node := range program.Nodes {
		if node.Op == "cs_ref" {
			refs++
		}
	}
	if refs != 3 {
		t.Fatalf("program has %d cs_ref nodes, want 3", refs)
	}
}
