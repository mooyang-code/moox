package factorudf

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// canonicalNaN 是输出统一使用的 NaN 位模式：wasm 规范允许运算产生的 NaN 的符号与负载不确定，
// 宿主在边界统一，业务结果不得依赖 NaN 的位模式。
var canonicalNaN = math.Float64frombits(0x7ff8000000000000)

// Batch 是一次 guest 调用的输入输出（同一个 UDF、同一个窗口长度 L 的一批序列）。
//
// 布局都是 [序列][...]：Inputs[k] 是第 k 个输入，长度 NSeries*InLen；Params 长度 NSeries*参数个数（按元数据声明顺序，
// 整数参数以 f64 精确传入）；Out 长度 NSeries*OutLen。InLen = OutLen + L − 1，Out[s][j] 对应以 Inputs[k][s][j+L−1] 结尾的窗口。
type Batch struct {
	NSeries int
	InLen   int
	OutLen  int
	Inputs  [][]float64
	Params  []float64
	Out     []float64

	// 以下只给一致性测试用：在各区域前后填入垃圾值（检查回看外读取），保留 ±Inf（检查有限性）。
	padElems int
	padSeed  uint64
	keepInf  bool
}

// bytes 返回这批数据在暂停区里的字节数（含垃圾填充），计算过程检查溢出。
func (b *Batch) bytes(nParams int) (int64, error) {
	if b.NSeries < 1 || b.InLen < 1 || b.OutLen < 1 || b.InLen < b.OutLen {
		return 0, fmt.Errorf("批的维度不合法：序列 %d、输入长 %d、输出长 %d", b.NSeries, b.InLen, b.OutLen)
	}
	elems, err := b.elems(nParams)
	if err != nil {
		return 0, err
	}
	if elems > math.MaxInt32/8 {
		return 0, fmt.Errorf("批太大：%d 个元素超出 32 位寻址", elems)
	}
	return elems * 8, nil
}

func (b *Batch) elems(nParams int) (int64, error) {
	const limit = int64(1) << 40
	total := int64(0)
	add := func(a, c int64) error {
		if a < 0 || c < 0 || a > limit || c > limit || a*c > limit {
			return errors.New("批的元素个数溢出")
		}
		total += a * c
		if total > limit {
			return errors.New("批的元素个数溢出")
		}
		return nil
	}
	pad := int64(b.padElems)
	if err := add(int64(b.NSeries), int64(nParams)); err != nil {
		return 0, err
	}
	if err := add(int64(b.NSeries)*int64(len(b.Inputs)), int64(b.InLen)); err != nil {
		return 0, err
	}
	if err := add(int64(b.NSeries), int64(b.OutLen)); err != nil {
		return 0, err
	}
	if pad > 0 {
		total += pad * 6
	}
	return total, nil
}

// splitmix 是确定的伪随机数发生器（垃圾值与合成数据都用它，跨平台一致）。
type splitmix uint64

