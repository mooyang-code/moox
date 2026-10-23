package factorexpr

import (
	"strconv"
	"strings"
)

// Kind 是表达式的类型（设计 4.3）。
type Kind int

const (
	KindNumber      Kind = iota + 1 // 数值序列
	KindBool                        // 布尔序列（三值：真、假、NaN）
	KindNumberConst                 // 数值常量
	KindBoolConst                   // 布尔常量
)

func (k Kind) isConst() bool  { return k == KindNumberConst || k == KindBoolConst }
func (k Kind) isNumber() bool { return k == KindNumber || k == KindNumberConst }
func (k Kind) isBool() bool   { return k == KindBool || k == KindBoolConst }

func (k Kind) describe() string {
	switch k {
	case KindNumber:
		return "数值序列"
	case KindBool:
		return "布尔序列"
	case KindNumberConst:
		return "数值常量"
	case KindBoolConst:
		return "布尔常量"
	}
	return "未知类型"
}

// 叶子节点的 Op。
const (
	OpColumn = "col"
	OpConst  = "const"
	// OpParam 是可调参数：每条序列一个值，在整个计算区间上不随时间变化（目标期的快照覆盖整个回看区间）。
	OpParam = "param"
)

// Node 是规范化后的表达式树节点：let 已内联、常量已折叠。同一个因子内，被多处引用的 let 共享同一个节点。
type Node struct {
	Op       string
	Kind     Kind
	Function *Function // 列、常量与参数为 nil
	Column   string    // Op == OpColumn
	Param    string    // Op == OpParam：参数名
	Value    float64   // Op == OpConst；布尔常量为 1 或 0
	IsInt    bool      // 常量是否为整数
	Args     []*Node   // 序列、常量或参数子节点
	// Ints 是窗口、位移等整数参数；IntParams 与它等长，非空的位置由同名参数提供（Ints 里是默认值）。
	Ints      []int
	IntParams []string
	// Floats 是分位数等数值参数；FloatParams 与它等长，含义同 IntParams。
	Floats      []float64
	FloatParams []string
	// Scalar 为真表示节点不依赖输入列，只由参数（和常量）决定：每条序列一个值，不随时间变化。
	Scalar bool
	// Lookback 是该节点在时刻 t 有效所需的行数（设计 6.2），参数取最坏情况；常量与参数为 0。
	Lookback int
	// back、shift 是需求区间的最坏扩展：back 取参数范围内的最大值，shift 取最小值（delay 的需求区间可能整体前移）。
	back  int
	shift int
	cross bool
	size  int
}

// HasParamSlots 报告节点的整数或数值参数里是否有可调参数。
func (n *Node) HasParamSlots() bool {
	for _, name := range n.IntParams {
		if name != "" {
			return true
		}
	}
	for _, name := range n.FloatParams {
		if name != "" {
			return true
		}
	}
	return false
}

// CrossSection 报告该节点的子树里是否有截面算子。
func (n *Node) CrossSection() bool { return n.cross }

// Canonical 返回节点的规范文本：函数调用形式，常量按数值书写。只改格式、注释或 let 名不会改变它。
// 文本按展开后的树写出；编译保证一个因子展开后的节点不超过 MaxFactorNodes，标识符不超过 MaxIdentifierRunes，长度有上界。
func (n *Node) Canonical() string {
	var b strings.Builder
	n.writeCanonical(&b)
	return b.String()
}

// writeCanonical 把规范文本直接写进同一个缓冲区，不在节点上缓存子树文本：逐节点缓存会让链式 let 的总占用按平方增长。
func (n *Node) writeCanonical(b *strings.Builder) {
	switch n.Op {
	case OpColumn:
		b.WriteString(n.Column)
		return
	case OpConst:
		b.WriteString(formatConst(n))
		return
	case OpParam:
		b.WriteString("$" + n.Param)
		return
	}
	b.WriteString(n.Op)
	b.WriteByte('(')
	first := true
	separate := func() {
		if !first {
			b.WriteByte(',')
		}
		first = false
	}
	for _, arg := range n.Args {
		separate()
		arg.writeCanonical(b)
	}
	for i, value := range n.Ints {
		separate()
		if i < len(n.IntParams) && n.IntParams[i] != "" {
			b.WriteString("$" + n.IntParams[i])
			continue
		}
		b.WriteString(strconv.Itoa(value))
	}
	for i, value := range n.Floats {
		separate()
		if i < len(n.FloatParams) && n.FloatParams[i] != "" {
			b.WriteString("$" + n.FloatParams[i])
			continue
		}
		b.WriteString(formatFloat(value))
	}
	b.WriteByte(')')
}

func formatConst(n *Node) string {
	if n.Kind == KindBoolConst {
		if n.Value != 0 {
			return "true"
		}
		return "false"
	}
	return formatFloat(n.Value)
}

func formatFloat(value float64) string {
	if value == 0 {
		return "0" // 不区分 -0 与 0
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func newColumn(name string) *Node {
	return &Node{Op: OpColumn, Kind: KindNumber, Column: name, Lookback: 1, size: 1}
}

func newParam(param Param) *Node {
	return &Node{Op: OpParam, Kind: KindNumber, Param: param.Name, IsInt: param.IsInt(), Value: param.Default, Scalar: true, size: 1}
}

func newNumber(value float64, isInt bool) *Node {
	return &Node{Op: OpConst, Kind: KindNumberConst, Value: value, IsInt: isInt, size: 1}
}

func newBool(value bool) *Node {
	node := &Node{Op: OpConst, Kind: KindBoolConst, size: 1}
	if value {
		node.Value = 1
	}
	return node
}

func addSize(a, b int) int {
	if sum := a + b; sum <= MaxFactorNodes+1 {
		return sum
	}
	return MaxFactorNodes + 1
}
