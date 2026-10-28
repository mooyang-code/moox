package factorudf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"runtime"
	"time"
)

// ConformanceOptions 是一致性测试的限制。
type ConformanceOptions struct {
	// Timeout 是整套测试的总时限；MaxBatchBytes 是单次调用的字节上限（超过的参数组合被跳过并记入报告）。
	Timeout       time.Duration
	MaxBatchBytes int64
}

func (o ConformanceOptions) withDefaults() ConformanceOptions {
	if o.Timeout <= 0 {
		o.Timeout = time.Minute
	}
	if o.MaxBatchBytes <= 0 {
		o.MaxBatchBytes = 256 << 20
	}
	return o
}

// TestResult 是一类测试的结果。
type TestResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// Report 是一致性测试报告。Digest 是全部参考输出的规范字节（小端 f64、NaN 已规范化）的 sha256：
// 服务端重跑后比对摘要，本地（arm64）与服务端（amd64）的跨架构确定性因此每次发布都被检查。
type Report struct {
	Hash         string       `json:"hash"`
	Name         string       `json:"name"`
	Tests        []TestResult `json:"tests"`
	Passed       bool         `json:"passed"`
	Digest       string       `json:"digest"`
	ParamSets    int          `json:"param_sets"`
	Skipped      []string     `json:"skipped,omitempty"`
	NSPerPoint   float64      `json:"ns_per_point"`
	PeakMemBytes int64        `json:"peak_mem_bytes"`
	Arch         string       `json:"arch"`
}

// Failures 返回未通过的测试。
func (r *Report) Failures() []TestResult {
	var out []TestResult
	for _, t := range r.Tests {
		if !t.Passed {
			out = append(out, t)
		}
	}
	return out
}

const (
	// 合成数据的尺寸。
	conformanceSeries = 6
	conformanceOutLen = 48
	maxParamSets      = 12
)

// 测试名。
const (
	testChunk       = "分块与只依赖窗口"
	testBatch       = "批次组合与换序"
	testFuture      = "未来数据扰动"
	testPast        = "回看之外的历史"
	testNaN         = "空值"
	testFinite      = "有限性"
	testDeterminism = "确定性"
	testFixtures    = "fixtures 对拍"
	testResource    = "资源"
)

type conformance struct {
	ctx    context.Context
	rt     *Runtime
	m      *Module
	opts   ConformanceOptions
	digest hash.Hash

	results map[string]*TestResult
	order   []string
	skipped []string

	points    int64
	elapsed   time.Duration
	peakBytes int64
}

func (c *conformance) fail(test, format string, args ...any) {
	if c.ctx.Err() != nil {
		// 总时限到了：之后的执行失败都是被截止时间打断的，统一记为超时，不当作模块的缺陷。
		test, format, args = testResource, "一致性测试超过总时限 %s", []any{c.opts.Timeout}
	}
	r := c.results[test]
	if r == nil {
		r = &TestResult{Name: test, Passed: true}
		c.results[test] = r
		c.order = append(c.order, test)
	}
	if r.Passed {
		r.Passed = false
		r.Detail = fmt.Sprintf(format, args...) // 只记第一个失败（带输入摘要与最小复现参数）
	}
}

func (c *conformance) ok(test string) {
	if c.results[test] == nil {
		c.results[test] = &TestResult{Name: test, Passed: true}
		c.order = append(c.order, test)
	}
}

// RunConformance 在模块上运行设计 8.3 的全部测试，并返回报告。测试在专用实例上运行（不占用调用方的实例池），
// 整套测试有总时限；内部错误（而不是测试不通过）返回 error。
func RunConformance(ctx context.Context, rt *Runtime, m *Module, fixtures FixtureSet, opts ConformanceOptions) (*Report, error) {
	opts = opts.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	c := &conformance{ctx: ctx, rt: rt, m: m, opts: opts, digest: sha256.New(), results: map[string]*TestResult{}}
	for _, name := range []string{testChunk, testBatch, testFuture, testPast, testNaN, testFinite, testDeterminism, testFixtures, testResource} {
		c.ok(name)
	}
	man := m.Info.Manifest
	sets := paramSets(man)
	ran := 0
	for _, params := range sets {
		if ctx.Err() != nil {
			c.fail(testResource, "一致性测试超过总时限 %s", opts.Timeout)
			break
		}
		done, err := c.runParamSet(params)
		if err != nil {
			return nil, err
		}
		if done {
			ran++
		}
	}
	if err := c.runFixtures(fixtures); err != nil {
		return nil, err
	}
	if ran == 0 {
		c.fail(testResource, "没有任何参数组合能在批字节上限 %d 内运行", opts.MaxBatchBytes)
	}
	rep := &Report{Hash: m.Info.Hash, Name: man.Name, ParamSets: ran, Skipped: c.skipped, Arch: runtime.GOARCH, PeakMemBytes: c.peakBytes}
	if c.points > 0 {
		rep.NSPerPoint = float64(c.elapsed.Nanoseconds()) / float64(c.points)
	}
	rep.Passed = true
	for _, name := range c.order {
		r := *c.results[name]
		rep.Tests = append(rep.Tests, r)
		rep.Passed = rep.Passed && r.Passed
	}
	rep.Digest = hex.EncodeToString(c.digest.Sum(nil))
	return rep, nil
}

