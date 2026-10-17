package factorexpr

import (
	"strings"
	"testing"
)

func program(t *testing.T, specs ...string) *Program {
	t.Helper()
	inputs := make([]ProgramInput, len(specs))
	for i, text := range specs {
		inputs[i] = ProgramInput{FactorID: "f" + string(rune('a'+i)), Factor: mustCompile(t, text)}
	}
	p, err := NewProgram(inputs)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func countOps(p *Program, op string) int {
	count := 0
	for _, node := range p.Nodes {
		if node.Op == op {
			count++
		}
	}
	return count
}

func TestProgramDeduplicatesAcrossFactors(t *testing.T) {
	p := program(t,
		"outputs:\n  a: close / ts_mean(close, 20)\n",
		"outputs:\n  b: ts_rank(close / ts_mean(close, 20) - 1, 20)\n",
		"outputs:\n  c: ts_mean(close, 5) * 2\n  d: ts_mean(open, 20) * 2\n",
	)
	if got := countOps(p, "ts_mean"); got != 3 {
		t.Fatalf("ts_mean nodes = %d, want 3 (close/20 shared, close/5, open/20)", got)
	}
	if got := countOps(p, "div"); got != 1 {
		t.Fatalf("div nodes = %d, want 1", got)
	}
	if got := countOps(p, OpConst); got != 2 {
		t.Fatalf("const nodes = %d, want 2 (1 and 2)", got)
	}
	if got := countOps(p, OpColumn); got != 2 {
		t.Fatalf("column nodes = %d, want 2", got)
	}
	if strings.Join(p.Columns, ",") != "close,open" || p.Lookback != 39 {
		t.Fatalf("columns = %v, lookback = %d", p.Columns, p.Lookback)
	}
	if len(p.Outputs) != 4 || p.Outputs[0].FactorID != "fa" || p.Outputs[3].Name != "d" {
		t.Fatalf("outputs = %+v", p.Outputs)
	}
	// a 的根节点 div 同时被 b 引用：a 输出的就是 b 内部的那个节点。
	if p.Nodes[p.Outputs[0].Node].Op != "div" {
		t.Fatalf("output a node = %+v", p.Nodes[p.Outputs[0].Node])
	}
}

func TestProgramTopologicalOrderAndLastUse(t *testing.T) {
	p := program(t, "outputs:\n  a: ts_mean(close, 5) - ts_mean(close, 10)\n  b: ts_mean(close, 5)\n")
	for _, node := range p.Nodes {
		for _, arg := range node.Args {
			if arg >= node.Index {
				t.Fatalf("node %d (%s) uses later node %d", node.Index, node.Op, arg)
			}
			if p.Nodes[arg].LastUse < node.Index {
				t.Fatalf("node %d last use %d < consumer %d", arg, p.Nodes[arg].LastUse, node.Index)
			}
		}
	}
	for _, output := range p.Outputs {
		if p.Nodes[output.Node].LastUse != len(p.Nodes) {
			t.Fatalf("output %s should be retained, last use = %d", output.Name, p.Nodes[output.Node].LastUse)
		}
	}
	for _, node := range p.Nodes {
		if node.Op == OpColumn && node.LastUse == len(p.Nodes) {
			t.Fatalf("column should be released after its last consumer")
		}
	}
}

func TestProgramSharesLetWithinFactor(t *testing.T) {
	p := program(t, "let:\n  tp: (high + low + close) / 3\n  m: ts_mean(tp, 20)\noutputs:\n  cci: (tp - m) / (0.015 * ts_mean(abs(tp - m), 20))\n")
	if got := countOps(p, "div"); got != 2 {
		t.Fatalf("div nodes = %d, want 2 (tp and the output)", got)
	}
	if got := countOps(p, "sub"); got != 1 {
		t.Fatalf("sub nodes = %d, want 1 (tp - m shared)", got)
	}
}

func TestProgramRejects(t *testing.T) {
	a := mustCompile(t, "outputs:\n  x: close\n")
	b := mustCompile(t, "outputs:\n  x: open\n")
	if _, err := NewProgram([]ProgramInput{{FactorID: "a", Factor: a}, {FactorID: "b", Factor: b}}); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewProgram([]ProgramInput{{FactorID: "a"}}); err == nil {
		t.Fatal("nil factor should be rejected")
	}
}

func TestEmptyProgram(t *testing.T) {
	p, err := NewProgram(nil)
	if err != nil || len(p.Nodes) != 0 || p.Lookback != 1 {
		t.Fatalf("program = %+v, err = %v", p, err)
	}
}
