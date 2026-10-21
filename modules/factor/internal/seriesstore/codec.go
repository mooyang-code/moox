package seriesstore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// 块编码（设计 3.2）：头部（行数、列名与类型表）、时间列、series_tag 字典、已填充位图，
// 然后每个字段一段：空值位图 + 数据。不做应用层压缩，由 Pebble 的块压缩负责。
const (
	blockMagic   = 0x5342 // "SB"
	blockVersion = 1
)

var errCorruptBlock = errors.New("本地时序存储的块已损坏")

func encodeBlock(c *Columns) []byte {
	n := c.Len()
	size := 16 + n*(8+2) + n/8 + 8
	for i := range c.Fields {
		size += len(c.Fields[i].Name) + 4 + n/8 + 1 + n*8
	}
	buf := make([]byte, 0, size)
	buf = binary.LittleEndian.AppendUint16(buf, blockMagic)
	buf = append(buf, blockVersion)
	buf = binary.AppendUvarint(buf, uint64(n))
	buf = binary.AppendUvarint(buf, uint64(len(c.Fields)))
	for _, f := range c.Fields {
		buf = binary.AppendUvarint(buf, uint64(len(f.Name)))
		buf = append(buf, f.Name...)
		buf = append(buf, byte(f.Type))
	}
	for _, t := range c.Times {
		buf = binary.LittleEndian.AppendUint64(buf, uint64(t))
	}
	dict := map[string]uint64{}
	var names []string
	for _, tag := range c.Tags {
		if _, ok := dict[tag]; !ok {
			dict[tag] = uint64(len(names))
			names = append(names, tag)
		}
	}
	buf = binary.AppendUvarint(buf, uint64(len(names)))
	for _, name := range names {
		buf = binary.AppendUvarint(buf, uint64(len(name)))
		buf = append(buf, name...)
	}
	for _, tag := range c.Tags {
		buf = binary.AppendUvarint(buf, dict[tag])
	}
	buf = appendBits(buf, c.Filled)
	for i, f := range c.Fields {
		col := &c.Cols[i]
		buf = appendBits(buf, col.Null)
		switch f.Type {
		case TypeFloat64:
			for _, v := range col.F {
				buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(v))
			}
		case TypeInt64:
			for _, v := range col.I {
				buf = binary.LittleEndian.AppendUint64(buf, uint64(v))
			}
		case TypeBool:
			for _, v := range col.I {
				buf = append(buf, byte(v))
			}
		case TypeString:
			for _, v := range col.S {
				buf = binary.AppendUvarint(buf, uint64(len(v)))
				buf = append(buf, v...)
			}
		}
	}
	return buf
}

func appendBits(buf []byte, bits []bool) []byte {
	start := len(buf)
	buf = append(buf, make([]byte, (len(bits)+7)/8)...)
	for i, b := range bits {
		if b {
			buf[start+i/8] |= 1 << (i % 8)
		}
	}
	return buf
}

type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.pos+n > len(r.b) {
		r.err = errCorruptBlock
		return nil
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out
}

func (r *reader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		r.err = errCorruptBlock
		return 0
	}
	r.pos += n
	return v
}

func (r *reader) bits(n int) []bool {
	raw := r.take((n + 7) / 8)
	if raw == nil && n > 0 {
		return nil
	}
	out := make([]bool, n)
	for i := range out {
		out[i] = raw[i/8]&(1<<(i%8)) != 0
	}
	return out
}

// decodeBlock 解出一个块；返回的 Columns 的 Fields 来自块头部（Fill 规则不在块里，由调用方按数据集字段补上）。
func decodeBlock(data []byte) (*Columns, error) {
	r := &reader{b: data}
	head := r.take(3)
	if r.err != nil || binary.LittleEndian.Uint16(head) != blockMagic || head[2] != blockVersion {
		return nil, errCorruptBlock
	}
	n := int(r.uvarint())
	nf := int(r.uvarint())
	if r.err != nil || n < 0 || n > 1<<24 || nf < 0 || nf > 1<<12 {
		return nil, errCorruptBlock
	}
	fields := make([]Field, nf)
	for i := range fields {
		name := r.take(int(r.uvarint()))
		typ := r.take(1)
		if r.err != nil {
			return nil, r.err
		}
		fields[i] = Field{Name: string(name), Type: FieldType(typ[0])}
		if !fields[i].Type.valid() {
			return nil, errCorruptBlock
		}
	}
	c := NewColumns(fields, n)
	times := r.take(n * 8)
	if r.err != nil {
		return nil, r.err
	}
	for i := 0; i < n; i++ {
		c.Times = append(c.Times, int64(binary.LittleEndian.Uint64(times[i*8:])))
	}
	nd := int(r.uvarint())
	if r.err != nil || nd < 0 || nd > n+1 {
		return nil, errCorruptBlock
	}
	dict := make([]string, nd)
	for i := range dict {
		dict[i] = string(r.take(int(r.uvarint())))
	}
	for i := 0; i < n; i++ {
		idx := int(r.uvarint())
		if r.err != nil || idx >= nd {
			return nil, errCorruptBlock
		}
		c.Tags = append(c.Tags, dict[idx])
	}
	c.Filled = r.bits(n)
	for i, f := range fields {
		col := &c.Cols[i]
		col.Null = r.bits(n)
		switch f.Type {
		case TypeFloat64:
			raw := r.take(n * 8)
			if r.err != nil {
				return nil, r.err
			}
			col.F = make([]float64, n)
			for j := range col.F {
				col.F[j] = math.Float64frombits(binary.LittleEndian.Uint64(raw[j*8:]))
			}
		case TypeInt64:
			raw := r.take(n * 8)
			if r.err != nil {
				return nil, r.err
			}
			col.I = make([]int64, n)
			for j := range col.I {
				col.I[j] = int64(binary.LittleEndian.Uint64(raw[j*8:]))
			}
		case TypeBool:
			raw := r.take(n)
			if r.err != nil {
				return nil, r.err
			}
			col.I = make([]int64, n)
			for j := range col.I {
				col.I[j] = int64(raw[j])
			}
		case TypeString:
			col.S = make([]string, n)
			for j := range col.S {
				col.S[j] = string(r.take(int(r.uvarint())))
			}
		}
		if r.err != nil {
			return nil, fmt.Errorf("%w: %v", errCorruptBlock, r.err)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return c, nil
}
