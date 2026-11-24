// Package seriesformat 把各种格式的历史时序数据文件解析成列式的批（设计 7.2）：统一的 Decoder 接口按格式名注册，
// csv 是第一个实现，以后加 parquet、jsonl、交易所历史数据包只是多注册一个实现；
// 校验层（Checker）与格式无关：字段齐全、时间按 UTC 解析、按频率对齐、按序列连续。
package seriesformat

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// FieldType 是数据集字段的值类型。
type FieldType string

const (
	TypeDouble FieldType = "double"
	TypeInt    FieldType = "int"
	TypeBool   FieldType = "bool"
	TypeString FieldType = "string"
	TypeTime   FieldType = "time"
	TypeJSON   FieldType = "json"
	TypeBytes  FieldType = "bytes"
)

// Field 是数据集的一个字段（不含时间列与 series_tag）。
type Field struct {
	Name string
	Type FieldType
}

// Column 是一列数据，按字段类型使用对应的切片；Null[i] 为真表示该行该字段为空。
type Column struct {
	Null []bool
	F    []float64 // double
	I    []int64   // int、bool（0/1）
	S    []string  // string、time、json、bytes
}

// Batch 是列式的一批行：Cols 与 Schema.Fields 一一对应。
type Batch struct {
	Times []time.Time // UTC
	Tags  []string
	Cols  []Column
}

// Len 返回批里的行数。
func (b *Batch) Len() int {
	if b == nil {
		return 0
	}
	return len(b.Times)
}

// Options 是打开一个数据文件的参数。
type Options struct {
	// Fields 是数据集的全部字段：文件必须带全部字段，缺哪个报哪个。
	Fields []Field
	// TimeColumn 是时间列名，默认 data_time。
	TimeColumn string
	// SeriesTag 非空时给所有行统一指定 series_tag（文件里不能再有 series_tag 列）。
	SeriesTag string
}

// TimeColumnName 返回时间列名。
func (o Options) TimeColumnName() string {
	if o.TimeColumn == "" {
		return "data_time"
	}
	return o.TimeColumn
}

// BatchReader 按批读取一个数据源。
type BatchReader interface {
	// Next 读取最多 max 行；读完返回 io.EOF（可能同时返回最后一批）。
	Next(max int) (*Batch, error)
	Close() error
}

// Decoder 是一种文件格式的解析器。
type Decoder interface {
	// Format 是格式名（小写），例如 csv。
	Format() string
	// Open 读取 r，先校验表头 / 元数据，再按批产出行。
	Open(r io.Reader, opts Options) (BatchReader, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Decoder{}
)

// Register 注册一个格式解析器；格式名重复会 panic（注册发生在包初始化里）。
func Register(d Decoder) {
	mu.Lock()
	defer mu.Unlock()
	name := d.Format()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("seriesformat: 格式 %q 重复注册", name))
	}
	registry[name] = d
}

// Lookup 按格式名取得解析器。
func Lookup(format string) (Decoder, error) {
	mu.RLock()
	defer mu.RUnlock()
	if d, ok := registry[format]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("不支持的数据格式 %q（已支持：%v）", format, formatsLocked())
}

// Formats 返回已注册的格式名。
func Formats() []string {
	mu.RLock()
	defer mu.RUnlock()
	return formatsLocked()
}

func formatsLocked() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ErrMissingFields 表示文件缺少数据集的字段。
type ErrMissingFields struct{ Fields []string }

func (e *ErrMissingFields) Error() string {
	return fmt.Sprintf("文件缺少数据集字段 %v（导入必须带数据集的全部字段）", e.Fields)
}

// ErrUnknownColumns 表示文件里有数据集没有的列。
type ErrUnknownColumns struct{ Columns []string }

func (e *ErrUnknownColumns) Error() string {
	return fmt.Sprintf("文件里的列 %v 不是数据集字段", e.Columns)
}

// NewBatch 按字段表创建空批。
func NewBatch(fields []Field, capacity int) *Batch {
	return &Batch{Times: make([]time.Time, 0, capacity), Tags: make([]string, 0, capacity), Cols: make([]Column, len(fields))}
}

// appendRow 追加一行的时间与标签；各列的值由调用方随后追加。
func (b *Batch) appendRow(at time.Time, tag string) {
	b.Times = append(b.Times, at)
	b.Tags = append(b.Tags, tag)
}

var errClosed = errors.New("seriesformat: 读取器已关闭")
