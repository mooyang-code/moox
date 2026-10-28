package udfhost

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/stretchr/testify/require"
)

func wasm(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "factorudf", "testdata", name+".wasm"))
	require.NoError(t, err)
	return b
}

// newHost 加载示例模块并返回宿主与解析环境。
func newHost(t *testing.T, limits factorudf.Limits, names ...string) (*Host, factorexpr.UDFs) {
	t.Helper()
	rt, err := factorudf.NewRuntime(context.Background(), factorudf.Options{Limits: limits})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	host := New(rt)
	env := factorexpr.UDFs{}
	for _, name := range names {
		m, err := host.Load(context.Background(), wasm(t, name))
		require.NoError(t, err)
		u, err := UDFFromManifest(m.Info.Manifest, m.Info.Hash, Describe{Doc: "示例"})
		require.NoError(t, err)
		env[u.Name] = u
	}
	return host, env
}

type lcg uint64

func (r *lcg) next() uint64 {
	*r = *r*6364136223846793005 + 1442695040888963407
	return uint64(*r >> 11)
}

// testPanel 构造一个有 NaN 开头与中途 NaN 的面板，数值是二的幂的整数倍，精确可复现。
func testPanel(t *testing.T, length, series int, cols ...string) *exec.Panel {
	t.Helper()
	keys := make([]exec.SeriesKey, series)
	for i := range keys {
		keys[i] = exec.SeriesKey{Subject: fmt.Sprintf("S%02d", i)}
	}
	panel, err := exec.NewPanel(length, cols, keys)
	require.NoError(t, err)
	rng := lcg(42)
	for c := range cols {
		for s := 0; s < series; s++ {
			v := 100.0 + float64(s)
			for tt := 0; tt < length; tt++ {
				v += float64(int64(rng.next()%2001)-1000) / 1024
				value := v
				switch {
				case s%5 == 1 && tt < 30+s:
					value = math.NaN() // 上市晚
				case s%7 == 3 && tt == length/2:
					value = math.NaN() // 中途缺失
				}
				panel.Set(c, s, tt, value)
				panel.MarkPresent(s, tt)
			}
		}
	}
	return panel
}

func compileAll(t *testing.T, env factorexpr.UDFEnv, specs ...string) *factorexpr.Program {
	t.Helper()
	var members []factorexpr.ProgramInput
	for i, spec := range specs {
		f, err := factorexpr.CompileWith("output: "+spec, env)
		require.NoError(t, err, spec)
		members = append(members, factorexpr.ProgramInput{Key: fmt.Sprintf("m%d", i), FactorID: fmt.Sprintf("f%d", i), Factor: f})
	}
	program, err := factorexpr.NewProgram(members)
	require.NoError(t, err)
	return program
}

func sameBits(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Float64bits(a) == math.Float64bits(b)
}

// 自定义算子版的 ts_mean 与截断 EMA，和内置算子逐位一致：含开头 NaN、中途 NaN、分块、多 worker。
func TestUDFMatchesBuiltinsBitForBit(t *testing.T) {
	host, env := newHost(t, factorudf.Limits{}, "ts_mean", "ema_trunc")
	panel := testPanel(t, 500, 14, "close")
	program := compileAll(t, env,
		"ts_mean(close, 20)", "udf.ts_mean_udf(close, 20)",
		"ts_mean(close, 1)", "udf.ts_mean_udf(close, 1)",
		"ts_mean(close, 97)", "udf.ts_mean_udf(close, 97)",
		"ema(close, 20, 208)", "udf.ema_trunc(close, 20, 208)",
		"ema(close, 5, 60)", "udf.ema_trunc(close, 5, 60)",
		"ts_mean(ema(close, 10, 40), 15)", "udf.ts_mean_udf(udf.ema_trunc(close, 10, 40), 15)",
	)
	ctx := context.Background()
	full, err := exec.Run(ctx, program, panel, exec.Options{Start: 300, End: 499, Workers: 1, UDF: host})
	require.NoError(t, err)
	require.Len(t, full.Columns, 12)
	for pair := 0; pair < 12; pair += 2 {
		for s := range full.Series {
			for tt := full.Start; tt <= full.End; tt++ {
				a, b := full.Value(pair, s, tt), full.Value(pair+1, s, tt)
				require.True(t, sameBits(a, b), "第 %d 对、序列 %d、t=%d：内置 %v，自定义 %v", pair/2, s, tt, a, b)
			}
		}
	}
	// 多 worker、不同分块与起点：结果逐位相同。
	for _, w := range []int{1, 4, 16} {
		for _, rng := range [][2]int{{300, 499}, {450, 499}, {499, 499}, {300, 300}, {377, 420}} {
			got, err := exec.Run(ctx, program, panel, exec.Options{Start: rng[0], End: rng[1], Workers: w, UDF: host})
			require.NoError(t, err)
			for c := range got.Columns {
				for s := range got.Series {
					for tt := got.Start; tt <= got.End; tt++ {
						require.True(t, sameBits(got.Value(c, s, tt), full.Value(c, s, tt)), "workers=%d 区间 %v 列 %d 序列 %d t=%d", w, rng, c, s, tt)
					}
				}
			}
		}
	}
	// 至少有有限值（不是全 NaN 的假通过）。
	finite := 0
	for s := range full.Series {
		if v := full.Value(1, s, 499); !math.IsNaN(v) {
			finite++
		}
	}
	require.Positive(t, finite)
}

