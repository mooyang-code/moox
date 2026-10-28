package factorudf

import (
	"errors"
	"fmt"
)

// wasmInfo 是不执行任何代码、直接从 wasm 二进制里读出的结构信息：模块检查要拒绝的东西（导入、start 函数、共享内存……）
// 都能在这里确定，不依赖运行时对未知特性的反应。
type wasmInfo struct {
	custom      map[string][]byte
	imports     int
	hasStart    bool
	memories    []limits
	tables      []limits
	functions   int
	customCount map[string]int
}

type limits struct {
	min, max uint64
	hasMax   bool
	shared   bool
	memory64 bool
}

type reader struct {
	b   []byte
	pos int
}

var errTruncated = errors.New("wasm 二进制被截断")

func (r *reader) byte() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, errTruncated
	}
	v := r.b[r.pos]
	r.pos++
	return v, nil
}

func (r *reader) u64() (uint64, error) {
	var result uint64
	var shift uint
	for i := 0; i < 10; i++ {
		b, err := r.byte()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
	}
	return 0, errors.New("wasm LEB128 整数过长")
}

func (r *reader) bytes(n uint64) ([]byte, error) {
	if n > uint64(len(r.b)-r.pos) {
		return nil, errTruncated
	}
	out := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return out, nil
}

func (r *reader) limits() (limits, error) {
	flag, err := r.byte()
	if err != nil {
		return limits{}, err
	}
	// 0x01 有最大值；0x02 共享；0x04 64 位内存（memory64 提案）。
	if flag&^0x07 != 0 {
		return limits{}, fmt.Errorf("未知的 limits 标志 0x%02x", flag)
	}
	l := limits{hasMax: flag&0x01 != 0, shared: flag&0x02 != 0, memory64: flag&0x04 != 0}
	if l.min, err = r.u64(); err != nil {
		return limits{}, err
	}
	if l.hasMax {
		if l.max, err = r.u64(); err != nil {
			return limits{}, err
		}
	}
	return l, nil
}

// scanWasm 读取各段：只解析模块检查需要的内容，其余段按长度跳过。
func scanWasm(wasm []byte) (*wasmInfo, error) {
	if len(wasm) < 8 || string(wasm[:4]) != "\x00asm" {
		return nil, errors.New("不是 wasm 二进制（缺少魔数）")
	}
	if wasm[4] != 1 || wasm[5] != 0 || wasm[6] != 0 || wasm[7] != 0 {
		return nil, errors.New("不支持的 wasm 版本")
	}
	info := &wasmInfo{custom: map[string][]byte{}, customCount: map[string]int{}}
	r := &reader{b: wasm, pos: 8}
	for r.pos < len(wasm) {
		id, err := r.byte()
		if err != nil {
			return nil, err
		}
		size, err := r.u64()
		if err != nil {
			return nil, err
		}
		body, err := r.bytes(size)
		if err != nil {
			return nil, err
		}
		sec := &reader{b: body}
		// 向量段（类型、导入、函数、表、内存、全局、导出、元素、代码、数据）开头是元素个数，每个元素至少占一个字节：
		// 个数比段内字节数还大的段一定是畸形的。不在这里拒绝，后面的 wazero 会先按声明的个数分配 map 与切片再检查实际内容，
		// 一个几字节的段就能让宿主分配出几十 GiB。
		switch id {
		case 1, 2, 3, 4, 5, 6, 7, 9, 10, 11:
			probe := &reader{b: body}
			n, err := probe.u64()
			if err != nil {
				return nil, err
			}
			if n > uint64(len(body)-probe.pos) {
				return nil, fmt.Errorf("段 %d 声明了 %d 个元素，比段内的字节数还多", id, n)
			}
		}
		switch id {
		case 0:
			nameLen, err := sec.u64()
			if err != nil {
				return nil, err
			}
			name, err := sec.bytes(nameLen)
			if err != nil {
				return nil, err
			}
			info.customCount[string(name)]++
			if _, dup := info.custom[string(name)]; !dup {
				info.custom[string(name)] = append([]byte(nil), body[sec.pos:]...)
			}
		case 2:
			n, err := sec.u64()
			if err != nil {
				return nil, err
			}
			info.imports += int(n)
		case 3:
			n, err := sec.u64()
			if err != nil {
				return nil, err
			}
			info.functions = int(n)
		case 4:
			n, err := sec.u64()
			if err != nil {
				return nil, err
			}
			for i := uint64(0); i < n; i++ {
				if _, err := sec.byte(); err != nil { // 元素类型
					return nil, err
				}
				l, err := sec.limits()
				if err != nil {
					return nil, err
				}
				info.tables = append(info.tables, l)
			}
		case 5:
			n, err := sec.u64()
			if err != nil {
				return nil, err
			}
			for i := uint64(0); i < n; i++ {
				l, err := sec.limits()
				if err != nil {
					return nil, err
				}
				info.memories = append(info.memories, l)
			}
		case 8:
			info.hasStart = true
		}
	}
	return info, nil
}