// paramSets 取声明范围的下限、上限与若干中间值：全下限、全上限、1/4、1/2、3/4，外加每个参数单独取上下限。
// 取值都是精确可表示的（整数，或端点按二的幂分割），在 arm64 与 amd64 上一致。
func paramSets(man *Manifest) [][]float64 {
	at := func(p Param, num, den int) float64 {
		if p.Type == ParamInt {
			span := int64(p.Max) - int64(p.Min)
			return float64(int64(p.Min) + span*int64(num)/int64(den))
		}
		span := float64(p.Max - p.Min)
		return float64(float64(p.Min) + float64(span*float64(num))/float64(den))
	}
	var sets [][]float64
	seen := map[string]bool{}
	add := func(v []float64) {
		key := fmt.Sprint(v)
		if !seen[key] && len(sets) < maxParamSets {
			seen[key] = true
			sets = append(sets, v)
		}
	}
	uniform := func(num, den int) []float64 {
		v := make([]float64, len(man.Params))
		for i, p := range man.Params {
			v[i] = at(p, num, den)
		}
		return v
	}
	add(uniform(0, 1))
	add(uniform(1, 1))
	add(uniform(1, 2))
	add(uniform(1, 4))
	add(uniform(3, 4))
	// 每个参数单独取上限（其余取下限）与单独取下限（其余取上限）。
	for i := range man.Params {
		for _, hi := range []bool{true, false} {
			v := make([]float64, len(man.Params))
			for j, p := range man.Params {
				if (j == i) == hi {
					v[j] = at(p, 1, 1)
				} else {
					v[j] = at(p, 0, 1)
				}
			}
			add(v)
		}
	}
	if len(sets) == 0 {
		sets = [][]float64{{}}
	}
	return sets
}

// ---------------------------------------------------------------- 合成数据

// genKind 是合成序列的种类：随机游走、开头 NaN、中途 NaN、常数、量级很大、量级很小。
const genKinds = 6

// genSeries 生成一条序列。数值都是二的幂的整数倍：随机游走的步长是 k/1024（k 为整数），累加与缩放都精确，
// 数据在不同架构上逐位相同（不依赖浮点运算的舍入或融合乘加）。
func genSeries(rng *splitmix, kind, n, inputIndex int) []float64 {
	scale := 1.0
	switch kind {
	case 4:
		scale = math.Ldexp(1, 40)
	case 5:
		scale = math.Ldexp(1, -60)
	}
	out := make([]float64, n)
	v := 4096.0 + float64(rng.next()%1024)
	for i := range out {
		if kind == 1 {
			out[i] = 42.5
			continue
		}
		step := float64(int64(rng.next()%2001)-1000) / 1024
		v += step
		out[i] = v * scale
	}
	switch kind {
	case 2: // 开头 NaN：不同输入的长度不同
		lead := min(n/4, 3+inputIndex*5+int(rng.next()%4))
		for i := 0; i < lead; i++ {
			out[i] = math.NaN()
		}
	case 3: // 中途 NaN
		out[n/2] = math.NaN()
		if n > 8 {
			out[n/3] = math.NaN()
		}
	}
	return out
}

// dataSet 是一个参数组合下的合成数据：每条序列每个输入一份，长度 inLen。
type dataSet struct {
	l      int64
	inLen  int
	outLen int
	inputs [][][]float64 // [输入][序列][inLen]
	params []float64
}