// 参数槽：每条序列各自的参数值（窗口长度不同）逐位等于内置算子逐条序列取同样参数。
func TestUDFWithPerSeriesParams(t *testing.T) {
	host, env := newHost(t, factorudf.Limits{}, "ts_mean")
	spec := "params:\n  n: {type: int, min: 2, max: 60, default: 10}\noutput: %s\n"
	build := func(expr string) *factorexpr.Program {
		f, err := factorexpr.CompileWith(fmt.Sprintf(spec, expr), env)
		require.NoError(t, err)
		p, err := factorexpr.NewProgram([]factorexpr.ProgramInput{{Key: "m", FactorID: "f", Factor: f}})
		require.NoError(t, err)
		return p
	}
	panel := testPanel(t, 300, 9, "close")
	params := map[string][]float64{factorexpr.ParamSlot("m", "n"): {2, 5, 10, 20, 33, 60, 7, 7, 41}}
	opts := exec.Options{Start: 200, End: 299, Workers: 4, Params: params, UDF: host}
	builtin, err := exec.Run(context.Background(), build("ts_mean(close, n)"), panel, opts)
	require.NoError(t, err)
	custom, err := exec.Run(context.Background(), build("udf.ts_mean_udf(close, n)"), panel, opts)
	require.NoError(t, err)
	for s := range builtin.Series {
		for tt := 200; tt <= 299; tt++ {
			require.True(t, sameBits(builtin.Value(0, s, tt), custom.Value(0, s, tt)), "序列 %d t=%d", s, tt)
		}
	}
}

// 故障让 Run 返回错误，不写 NaN；fill_nan 与 cs_rank 不会把失败吞成空值。
func TestUDFFailureFailsTheWholeRun(t *testing.T) {
	host, env := newHost(t, factorudf.Limits{CallTimeout: 300 * time.Millisecond}, "faulty")
	panel := testPanel(t, 120, 6, "close")
	// 在一条序列的窗口里注入哨兵值。
	col, _ := panel.ColumnIndex("close")
	panel.Set(col, 2, 110, -8888888.0)
	for _, expr := range []string{
		"udf.faulty_mean(close, 5)",
		"fill_nan(udf.faulty_mean(close, 5), 0)",
		"cs_rank(udf.faulty_mean(close, 5))",
		"udf.faulty_mean(close, 5) + ts_mean(close, 3)",
	} {
		program := compileAll(t, env, expr)
		_, err := exec.Run(context.Background(), program, panel, exec.Options{Start: 100, End: 119, Workers: 3, UDF: host})
		require.Error(t, err, expr)
		require.Contains(t, err.Error(), "自定义算子 udf.faulty_mean", expr)
	}
	// 死循环：被截止时间中断，运行失败。
	panel.Set(col, 2, 110, -7777777.0)
	_, err := exec.Run(context.Background(), compileAll(t, env, "udf.faulty_mean(close, 5)"), panel, exec.Options{Start: 100, End: 119, UDF: host})
	require.Error(t, err)
	// 同一个宿主立刻还能正常计算（故障实例被丢弃，进程不受影响）。
	panel.Set(col, 2, 110, 100.0)
	res, err := exec.Run(context.Background(), compileAll(t, env, "udf.faulty_mean(close, 5)"), panel, exec.Options{Start: 100, End: 119, UDF: host})
	require.NoError(t, err)
	require.False(t, math.IsNaN(res.Value(0, 0, 119)))
}

func TestRunWithoutHostFails(t *testing.T) {
	_, env := newHost(t, factorudf.Limits{}, "ts_mean")
	panel := testPanel(t, 60, 3, "close")
	_, err := exec.Run(context.Background(), compileAll(t, env, "udf.ts_mean_udf(close, 5)"), panel, exec.Options{Start: 50, End: 59})
	require.ErrorContains(t, err, "没有提供宿主")
}

// 批字节上限：每批序列数由公式决定，多批结果与单批一致；放不下一条序列时报错。
func TestUDFBatching(t *testing.T) {
	big, env := newHost(t, factorudf.Limits{}, "ts_mean")
	small, _ := newHost(t, factorudf.Limits{BatchBytes: 8 * (1*(50+19) + 1 + 50) * 3}, "ts_mean") // 每批 3 条
	panel := testPanel(t, 200, 11, "close")
	program := compileAll(t, env, "udf.ts_mean_udf(close, 20)")
	opts := exec.Options{Start: 150, End: 199, Workers: 1}
	opts.UDF = big
	want, err := exec.Run(context.Background(), program, panel, opts)
	require.NoError(t, err)
	opts.UDF = small
	got, err := exec.Run(context.Background(), program, panel, opts)
	require.NoError(t, err)
	for s := range want.Series {
		for tt := 150; tt <= 199; tt++ {
			require.True(t, sameBits(want.Value(0, s, tt), got.Value(0, s, tt)))
		}
	}
	tiny, _ := newHost(t, factorudf.Limits{BatchBytes: 64}, "ts_mean")
	opts.UDF = tiny
	_, err = exec.Run(context.Background(), program, panel, opts)
	require.ErrorContains(t, err, "批字节上限")
}

func TestEstimateMemoryCountsUDFCopyBuffers(t *testing.T) {
	_, env := newHost(t, factorudf.Limits{}, "ts_mean")
	udfProgram := compileAll(t, env, "udf.ts_mean_udf(close, 500)")
	plain := compileAll(t, env, "ts_mean(close, 500)")
	a, err := exec.EstimateMemory(udfProgram, 100, 1000, 900, 999)
	require.NoError(t, err)
	b, err := exec.EstimateMemory(plain, 100, 1000, 900, 999)
	require.NoError(t, err)
	require.Greater(t, a.PeakBuffers, b.PeakBuffers, "自定义算子每次调用的拷贝缓冲计入估算")
}
