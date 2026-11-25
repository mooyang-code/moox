// Package seriesformat 把历史时序数据的 CSV 文件（.csv 或 .csv.gz）解析成列式的批（OpenCSV），
// 并做通用校验（Checker）：字段齐全、时间按 UTC 解析、按频率对齐、按序列（标的、series_tag）连续。
package seriesformat

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
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

// Batch 是列式的一批行：Cols 与 Options.Fields 一一对应。Subjects 只在多标的文件（带 subject_id 列）里有值，
// 是每行的标的；单标的文件为 nil，标的取文件名或 --subject。
type Batch struct {
	Times    []time.Time // UTC
	Tags     []string
	Subjects []string
	Cols     []Column
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
	// SubjectColumn 为真时表头的 subject_id 列被认作标的列（多标的文件，只写归档时允许）；为假时文件不能有 subject_id 列。
	SubjectColumn bool
	// RequireSeriesTag 为真时每行的 series_tag 必须非空（写归档时：漏写 tag 会在归档里多出一条空 tag 序列）。
	RequireSeriesTag bool
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

// DataFileName 报告文件名是不是可导入的数据文件（.csv 或 .csv.gz，不区分大小写），并返回去掉扩展名的部分（单标的文件的标的 ID）。
func DataFileName(name string) (base string, ok bool) {
	lower := strings.ToLower(name)
	for _, ext := range []string{".csv.gz", ".csv"} {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)], true
		}
	}
	return name, false
}

// Decompress 按内容的魔数判断是否 gzip 压缩，是就套上解压（.csv.gz 与 .csv 走同一个入口）。
func Decompress(r io.Reader) (io.Reader, error) {
	buffered := bufio.NewReader(r)
	magic, err := buffered.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(buffered)
		if err != nil {
			return nil, fmt.Errorf("解压 gzip：%w", err)
		}
		return gz, nil
	}
	return buffered, nil
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

// appendRow 追加一行的时间与标签（多标的文件另追加标的）；各列的值由调用方随后追加。
func (b *Batch) appendRow(at time.Time, tag string) {
	b.Times = append(b.Times, at)
	b.Tags = append(b.Tags, tag)
}

var errClosed = errors.New("seriesformat: 读取器已关闭")
