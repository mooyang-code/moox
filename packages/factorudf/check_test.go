package factorudf

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandBuiltValidModulePassesAllChecks(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	_, err := rt.Load(context.Background(), validBuilder().build())
	require.NoError(t, err)
}

func TestRejectsUnqualifiedModules(t *testing.T) {
	zero := 0
	cases := []struct {
		name   string
		mutate func(b *modBuilder)
		want   string
	}{
		{"有导入", func(b *modBuilder) { b.imports = [][2]string{{"env", "now"}}; b.funcs[0].typ = 0 }, "导入"},
		{"有 start 函数", func(b *modBuilder) { b.start = &zero }, "start"},
		{"共享内存（线程特性）", func(b *modBuilder) { b.memory = []byte{0x03, 0x02, 0x04} }, "共享内存"},
		{"64 位内存", func(b *modBuilder) { b.memory = []byte{0x04, 0x02} }, "64 位"},
		{"初始内存过大", func(b *modBuilder) { b.memory = []byte{0x00, 0x80, 0x10} }, "初始内存"},
		{"两个内存", func(b *modBuilder) { b.memories = [][]byte{{0x00, 0x01}} }, "要恰好一个"},
		{"没有内存", func(b *modBuilder) { b.memory = nil; b.exports = b.exports[1:] }, "没有定义内存"},
		{"表没有最大值", func(b *modBuilder) { b.tables = [][]byte{{0x70, 0x00, 0x02}} }, "没有声明最大元素数"},
		{"表过大", func(b *modBuilder) { b.tables = [][]byte{{0x70, 0x01, 0x80, 0x20, 0x80, 0x20}} }, "超过上限"},
		{"缺少元数据", func(b *modBuilder) { b.customs = nil }, "缺少自定义段"},
		{"元数据不是 JSON", func(b *modBuilder) { b.customs = [][2][]byte{{[]byte(CustomSection), []byte("not json")}} }, "元数据"},
		{"回看公式不合规", func(b *modBuilder) {
			b.customs = [][2][]byte{{[]byte(CustomSection), []byte(strings.Replace(validManifest, `"lookback":"n"`, `"lookback":"n*n"`, 1))}}
		}, "回看"},
		{"ABI 版本不对", func(b *modBuilder) { b.funcs[0].body = constI32(2) }, "ABI 版本"},
		{"导出缺失", func(b *modBuilder) { b.exports = b.exports[:3] }, "缺少导出函数 moox_udf_eval"},
		{"eval 签名不对", func(b *modBuilder) { b.funcs[2].typ = 1 }, "签名"},
		{"reserve 返回类型不对", func(b *modBuilder) {
			b.types = append(b.types, funcType{params: []byte{i32}, results: []byte{0x7e}})
			b.funcs[1] = fn{typ: len(b.types) - 1, body: []byte{0x42, 0x00}}
		}, "i32"},
	}
	rt := newTestRuntime(t, Options{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := validBuilder()
			tc.mutate(b)
			_, err := rt.Load(context.Background(), b.build())
			require.Error(t, err)
			var ce *CheckError
			require.True(t, errors.As(err, &ce), "应是模块检查错误：%v", err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestRejectsOversizedModule(t *testing.T) {
	rt := newTestRuntime(t, Options{Limits: Limits{MaxModuleBytes: 64}})
	_, err := rt.Load(context.Background(), validBuilder().build())
	require.ErrorContains(t, err, "超过上限")
}

func TestRejectsGarbage(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	for _, raw := range [][]byte{nil, []byte("hello"), []byte("\x00asm\x02\x00\x00\x00"), append([]byte("\x00asm\x01\x00\x00\x00"), 0x01, 0xff, 0xff)} {
		_, err := rt.Load(context.Background(), raw)
		var ce *CheckError
		require.True(t, errors.As(err, &ce), "%q", raw)
	}
}

func TestManifestValidation(t *testing.T) {
	bad := map[string]string{
		"名称含大写":           strings.Replace(validManifest, `"name":"hand"`, `"name":"Hand"`, 1),
		"未知字段":            strings.Replace(validManifest, `"abi":1`, `"abi":1,"extra":1`, 1),
		"参数范围倒置":          strings.Replace(validManifest, `"min":1,"max":10`, `"min":10,"max":1`, 1),
		"整数参数是小数":         strings.Replace(validManifest, `"min":1,"max":10`, `"min":1.5,"max":10`, 1),
		"回看引用未声明参数":       strings.Replace(validManifest, `"lookback":"n"`, `"lookback":"m"`, 1),
		"回看带减法":           strings.Replace(validManifest, `"lookback":"n"`, `"lookback":"n-1"`, 1),
		"回看取下限时为 0":       strings.Replace(strings.Replace(validManifest, `"min":1`, `"min":0`, 1), `"lookback":"n"`, `"lookback":"2*n"`, 1),
		"回看超过上限":          strings.Replace(validManifest, `"max":10`, `"max":2000000`, 1),
		"严格模式带 min_valid": strings.Replace(validManifest, `"min_valid":1`, `"min_valid":3`, 1),
		"nan 取值不对":        strings.Replace(validManifest, `"nan":"strict"`, `"nan":"loose"`, 1),
		"ABI 不对":          strings.Replace(validManifest, `"abi":1`, `"abi":9`, 1),
	}
	for name, raw := range bad {
		_, err := ParseManifest([]byte(raw))
		require.Error(t, err, name)
	}
	m, err := ParseManifest([]byte(validManifest))
	require.NoError(t, err)
	l, err := m.LookbackFor([]float64{7})
	require.NoError(t, err)
	require.EqualValues(t, 7, l)
}

func TestLookbackFormulas(t *testing.T) {
	params := []Param{{Name: "n", Type: ParamInt, Min: 2, Max: 500}, {Name: "q", Type: ParamFloat, Min: 0, Max: 1}}
	cases := []struct {
		expr   string
		lo, hi int64
		bad    bool
	}{
		{"20", 20, 20, false},
		{"n", 2, 500, false},
		{"4*n", 8, 2000, false},
		{"n+1", 3, 501, false},
		{"4 * n + 3", 11, 2003, false},
		{"0*n+5", 5, 5, false},
		{"q", 0, 0, true},
		{"n*n", 0, 0, true},
		{"n-1", 0, 0, true},
		{"2*(n)", 0, 0, true},
		{"", 0, 0, true},
		{"4*", 0, 0, true},
		{"+3", 0, 0, true},
		{"0", 0, 0, true},
	}
	for _, tc := range cases {
		lb, err := ParseLookback(tc.expr, params)
		if err == nil {
			var lo, hi int64
			lo, hi, err = lb.Range()
			if err == nil {
				require.Equal(t, tc.lo, lo, tc.expr)
				require.Equal(t, tc.hi, hi, tc.expr)
			}
		}
		require.Equal(t, tc.bad, err != nil, "%q：%v", tc.expr, err)
	}
	// 溢出被拒绝。
	huge := []Param{{Name: "n", Type: ParamInt, Min: 1, Max: maxIntParam}}
	lb, err := ParseLookback("9223372036854775807*n", huge)
	require.NoError(t, err)
	_, _, err = lb.Range()
	require.Error(t, err)
}
