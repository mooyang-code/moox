package factorexpr

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ProgramInput 是程序的一个成员：一个编译好的因子及其内部身份。
type ProgramInput struct {
	// Key 是成员在程序内的唯一身份（例如 “集合/别名”）；参数槽与输出都挂在它下面，不同集合的同名别名互不冲突。
	Key      string
	FactorID string
	Factor   *Factor
	// Universe 是成员所在集合的标的范围标识：截面算子只在这个范围内计算，依赖截面算子的节点按它区分。
	// 空串表示面板上的全部序列。
	Universe string
}

// ProgramNode 是去重后的计算图节点。Args 是子节点在 Program.Nodes 中的下标，子节点一定排在父节点之前。
type ProgramNode struct {
	Index    int
	Op       string
	Kind     Kind
	Function *Function
	Column   string
	// Param 是参数节点（Op == OpParam）的参数槽。
	Param string
	Value float64
	Args  []int
	// Ints、Floats 是整数与数值参数（参数槽的位置是默认值）；IntParams、FloatParams 与之等长，非空的位置是参数槽。
	Ints        []int
	IntParams   []string
	Floats      []float64
	FloatParams []string
	// Scalar 为真表示节点只由参数与常量决定：每条序列一个值，不随时间变化。
	Scalar bool
	// Cross 为真表示子树里有截面算子；Universe 是这类节点所属的标的范围。
	Cross    bool
	Universe string
	// Back、Shift 是需求区间的最坏扩展（参数按声明范围）。
	Back     int
	Shift    int
	Lookback int
	// LastUse 是最后一个使用它的节点的下标；输出节点为 len(Nodes)，表示保留到取结果之后。
	LastUse int
}

// HasParamSlots 报告节点的整数或数值参数里是否有参数槽。
func (n *ProgramNode) HasParamSlots() bool {
	for _, slot := range n.IntParams {
		if slot != "" {
			return true
		}
	}
	for _, slot := range n.FloatParams {
		if slot != "" {
			return true
		}
	}
	return false
}

// ProgramOutput 是一个输出列对应的节点。
type ProgramOutput struct {
	Key      string
	FactorID string
	Name     string
	Node     int
}

// ProgramParam 是程序里的一个参数槽：成员 Key 的参数 Name。
type ProgramParam struct {
	Slot    string
	Key     string
	Name    string
	Param   Param
	Default float64
}

// Program 是若干成员合并后的计算图（设计 6.1）：规范键相同的子树只保留一个节点。参数槽按成员区分，
// 所以只有不含参数的子树会在成员之间共享；参数取值相同的成员由执行器在每次运行时再去重。
type Program struct {
	Nodes    []*ProgramNode
	Outputs  []ProgramOutput
	Params   []ProgramParam
	Columns  []string
	Lookback int
}

// ParamSlot 返回成员 key 的参数 name 的参数槽。
func ParamSlot(key, name string) string { return key + "#" + name }

// NewProgram 合并多个成员。成员 Key 在程序内必须唯一。
func NewProgram(inputs []ProgramInput) (*Program, error) {
	program := &Program{Lookback: 1}
	interned := map[string]int{}
	columns := map[string]bool{}
	keys := map[string]bool{}
	for _, input := range inputs {
		if input.Factor == nil {
			return nil, fmt.Errorf("因子 %s 没有编译结果", input.FactorID)
		}
		if input.Key == "" {
			// 只有一个定义的程序（试算、离线工具）用定义 ID 作为成员身份。
			input.Key = input.FactorID
		}
		if input.Key == "" {
			return nil, fmt.Errorf("成员身份为空")
		}
		if keys[input.Key] {
			return nil, fmt.Errorf("成员 %s 重复", input.Key)
		}
		keys[input.Key] = true
		for _, param := range input.Factor.Params {
			program.Params = append(program.Params, ProgramParam{Slot: ParamSlot(input.Key, param.Name), Key: input.Key,
				Name: param.Name, Param: param, Default: param.Default})
		}
		slot := func(name string) string {
			if name == "" {
				return ""
			}
			return ParamSlot(input.Key, name)
		}
		memo := map[*Node]int{}
		var visit func(*Node) int
		visit = func(node *Node) int {
			if index, ok := memo[node]; ok {
				return index
			}
			args := make([]int, len(node.Args))
			for i, arg := range node.Args {
				args[i] = visit(arg)
			}
			candidate := &ProgramNode{
				Op: node.Op, Kind: node.Kind, Function: node.Function, Column: node.Column, Value: node.Value, Args: args,
				Ints: append([]int(nil), node.Ints...), Floats: append([]float64(nil), node.Floats...),
				Scalar: node.Scalar, Cross: node.cross, Back: node.back, Shift: node.shift, Lookback: node.Lookback,
			}
			if node.Op == OpParam {
				candidate.Param = slot(node.Param)
			}
			for _, name := range node.IntParams {
				candidate.IntParams = append(candidate.IntParams, slot(name))
			}
			for _, name := range node.FloatParams {
				candidate.FloatParams = append(candidate.FloatParams, slot(name))
			}
			if candidate.Cross {
				candidate.Universe = input.Universe
			}
			key := programKey(candidate)
			index, ok := interned[key]
			if !ok {
				index = len(program.Nodes)
				candidate.Index = index
				program.Nodes = append(program.Nodes, candidate)
				interned[key] = index
				if node.Op == OpColumn {
					columns[node.Column] = true
				}
			}
			memo[node] = index
			return index
		}
		for _, output := range input.Factor.Outputs {
			program.Outputs = append(program.Outputs, ProgramOutput{Key: input.Key, FactorID: input.FactorID, Name: output.Name,
				Node: visit(output.Root)})
		}
		program.Lookback = max(program.Lookback, input.Factor.Lookback)
	}
	for _, node := range program.Nodes {
		node.LastUse = node.Index
		for _, arg := range node.Args {
			program.Nodes[arg].LastUse = max(program.Nodes[arg].LastUse, node.Index)
		}
	}
	for _, output := range program.Outputs {
		program.Nodes[output.Node].LastUse = len(program.Nodes)
	}
	for column := range columns {
		program.Columns = append(program.Columns, column)
	}
	sort.Strings(program.Columns)
	return program, nil
}

// programKey 是节点的去重键：算子、子节点下标、常量参数、参数槽，以及截面依赖节点的标的范围。
// 子节点已先去重，所以下标相同即子树相同。
func programKey(node *ProgramNode) string {
	var b strings.Builder
	b.WriteString(node.Op)
	switch node.Op {
	case OpColumn:
		b.WriteString(":" + node.Column)
	case OpConst:
		b.WriteString(":" + strconv.Itoa(int(node.Kind)) + ":" + formatFloat(node.Value))
	case OpParam:
		b.WriteString(":" + node.Param)
	}
	b.WriteByte('(')
	for i, arg := range node.Args {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(arg))
	}
	b.WriteString(";")
	for i, value := range node.Ints {
		if node.IntParams[i] != "" {
			b.WriteString("$" + node.IntParams[i] + ",")
			continue
		}
		b.WriteString(strconv.Itoa(value) + ",")
	}
	b.WriteString(";")
	for i, value := range node.Floats {
		if node.FloatParams[i] != "" {
			b.WriteString("$" + node.FloatParams[i] + ",")
			continue
		}
		b.WriteString(formatFloat(value) + ",")
	}
	if node.Cross {
		b.WriteString(";u=" + node.Universe)
	}
	b.WriteByte(')')
	return b.String()
}
