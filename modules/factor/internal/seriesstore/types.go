// Package seriesstore 是因子机本地的时序存储：Pebble 持久化、按块列式编码，数据永不删除。
// 每个标的的本地数据在 [Earliest, Latest] 上按频率网格连续（缺的周期写成带标记的填充行），
// 所以「同步到哪里了」只看 Latest。写入只有一种语义：整段替换（设计第 3 节）。
package seriesstore

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Source 标识一个数据源：空间、数据集和频率。
type Source struct{ SpaceID, DatasetID, Freq string }

func (s Source) String() string { return s.SpaceID + "/" + s.DatasetID + "/" + s.Freq }

// FieldType 是字段的存储类型。
type FieldType uint8

const (
	TypeFloat64 FieldType = iota + 1
	TypeInt64
	TypeBool
	TypeString // 字符串、时间、JSON 都按字节串存放
)

func (t FieldType) valid() bool { return t >= TypeFloat64 && t <= TypeString }

// FillRule 是缺失行里该字段的填充规则（设计 3.4）。
type FillRule uint8

const (
	FillPrev      FillRule = iota // 取上一期该字段的值（默认）
	FillPrevClose                 // 取上一期的 close（给 open/high/low/close 用）
	FillZero                      // 填 0（给 volume 等量类字段用）
)

// CloseField 是 FillPrevClose 取值的字段名。
const CloseField = "close"

// Field 是数据集的一个字段。
type Field struct {
	Name string
	Type FieldType
	Fill FillRule
}

// Column 是一列数据，按 Type 使用对应的切片；Null[i] 为真表示该行该字段为 NULL。
type Column struct {
	Null []bool
	F    []float64 // TypeFloat64
	I    []int64   // TypeInt64、TypeBool（0/1）
	S    []string  // TypeString
}

// Columns 是列式的一批行，行身份是 (Times[i], Tags[i])。
type Columns struct {
	Fields []Field
	Times  []int64 // UTC 秒
	Tags   []string
	Filled []bool // 该行是填充行
	Cols   []Column
}

// NewColumns 创建空的列集合。
func NewColumns(fields []Field, capacity int) *Columns {
	c := &Columns{
		Fields: append([]Field(nil), fields...),
		Times:  make([]int64, 0, capacity), Tags: make([]string, 0, capacity), Filled: make([]bool, 0, capacity),
		Cols: make([]Column, len(fields)),
	}
	return c
}

// Len 返回行数。
func (c *Columns) Len() int {
	if c == nil {
		return 0
	}
	return len(c.Times)
}

// FieldIndex 返回字段下标，没有返回 -1。
func (c *Columns) FieldIndex(name string) int {
	for i, f := range c.Fields {
		if f.Name == name {
			return i
		}
	}
	return -1
}

// Value 是一个单元格的值，供追加行和测试使用。
type Value struct {
	Null bool
	F    float64
	I    int64
	S    string
}

// Float 构造数值。
func Float(v float64) Value { return Value{F: v} }

// Int 构造整数值。
func Int(v int64) Value { return Value{I: v} }

// Str 构造字符串值。
func Str(v string) Value { return Value{S: v} }

// Bool 构造布尔值。
func Bool(v bool) Value {
	if v {
		return Value{I: 1}
	}
	return Value{}
}

// NullValue 是 NULL。
var NullValue = Value{Null: true}

// AppendRow 追加一行；values 与 Fields 一一对应。
func (c *Columns) AppendRow(at time.Time, tag string, filled bool, values ...Value) {
	c.appendRaw(at.UTC().Unix(), tag, filled)
	for i := range c.Cols {
		var v Value
		if i < len(values) {
			v = values[i]
		} else {
			v = NullValue
		}
		c.appendValue(i, v)
	}
}

func (c *Columns) appendRaw(at int64, tag string, filled bool) {
	c.Times = append(c.Times, at)
	c.Tags = append(c.Tags, tag)
	c.Filled = append(c.Filled, filled)
}

func (c *Columns) appendValue(field int, v Value) {
	col := &c.Cols[field]
	col.Null = append(col.Null, v.Null)
	switch c.Fields[field].Type {
	case TypeFloat64:
		col.F = append(col.F, v.F)
	case TypeInt64, TypeBool:
		col.I = append(col.I, v.I)
	case TypeString:
		col.S = append(col.S, v.S)
	}
}