func (c *conformance) buildData(params []float64) (*dataSet, bool) {
	man := c.m.Info.Manifest
	l, err := man.LookbackFor(params)
	if err != nil {
		c.fail(testResource, "参数 %v 的回看公式求值失败：%v", params, err)
		return nil, false
	}
	outLen := conformanceOutLen
	series := conformanceSeries
	switch {
	case l > 200000:
		outLen, series = 4, 2
	case l > 8192:
		outLen, series = 8, 3
	case l > 1024:
		outLen = 24
	}
	d := &dataSet{l: l, inLen: outLen + int(l) - 1, outLen: outLen, params: params}
	b := &Batch{NSeries: series, InLen: d.inLen, OutLen: outLen, Inputs: make([][]float64, len(man.Inputs))}
	size, err := b.bytes(len(man.Params))
	if err != nil || size > c.opts.MaxBatchBytes {
		c.skipped = append(c.skipped, fmt.Sprintf("参数 %v：窗口长度 %d 需要的批太大", params, l))
		return nil, false
	}
	d.inputs = make([][][]float64, len(man.Inputs))
	seed := splitmix(uint64(l)*0x100000001b3 + uint64(len(params)))
	for k := range d.inputs {
		d.inputs[k] = make([][]float64, series)
		for s := 0; s < series; s++ {
			rng := splitmix(uint64(seed.next()) ^ uint64(k*131+s*7919))
			d.inputs[k][s] = genSeries(&rng, s%genKinds, d.inLen, k)
		}
	}
	return d, true
}

func (d *dataSet) series() int { return len(d.inputs[0]) }

// batch 组装指定序列（按给定顺序）、指定输出区间 [from, from+n) 的批。
func (d *dataSet) batch(order []int, from, n int, perturb func(input, series int, in []float64)) *Batch {
	inLen := n + int(d.l) - 1
	b := &Batch{NSeries: len(order), InLen: inLen, OutLen: n, Out: make([]float64, len(order)*n), keepInf: true}
	b.Inputs = make([][]float64, len(d.inputs))
	for k := range d.inputs {
		flat := make([]float64, 0, len(order)*inLen)
		for _, s := range order {
			win := append([]float64(nil), d.inputs[k][s][from:from+inLen]...)
			if perturb != nil {
				perturb(k, s, win)
			}
			flat = append(flat, win...)
		}
		b.Inputs[k] = flat
	}
	for range order {
		b.Params = append(b.Params, d.params...)
	}
	return b
}

func sameBits(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Float64bits(a) == math.Float64bits(b)
}

func (c *conformance) run(in *Instance, b *Batch) error {
	start := time.Now()
	err := in.Eval(c.ctx, b)
	c.elapsed += time.Since(start)
	c.points += int64(b.NSeries * b.OutLen)
	if sz := int64(in.mem.Size()); sz > c.peakBytes {
		c.peakBytes = sz
	}
	return err
}

// newInstance 创建测试专用的实例（不经过实例池）。
func (c *conformance) newInstance() (*Instance, error) { return c.m.instantiate(c.ctx) }

