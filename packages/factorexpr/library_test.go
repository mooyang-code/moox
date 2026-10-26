package factorexpr

import (
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

type libraryEntry struct {
	FactorID string   `yaml:"factor_id"`
	Inputs   []string `yaml:"inputs"`
	Outputs  []string `yaml:"outputs"`
	Lookback int      `yaml:"lookback"`
	Spec     string   `yaml:"spec"`
}

func loadLibrary(t *testing.T) []libraryEntry {
	t.Helper()
	raw, err := os.ReadFile("testdata/xbx_factors.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var entries []libraryEntry
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 11 {
		t.Fatalf("library has %d factors, want 11", len(entries))
	}
	return entries
}

// 现有 11 个因子的派生元数据必须与生产因子库一致（设计 6.2 的表）。
func TestLibraryMetadataMatchesProduction(t *testing.T) {
	for _, entry := range loadLibrary(t) {
		t.Run(entry.FactorID, func(t *testing.T) {
			factor, err := Compile(entry.Spec)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if !reflect.DeepEqual(factor.InputColumns, entry.Inputs) {
				t.Errorf("inputs = %v, want %v", factor.InputColumns, entry.Inputs)
			}
			if got := factor.OutputNamesFor(entry.FactorID); !reflect.DeepEqual(got, entry.Outputs) {
				t.Errorf("outputs = %v, want %v", got, entry.Outputs)
			}
			if factor.Lookback != entry.Lookback {
				t.Errorf("lookback = %d, want %d", factor.Lookback, entry.Lookback)
			}
			if factor.Type != FactorTypeTimeSeries {
				t.Errorf("type = %s, want timeseries", factor.Type)
			}
		})
	}
}

// 11 个因子合并成一个集合程序时，不含参数的子表达式（输入列、tp、amp 等）只保留一个节点；含窗口参数的节点
// 按成员各有参数槽（ts_mean(close, $window) 在 Bias、BiasQ、ZscoreAbsMeanQ 里各一个），取值相同时由执行器在运行时去重。
func TestLibraryProgramSharesSubexpressions(t *testing.T) {
	var inputs []ProgramInput
	for _, entry := range loadLibrary(t) {
		factor, err := Compile(entry.Spec)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, ProgramInput{FactorID: entry.FactorID, Factor: factor})
	}
	program, err := NewProgram(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if program.Lookback != 358 {
		t.Errorf("program lookback = %d, want 358", program.Lookback)
	}
	if want := []string{"close", "high", "low", "open", "quote_volume", "volume"}; !reflect.DeepEqual(program.Columns, want) {
		t.Errorf("columns = %v, want %v", program.Columns, want)
	}
	if len(program.Params) != 11 {
		t.Errorf("program has %d param slots, want 11", len(program.Params))
	}
	counts := map[string]int{}
	for _, node := range program.Nodes {
		if node.Op == "ts_mean" && len(node.Args) == 1 && program.Nodes[node.Args[0]].Column == "close" {
			counts["ts_mean(close,$window)"]++
		}
		if node.Op == "div" && len(node.Args) == 2 && program.Nodes[node.Args[1]].Value == 3 {
			counts["tp"]++
		}
		if node.Op == OpColumn {
			counts["col:"+node.Column]++
		}
	}
	if counts["ts_mean(close,$window)"] != 3 {
		t.Errorf("ts_mean(close, $window) appears %d times, want 3", counts["ts_mean(close,$window)"])
	}
	// Cci、MinMax 的 (high + low + close) / 3 与 ZfAbsMean 的 (close + high + low) / 3 书写顺序不同，各一个。
	if counts["tp"] != 2 {
		t.Errorf("tp appears %d times, want 2", counts["tp"])
	}
	for _, column := range program.Columns {
		if counts["col:"+column] != 1 {
			t.Errorf("column %s appears %d times, want 1", column, counts["col:"+column])
		}
	}
	if len(program.Outputs) != 11 {
		t.Errorf("program has %d outputs, want 11", len(program.Outputs))
	}
}