func (s *splitmix) next() uint64 {
	*s += 0x9e3779b97f4a7c15
	z := uint64(*s)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func putF64(dst []byte, src []float64) {
	for i, v := range src {
		binary.LittleEndian.PutUint64(dst[i*8:], math.Float64bits(v))
	}
}

func fillJunk(dst []byte, seed *splitmix) {
	for i := 0; i+8 <= len(dst); i += 8 {
		binary.LittleEndian.PutUint64(dst[i:], seed.next())
	}
}

// classifyCall 把 wazero 返回的调用错误归类；预算在分配或增长时拒绝过时，失败原因是预算（guest 的增长失败会表现成 trap）。
func (in *Instance) classifyCall(ctx context.Context, err error) *CallError {
	if in.meter.Denied() {
		return &CallError{Reason: ReasonBudget, Err: fmt.Errorf("%w：线性内存增长被预算拒绝", ErrBudget)}
	}
	return classify(ctx, err)
}

// Eval 在实例上执行一批。出错（trap、超时、预算、非零返回码）后实例不可再用，调用方要丢弃。
func (in *Instance) Eval(ctx context.Context, b *Batch) error {
	man := in.m.Info.Manifest
	nParams := len(man.Params)
	if len(b.Inputs) != len(man.Inputs) {
		return &CallError{Reason: ReasonInput, Err: fmt.Errorf("输入个数 %d 与元数据 %d 不一致", len(b.Inputs), len(man.Inputs))}
	}
	if len(b.Params) != b.NSeries*nParams {
		return &CallError{Reason: ReasonInput, Err: fmt.Errorf("参数个数 %d 与 序列数×参数个数 %d 不一致", len(b.Params), b.NSeries*nParams)}
	}
	if len(b.Out) != b.NSeries*b.OutLen {
		return &CallError{Reason: ReasonInput, Err: fmt.Errorf("输出长度 %d 与 序列数×输出长 %d 不一致", len(b.Out), b.NSeries*b.OutLen)}
	}
	for k, in := range b.Inputs {
		if len(in) != b.NSeries*b.InLen {
			return &CallError{Reason: ReasonInput, Err: fmt.Errorf("输入 %d 的长度 %d 与 序列数×输入长 %d 不一致", k, len(in), b.NSeries*b.InLen)}
		}
	}
	size, err := b.bytes(nParams)
	if err != nil {
		return &CallError{Reason: ReasonInput, Err: err}
	}
	cctx, cancel := context.WithTimeout(ctx, in.m.rt.limits.CallTimeout)
	defer cancel()

	res, err := in.reserve.Call(cctx, uint64(uint32(size)))
	if err != nil {
		return in.classifyCall(cctx, err)
	}
	base := uint32(res[0])
	if base == 0 {
		if in.meter.Denied() {
			return &CallError{Reason: ReasonBudget, Err: fmt.Errorf("%w：暂存区申请被预算拒绝", ErrBudget)}
		}
		return &CallError{Reason: ReasonMemory}
	}
	if int64(size) > in.scratch {
		in.scratch = int64(size)
	}
	buf, ok := in.mem.Read(base, uint32(size))
	if !ok {
		return &CallError{Reason: ReasonMemory, Err: errors.New("暂停区超出线性内存")}
	}
	// 区域布局：[垃圾][参数][垃圾][输入][垃圾][输出][垃圾]，没有垃圾填充时就是紧挨着。
	junk := splitmix(b.padSeed)
	off := 0
	pad := func() {
		n := b.padElems * 8
		if n > 0 {
			fillJunk(buf[off:off+n], &junk)
			off += n
		}
	}
	pad()
	paramsOff := off
	putF64(buf[off:], b.Params)
	off += len(b.Params) * 8
	pad()
	inputsOff := off
	for _, in := range b.Inputs {
		putF64(buf[off:], in)
		off += len(in) * 8
	}
	pad()
	outOff := off
	// 输出区先填 NaN：guest 没写的位置读回来是 NaN，而不是上一次调用留下的旧值。
	for i := 0; i < len(b.Out); i++ {
		binary.LittleEndian.PutUint64(buf[off+i*8:], math.Float64bits(canonicalNaN))
	}
	off += len(b.Out) * 8
	pad()

	args := []uint64{
		uint64(b.NSeries), uint64(b.InLen), uint64(b.OutLen),
		uint64(base) + uint64(paramsOff), uint64(base) + uint64(inputsOff), uint64(base) + uint64(outOff),
	}
	rc, err := in.eval.Call(cctx, args...)
	if err != nil {
		return in.classifyCall(cctx, err)
	}
	if code := int32(rc[0]); code != 0 {
		return &CallError{Reason: ReasonCode, Code: code}
	}
	// guest 内存可能在调用中增长，每次调用后重新取视图，不跨调用缓存。
	view, ok := in.mem.Read(base, uint32(size))
	if !ok {
		return &CallError{Reason: ReasonMemory, Err: errors.New("暂停区超出线性内存")}
	}
	for i := range b.Out {
		v := math.Float64frombits(binary.LittleEndian.Uint64(view[outOff+i*8:]))
		switch {
		case math.IsNaN(v):
			v = canonicalNaN
		case math.IsInf(v, 0) && !b.keepInf:
			// 输出只能是有限值或 NaN；SDK 已经转换，这里兜底。
			v = canonicalNaN
		}
		b.Out[i] = v
	}
	return nil
}

// Eval 借出一个实例执行一批，成功归还、出错丢弃。
func (m *Module) Eval(ctx context.Context, b *Batch) error {
	in, err := m.Acquire(ctx)
	if err != nil {
		return err
	}
	if err := in.Eval(ctx, b); err != nil {
		m.Discard(ctx, in)
		return err
	}
	m.Release(ctx, in)
	return nil
}