// runParamSet 在一个参数组合下跑全部测试；done 为假表示这个组合被跳过。
func (c *conformance) runParamSet(params []float64) (done bool, err error) {
	d, ok := c.buildData(params)
	if !ok {
		return false, nil
	}
	in, err := c.newInstance()
	if err != nil {
		return false, fmt.Errorf("创建测试实例：%w", err)
	}
	defer in.close(c.ctx)

	n := d.series()
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	// 参考输出：整批、原顺序、不加垃圾。摘要只包含它们。
	ref := d.batch(order, 0, d.outLen, nil)
	if err := c.run(in, ref); err != nil {
		c.fail(testResource, "参数 %v：执行失败：%v", params, err)
		return false, nil
	}
	for _, v := range ref.Out {
		var buf [8]byte
		if math.IsNaN(v) {
			v = canonicalNaN
		}
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
		c.digest.Write(buf[:])
		if math.IsInf(v, 0) {
			c.fail(testFinite, "参数 %v：输出含 ±Inf", params)
		}
	}
	full := func(s, j int) float64 { return ref.Out[s*d.outLen+j] }
	check := func(test string, b *Batch, rows []int, from int, what string) {
		for r, s := range rows {
			for j := 0; j < b.OutLen; j++ {
				got, want := b.Out[r*b.OutLen+j], full(s, from+j)
				if !sameBits(got, want) {
					c.fail(test, "参数 %v、序列 %d（种类 %d）、输出 %d：%s 得到 %v，参考 %v", params, s, s%genKinds, from+j, what, got, want)
					return
				}
			}
		}
	}

	// 分块与只依赖窗口：不同分块大小与起点，逐位相同。
	for _, size := range []int{1, 5, 17} {
		if size > d.outLen {
			continue
		}
		for _, from := range []int{0, 1, d.outLen / 2, d.outLen - size} {
			if from+size > d.outLen {
				continue
			}
			rows := []int{(from + size) % n, (from*3 + 1) % n}
			b := d.batch(rows[:1], from, size, nil)
			b.padElems, b.padSeed = 13, uint64(from*977+size)
			if err := c.run(in, b); err != nil {
				c.fail(testChunk, "参数 %v：分块 %d@%d 执行失败：%v", params, size, from, err)
				return true, nil
			}
			check(testChunk, b, rows[:1], from, fmt.Sprintf("分块 %d@%d", size, from))
		}
	}

	// 批次组合与换序：反序、拆成两批、带垃圾填充的偶数序列各自成批。
	rev := make([]int, n)
	for i := range rev {
		rev[i] = n - 1 - i
	}
	b := d.batch(rev, 0, d.outLen, nil)
	b.padElems, b.padSeed = 29, 7
	if err := c.run(in, b); err != nil {
		c.fail(testBatch, "参数 %v：反序批执行失败：%v", params, err)
		return true, nil
	}
	check(testBatch, b, rev, 0, "反序批")
	for _, rows := range [][]int{order[:n/2], order[n/2:]} {
		b := d.batch(rows, 0, d.outLen, nil)
		if err := c.run(in, b); err != nil {
			c.fail(testBatch, "参数 %v：子批执行失败：%v", params, err)
			return true, nil
		}
		check(testBatch, b, rows, 0, "子批")
	}

	// 未来数据扰动：t 之后的输入换成随机值或 NaN，t 及之前的输出不变。
	t := d.outLen / 2
	junk := splitmix(uint64(d.l))
	b = d.batch(order, 0, d.outLen, func(k, s int, win []float64) {
		for i := t + int(d.l); i < len(win); i++ {
			if junk.next()%3 == 0 {
				win[i] = math.NaN()
			} else {
				win[i] = float64(int64(junk.next()%2001)-1000) * 1e3
			}
		}
	})
	if err := c.run(in, b); err != nil {
		c.fail(testFuture, "参数 %v：执行失败：%v", params, err)
		return true, nil
	}
	for s := 0; s < n; s++ {
		for j := 0; j <= t; j++ {
			if got, want := b.Out[s*d.outLen+j], full(s, j); !sameBits(got, want) {
				c.fail(testFuture, "参数 %v、序列 %d、输出 %d：改动 t=%d 之后的数据后输出变了：%v → %v", params, s, j, t, want, got)
			}
		}
	}

	// 回看之外的历史：输出 j 只依赖 [j, j+L−1]，把更早的输入换成随机值或 NaN，t 及之后的输出不变。
	b = d.batch(order, 0, d.outLen, func(k, s int, win []float64) {
		for i := 0; i < t; i++ {
			if junk.next()%2 == 0 {
				win[i] = math.NaN()
			} else {
				win[i] = float64(int64(junk.next()%2001)-1000) * 1e3
			}
		}
	})
	if err := c.run(in, b); err != nil {
		c.fail(testPast, "参数 %v：执行失败：%v", params, err)
		return true, nil
	}
	for s := 0; s < n; s++ {
		for j := t; j < d.outLen; j++ {
			if got, want := b.Out[s*d.outLen+j], full(s, j); !sameBits(got, want) {
				c.fail(testPast, "参数 %v、序列 %d、输出 %d：窗口之外的历史改变后输出变了：%v → %v", params, s, j, want, got)
			}
		}
	}

	// 空值：规则规定必须是 NaN 的位置（严格：窗口含 NaN；relaxed：见设计第 5 节）。
	c.checkNaN(d, ref, params)

	// 确定性：两个新实例各跑两次，逐位相同（并且与参考输出相同）。
	for i := 0; i < 2; i++ {
		fresh, err := c.newInstance()
		if err != nil {
			return false, fmt.Errorf("创建测试实例：%w", err)
		}
		for rep := 0; rep < 2; rep++ {
			b := d.batch(order, 0, d.outLen, nil)
			if err := c.run(fresh, b); err != nil {
				c.fail(testDeterminism, "参数 %v：新实例 %d 第 %d 次执行失败：%v", params, i, rep, err)
				break
			}
			for idx, v := range b.Out {
				if !sameBits(v, ref.Out[idx]) {
					c.fail(testDeterminism, "参数 %v：新实例 %d 第 %d 次的输出 %d 与参考不同：%v → %v", params, i, rep, idx, ref.Out[idx], v)
					break
				}
			}
		}
		fresh.close(c.ctx)
		if c.ctx.Err() != nil {
			break
		}
	}
	return true, nil
}