// ValueAt 取一个单元格。
func (c *Columns) ValueAt(field, row int) Value {
	col := &c.Cols[field]
	if col.Null[row] {
		return NullValue
	}
	switch c.Fields[field].Type {
	case TypeFloat64:
		return Value{F: col.F[row]}
	case TypeInt64, TypeBool:
		return Value{I: col.I[row]}
	default:
		return Value{S: col.S[row]}
	}
}

// copyRow 把 src 的一行复制到 c 末尾；两边的字段必须一致。
func (c *Columns) copyRow(src *Columns, row int) {
	c.appendRaw(src.Times[row], src.Tags[row], src.Filled[row])
	for i := range c.Cols {
		c.appendValue(i, src.ValueAt(i, row))
	}
}

// sortRows 按 (时间, 标签) 排序。
func (c *Columns) sortRows() *Columns {
	n := c.Len()
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if c.Times[x] != c.Times[y] {
			return c.Times[x] < c.Times[y]
		}
		return c.Tags[x] < c.Tags[y]
	})
	out := NewColumns(c.Fields, n)
	for _, row := range order {
		out.copyRow(c, row)
	}
	return out
}

// AppendRowFrom 把 src 的一行追加到 c 末尾；两边的字段必须一致。
func (c *Columns) AppendRowFrom(src *Columns, row int) { c.copyRow(src, row) }

// SortedByKey 返回按（时间、标签）排序的副本。
func (c *Columns) SortedByKey() *Columns { return c.sortRows() }

// Span 是一个左闭右开的时间区间（UTC）。
type Span struct{ From, To time.Time }

// Empty 报告区间是否为空。
func (s Span) Empty() bool { return !s.From.Before(s.To) }

// SubjectWrite 是对一个标的的一次整段替换：用 Rows 替换 [From, To) 内的全部数据。
type SubjectWrite struct {
	Subject  string
	From, To time.Time
	Rows     *Columns
}

// PendingSpan 是待提交的失效重算区间。
type PendingSpan struct {
	Subject string
	From    time.Time
	To      time.Time
	Reason  string
}

// WriteOptions 控制一次写入。
type WriteOptions struct {
	// FillGaps 允许把中间缺的周期写成填充行；否则缺期报 GapError。同步器与带 --allow-missing 的导入打开它。
	FillGaps bool
	// Cursor 非空时随写入原子提交为该数据源的同步位点。
	Cursor []byte
	// RecordPending 为真时，把每个标的实际改变的区间记入失效重算待办（与数据同批提交）。
	RecordPending bool
	// PendingReason 是待办的来源说明。
	PendingReason string
}

// WriteResult 是一个标的写入的结果。
type WriteResult struct {
	Subject string
	// Changed 是实际改变的区间：包括间隙填充和被重新生成的后续填充行，调用方据此覆盖窗口、计算失效重算范围。
	Changed Span
	// Written 是写入的真实行数，Filled 是新生成或重新生成的填充行数，Removed 是被替换掉的旧行数。
	Written, Filled, Removed int
	Earliest, Latest         time.Time
	// PrevLatest 是写入前的最新时间（零值表示此前没有数据）。
	PrevLatest time.Time
}

// Bounds 是标的本地数据的边界。
type Bounds struct{ Earliest, Latest time.Time }

// GapError 表示写入会在本地数据里留下空洞，而调用方没有允许填充。
type GapError struct {
	Subject string
	Missing []time.Time
}

func (e *GapError) Error() string {
	shown := e.Missing
	more := ""
	if len(shown) > 5 {
		shown, more = shown[:5], fmt.Sprintf(" 等共 %d 个", len(e.Missing))
	}
	parts := make([]string, 0, len(shown))
	for _, t := range shown {
		parts = append(parts, t.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("标的 %s 的数据不连续，缺少周期 %v%s", e.Subject, parts, more)
}

var (
	// ErrLayoutMismatch 表示本地目录的布局版本与程序不一致。
	ErrLayoutMismatch = errors.New("本地时序存储布局版本不一致")
	// ErrSchema 表示写入的字段与已记录的数据集字段不兼容。
	ErrSchema = errors.New("字段与本地记录的数据集字段不兼容")
	// ErrInvalidWrite 表示写入参数不合法。
	ErrInvalidWrite = errors.New("写入参数不合法")
)
