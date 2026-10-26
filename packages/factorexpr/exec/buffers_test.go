package exec

import (
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// 缓冲区峰值按手算核对，不依赖 EstimateMemory（Codex 审查给出的最小例子）：单序列、长度 3、只算最后一点，
// 先 ts_mean(x+1, 2) 后 ts_mean(x+2, 3)。依次分配 x+1（2 个）、均值（1 个，输出保留）、回收 x+1、
// 分配 x+2（3 个）、均值（1 个）：峰值是 1 + 3 + 1 = 5 个 float64。归还的数组不再被保留，所以这也是真实可达的峰值。
func TestPeakBufferHandComputed(t *testing.T) {
	panel, err := NewPanel(3, []string{"x"}, []SeriesKey{{"A", "t"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		panel.Set(0, 0, i, float64(i))
	}
	var inputs []factorexpr.ProgramInput
	for i, expression := range []string{"ts_mean(x + 1, 2)", "ts_mean(x + 2, 3)"} {
		factor, err := factorexpr.Compile(fmt.Sprintf("output: %s\n", expression))
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: fmt.Sprintf("f%d", i), Factor: factor})
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		t.Fatal(err)
	}
	result := run(t, program, panel, 2, 2, 1)
	if result.PeakBufferBytes != 5*8 {
		t.Fatalf("peak = %d bytes, want 40", result.PeakBufferBytes)
	}
	estimate, err := EstimateMemory(program, 1, 3, 2, 2)
	if err != nil || estimate.PeakBuffers != 5*8 {
		t.Fatalf("estimate = %+v, err = %v", estimate, err)
	}
	if result.Value(0, 0, 2) != 2.5 || result.Value(1, 0, 2) != 3 {
		t.Fatalf("values = %v, %v", result.Value(0, 0, 2), result.Value(1, 0, 2))
	}
}