// checkNaN 检查必须是 NaN 的位置。
func (c *conformance) checkNaN(d *dataSet, ref *Batch, params []float64) {
	man := c.m.Info.Manifest
	l := int(d.l)
	for s := 0; s < d.series(); s++ {
		for j := 0; j < d.outLen; j++ {
			if !mustBeNaN(man, d, s, j, l) {
				continue
			}
			if v := ref.Out[s*d.outLen+j]; !math.IsNaN(v) {
				c.fail(testNaN, "参数 %v、序列 %d（种类 %d）、输出 %d：窗口按规则应输出 NaN，得到 %v", params, s, s%genKinds, j, v)
				return
			}
		}
	}
}

// mustBeNaN 按第 5 节的规则判断输出 (s, j) 是否必须是 NaN。
func mustBeNaN(man *Manifest, d *dataSet, s, j, l int) bool {
	a, b := j, j+l-1
	if man.NaN == NaNStrict {
		for k := range d.inputs {
			for i := a; i <= b; i++ {
				if math.IsNaN(d.inputs[k][s][i]) {
					return true
				}
			}
		}
		return false
	}
	start := a
	for k := range d.inputs {
		x := d.inputs[k][s]
		i := a
		for i <= b && math.IsNaN(x[i]) { // 开头的 NaN 段
			i++
		}
		for p := i; p <= b; p++ { // 有效值之后不允许再出现 NaN
			if math.IsNaN(x[p]) {
				return true
			}
		}
		start = max(start, i)
	}
	return start > b || b+1-start < man.MinValid
}

// runFixtures 跑 fixtures 对拍，并把期望输出之外的实际输出并入摘要。
func (c *conformance) runFixtures(fixtures FixtureSet) error {
	if len(fixtures) == 0 {
		return nil
	}
	in, err := c.newInstance()
	if err != nil {
		return fmt.Errorf("创建测试实例：%w", err)
	}
	defer in.close(c.ctx)
	man := c.m.Info.Manifest
	for _, name := range fixtures.names() {
		var f FixtureFile
		if err := json.Unmarshal(fixtures[name], &f); err != nil {
			c.fail(testFixtures, "%s 解析失败：%v", name, err)
			continue
		}
		for _, fc := range f.Cases {
			if len(fc.Inputs) != len(man.Inputs) || len(fc.Params) != len(man.Params) {
				c.fail(testFixtures, "%s / %s：输入或参数个数与元数据不符", name, fc.Name)
				continue
			}
			params := make([]float64, len(fc.Params))
			for i, p := range fc.Params {
				params[i] = float64(p)
			}
			l, err := man.LookbackFor(params)
			if err != nil {
				c.fail(testFixtures, "%s / %s：%v", name, fc.Name, err)
				continue
			}
			outLen := len(fc.Expected)
			inLen := outLen + int(l) - 1
			if len(fc.Inputs[0]) != inLen {
				c.fail(testFixtures, "%s / %s：输入长度 %d 应为输出长度 %d + 窗口长度 %d − 1", name, fc.Name, len(fc.Inputs[0]), outLen, l)
				continue
			}
			b := &Batch{NSeries: 1, InLen: inLen, OutLen: outLen, Params: params, Out: make([]float64, outLen), keepInf: true}
			for _, series := range fc.Inputs {
				flat := make([]float64, len(series))
				for i, v := range series {
					flat[i] = float64(v)
				}
				b.Inputs = append(b.Inputs, flat)
			}
			if err := c.run(in, b); err != nil {
				c.fail(testFixtures, "%s / %s：执行失败：%v", name, fc.Name, err)
				var ce *CallError
				if errors.As(err, &ce) {
					in.close(c.ctx)
					if in, err = c.newInstance(); err != nil {
						return err
					}
				}
				continue
			}
			for i, want := range fc.Expected {
				got := b.Out[i]
				if !fixtureMatch(got, float64(want), fc.RelTol) {
					c.fail(testFixtures, "%s / %s：第 %d 个输出 %v 与期望 %v 不一致", name, fc.Name, i, got, float64(want))
					break
				}
			}
			for _, v := range b.Out {
				var buf [8]byte
				if math.IsNaN(v) {
					v = canonicalNaN
				}
				binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
				c.digest.Write(buf[:])
			}
		}
	}
	return nil
}

func fixtureMatch(got, want, relTol float64) bool {
	if math.IsNaN(got) || math.IsNaN(want) {
		return math.IsNaN(got) && math.IsNaN(want)
	}
	if relTol == 0 {
		return math.Float64bits(got) == math.Float64bits(want)
	}
	return math.Abs(got-want) <= relTol*math.Max(math.Abs(want), math.SmallestNonzeroFloat64)
}
