package factorexpr

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ProgramInput 是集合程序的一个因子。
type ProgramInput struct {
	FactorID string
	Factor   *Factor
}

// ProgramNode 是去重后的计算图节点。Args 是子节点在 Program.Nodes 中的下标，子节点一定排在父节点之前。
type ProgramNode struct {
	Index    int
	Op       string
	Kind     Kind
	Function *Function
	Column   string
	Value    float64
	Args     []int
	Ints     []int
	Floats   []float64
	Lookback int
	// LastUse 是最后一个使用它的节点的下标；输出节点为 len(Nodes)，表示保留到取结果之后。
	LastUse int
}

// ProgramOutput 是一个输出列对应的节点。
type ProgramOutput struct {
	FactorID string
	Name     string
	Node     int
}

// Program 是一个因子集全部因子合并后的计算图（设计 6.1）：规范键相同的子树只保留一个节点。
type Program struct {
	Nodes    []*ProgramNode
	Outputs  []ProgramOutput
	Columns  []string
	Lookback int
}

// NewProgram 合并多个因子。输出列名在集合内必须唯一。
func NewProgram(inputs []ProgramInput) (*Program, error) {
	program := &Program{Lookback: 1}
	interned := map[string]int{}
	columns := map[string]bool{}
	outputNames := map[string]string{}
	for _, input := range inputs {
		if input.Factor == nil {
			return nil, fmt.Errorf("因子 %s 没有编译结果", input.FactorID)
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
			key := programKey(node, args)
			index, ok := interned[key]
			if !ok {
				index = len(program.Nodes)
				program.Nodes = append(program.Nodes, &ProgramNode{
					Index: index, Op: node.Op, Kind: node.Kind, Function: node.Function, Column: node.Column, Value: node.Value,
					Args: args, Ints: append([]int(nil), node.Ints...), Floats: append([]float64(nil), node.Floats...),
					Lookback: node.Lookback,
				})
				interned[key] = index
				if node.Op == OpColumn {
					columns[node.Column] = true
				}
			}
			memo[node] = index
			return index
		}
		for _, output := range input.Factor.Outputs {
			if previous, duplicate := outputNames[output.Name]; duplicate {
				return nil, fmt.Errorf("输出列 %s 在因子 %s 与 %s 中重复", output.Name, previous, input.FactorID)
			}
			outputNames[output.Name] = input.FactorID
			program.Outputs = append(program.Outputs, ProgramOutput{FactorID: input.FactorID, Name: output.Name, Node: visit(output.Root)})
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

// programKey 是节点的去重键：算子、子节点下标与常量参数。子节点已先去重，所以下标相同即子树相同。
func programKey(node *Node, args []int) string {
	var b strings.Builder
	b.WriteString(node.Op)
	switch node.Op {
	case OpColumn:
		b.WriteString(":" + node.Column)
	case OpConst:
		b.WriteString(":" + strconv.Itoa(int(node.Kind)) + ":" + formatFloat(node.Value))
	}
	b.WriteByte('(')
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(arg))
	}
	b.WriteString(";")
	for _, value := range node.Ints {
		b.WriteString(strconv.Itoa(value) + ",")
	}
	b.WriteString(";")
	for _, value := range node.Floats {
		b.WriteString(formatFloat(value) + ",")
	}
	b.WriteByte(')')
	return b.String()
}
