package factorexpr

import "testing"

func nodeByCanonical(t *testing.T, p *Program, canonical string) int {
	t.Helper()
	for _, node := range p.Nodes {
		if programText(p, node.Index) == canonical {
			return node.Index
		}
	}
	t.Fatalf("node %s not found", canonical)
	return -1
}

// programText 按程序节点重建子树的规范文本，便于在测试里定位节点。
func programText(p *Program, index int) string {
	return programTree(p, index).Canonical()
}

func programTree(p *Program, index int) *Node {
	node := p.Nodes[index]
	tree := &Node{Op: node.Op, Kind: node.Kind, Column: node.Column, Value: node.Value, Ints: node.Ints, Floats: node.Floats}
	for _, arg := range node.Args {
		tree.Args = append(tree.Args, programTree(p, arg))
	}
	return tree
}

func TestNeedLiveTarget(t *testing.T) {
	p := program(t, "outputs:\n  o: ts_rank(ts_mean(close, 20), 20)\n")
	need, err := p.Need(100, 100)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]Interval{
		"ts_rank(ts_mean(close,20),20)": {100, 100},
		"ts_mean(close,20)":             {81, 100},
		"close":                         {62, 100},
	}
	for canonical, want := range cases {
		if got := need[nodeByCanonical(t, p, canonical)]; got != want {
			t.Errorf("%s need = %+v, want %+v", canonical, got, want)
		}
	}
	// 输入列的需求起点正好是“目标 − 回看 + 1”。
	if got := need[nodeByCanonical(t, p, "close")]; got.Start != 100-p.Lookback+1 || got.Len() != p.Lookback {
		t.Errorf("close need %+v does not match lookback %d", got, p.Lookback)
	}
}

func TestNeedRecalcRangeAndShift(t *testing.T) {
	p := program(t, "outputs:\n  a: delay(close, 3) + close\n  b: ts_mean(open, 5)\n  c: delta(high, 2) * 2\n")
	need, err := p.Need(50, 59)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]Interval{
		"delay(close,3)": {50, 59},
		// close 被 delay 需要 [47, 56]、被加法需要 [50, 59]，取凸包。
		"close":         {47, 59},
		"open":          {46, 59},
		"delta(high,2)": {50, 59},
		"high":          {48, 59},
	}
	for canonical, want := range cases {
		if got := need[nodeByCanonical(t, p, canonical)]; got != want {
			t.Errorf("%s need = %+v, want %+v", canonical, got, want)
		}
	}
	if got := need[nodeByCanonical(t, p, "2")]; !got.Empty() {
		t.Errorf("constant need = %+v, want empty", got)
	}
}

// 需求区间不会超出“目标起点 − 集合回看 + 1”，至少有一个输入列恰好从那里开始。
func TestNeedStaysWithinProgramLookback(t *testing.T) {
	var inputs []ProgramInput
	for _, entry := range loadLibrary(t) {
		inputs = append(inputs, ProgramInput{FactorID: entry.FactorID, Factor: mustCompile(t, entry.Spec)})
	}
	p, err := NewProgram(inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []Interval{{200, 200}, {200, 699}} {
		need, err := p.Need(target.Start, target.End)
		if err != nil {
			t.Fatal(err)
		}
		earliest := target.End
		for index, interval := range need {
			if interval.Empty() {
				continue
			}
			if interval.Start < target.Start-p.Lookback+1 || interval.End > target.End {
				t.Fatalf("node %d need %+v outside [%d, %d]", index, interval, target.Start-p.Lookback+1, target.End)
			}
			earliest = min(earliest, interval.Start)
		}
		if earliest != target.Start-p.Lookback+1 {
			t.Fatalf("earliest need = %d, want %d", earliest, target.Start-p.Lookback+1)
		}
	}
	if _, err := p.Need(5, 4); err == nil {
		t.Fatal("empty target should be rejected")
	}
}
