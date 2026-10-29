package factorudf

import "fmt"

// 本文件对各段的内部结构做长度校验：wazero 解码器按声明的个数、长度先分配再读内容（数据段的字节长度、名称长度、
// 局部变量个数、元素个数、名称段的映射大小……），一个几字节的畸形段就能让宿主在实例化之前分配出几 GiB，而这一步不受内存预算保护。
// 这里只做“声明的个数/长度不能超过剩余字节”这类界限检查（以及局部变量总数上限），结构不对一律拒绝。

// 单个函数局部变量总数的上限（wasm 规范建议值），与整个模块局部变量总数的上限：wazero 按展开后的个数分配，
// 每个函数各自不超限、但函数很多时合计仍可能分配出几 GiB。
const (
	maxLocals       = 50000
	maxModuleLocals = 1 << 20
)

// validType 是允许出现的值类型与引用类型：数值、v128、funcref、externref。带堆类型的引用类型（0x63、0x64，后跟变长编码）
// 一律拒绝：wazero 对它们的解码有可变长度的读取，预扫描不能与它保持一致，而 Rust 的产物不会用到。
func validType(b byte) bool {
	switch b {
	case 0x7f, 0x7e, 0x7d, 0x7c, 0x7b, 0x70, 0x6f:
		return true
	}
	return false
}

func (r *reader) valtype() error {
	b, err := r.byte()
	if err != nil {
		return err
	}
	if !validType(b) {
		return fmt.Errorf("不支持的值类型 0x%02x", b)
	}
	return nil
}

func (r *reader) remaining() int { return len(r.b) - r.pos }

// count 读一个向量的元素个数，并要求每个元素至少占一个字节。
func (r *reader) count() (uint64, error) {
	n, err := r.u64()
	if err != nil {
		return 0, err
	}
	if n > uint64(r.remaining()) {
		return 0, fmt.Errorf("声明了 %d 个元素，比剩余的 %d 字节还多", n, r.remaining())
	}
	return n, nil
}

// name 读一个带长度前缀的字节串。
func (r *reader) name() error {
	n, err := r.u64()
	if err != nil {
		return err
	}
	_, err = r.bytes(n)
	return err
}

// skipLEB 跳过一个 LEB128 整数（任意宽度，最多 10 字节）。
func (r *reader) skipLEB() error {
	_, err := r.u64()
	return err
}

// skipConstExpr 跳过一个常量表达式（直到 end）；只认常量、global.get、ref.null、ref.func 与扩展常量表达式的整数运算，其余一律拒绝。
func (r *reader) skipConstExpr() error {
	for {
		op, err := r.byte()
		if err != nil {
			return err
		}
		switch op {
		case 0x0b: // end
			return nil
		case 0x41, 0x42, 0x23, 0xd2: // i32.const、i64.const、global.get、ref.func：后跟一个 LEB128
			err = r.skipLEB()
		case 0x43: // f32.const
			_, err = r.bytes(4)
		case 0x44: // f64.const
			_, err = r.bytes(8)
		case 0xd0: // ref.null：立即数只认 funcref / externref（单字节）；带堆类型的变长编码与 wazero 的读取位置对不上，一律拒绝
			var t byte
			if t, err = r.byte(); err == nil && t != 0x70 && t != 0x6f {
				return fmt.Errorf("ref.null 的类型 0x%02x 不支持", t)
			}
		case 0xfd: // SIMD 前缀：只认 v128.const（0x0c，后跟 16 字节）
			var sub uint64
			if sub, err = r.u64(); err == nil {
				if sub != 0x0c {
					return fmt.Errorf("常量表达式里有不支持的 SIMD 指令 0x%x", sub)
				}
				_, err = r.bytes(16)
			}
		case 0x6a, 0x6b, 0x6c, 0x7c, 0x7d, 0x7e: // i32.add/sub/mul、i64.add/sub/mul
		default:
			return fmt.Errorf("常量表达式里有不支持的指令 0x%02x", op)
		}
		if err != nil {
			return err
		}
	}
}

// validateSection 校验一个段的内部结构，id 为段编号。
func validateSection(id byte, body []byte) error {
	r := &reader{b: body}
	switch id {
	case 0:
		return validateCustom(r)
	case 1:
		return eachEntry(r, func() error {
			form, err := r.byte()
			if err != nil {
				return err
			}
			if form != 0x60 {
				return fmt.Errorf("不支持的类型形式 0x%02x", form)
			}
			for i := 0; i < 2; i++ { // 参数、结果
				n, err := r.count()
				if err != nil {
					return err
				}
				for j := uint64(0); j < n; j++ {
					if err := r.valtype(); err != nil {
						return err
					}
				}
			}
			return nil
		})
	case 2:
		return eachEntry(r, func() error {
			if err := r.name(); err != nil {
				return err
			}
			if err := r.name(); err != nil {
				return err
			}
			kind, err := r.byte()
			if err != nil {
				return err
			}
			switch kind {
			case 0:
				return r.skipLEB()
			case 1:
				if err := r.valtype(); err != nil {
					return err
				}
				_, err := r.limits()
				return err
			case 2:
				_, err := r.limits()
				return err
			case 3:
				if err := r.valtype(); err != nil {
					return err
				}
				_, err := r.byte()
				return err
			}
			return fmt.Errorf("未知的导入种类 %d", kind)
		})
	case 3:
		return eachEntry(r, r.skipLEB)
	case 4:
		return eachEntry(r, func() error {
			if err := r.valtype(); err != nil {
				return err
			}
			_, err := r.limits()
			return err
		})
	case 6:
		return eachEntry(r, func() error {
			if err := r.valtype(); err != nil {
				return err
			}
			if _, err := r.byte(); err != nil { // 可变性
				return err
			}
			return r.skipConstExpr()
		})
	case 7:
		return eachEntry(r, func() error {
			if err := r.name(); err != nil {
				return err
			}
			if _, err := r.byte(); err != nil {
				return err
			}
			return r.skipLEB()
		})
	case 9:
		return eachEntry(r, func() error { return validateElement(r) })
	case 10:
		var moduleLocals uint64
		return eachEntry(r, func() error {
			n, err := validateCode(r)
			moduleLocals += n
			if err == nil && moduleLocals > maxModuleLocals {
				err = fmt.Errorf("全部函数合计声明了超过 %d 个局部变量", maxModuleLocals)
			}
			return err
		})
	case 11:
		return eachEntry(r, func() error { return validateData(r) })
	}
	return nil
}

// eachEntry 读向量的个数，对每个元素调用 f。
func eachEntry(r *reader, f func() error) error {
	n, err := r.count()
	if err != nil {
		return err
	}
	for i := uint64(0); i < n; i++ {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func validateElement(r *reader) error {
	flag, err := r.u64()
	if err != nil {
		return err
	}
	if flag > 7 {
		return fmt.Errorf("未知的元素段标志 %d", flag)
	}
	if flag&0x01 == 0 && flag&0x02 != 0 { // 显式表索引（标志 2、6）
		if err := r.skipLEB(); err != nil {
			return err
		}
	}
	if flag&0x01 == 0 { // 主动段：偏移表达式（标志 0、2、4、6）
		if err := r.skipConstExpr(); err != nil {
			return err
		}
	}
	if flag&0x03 != 0 { // 标志 1、2、3、5、6、7：元素种类（0x00）或引用类型
		kind, err := r.byte()
		if err != nil {
			return err
		}
		if kind != 0x00 && !validType(kind) {
			return fmt.Errorf("不支持的元素类型 0x%02x", kind)
		}
	}
	n, err := r.count()
	if err != nil {
		return err
	}
	for i := uint64(0); i < n; i++ {
		if flag&0x04 == 0 {
			err = r.skipLEB() // 函数索引
		} else {
			err = r.skipConstExpr()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// validateCode 校验一个函数体的局部变量声明，返回它展开后的局部变量个数。
func validateCode(r *reader) (uint64, error) {
	size, err := r.u64()
	if err != nil {
		return 0, err
	}
	body, err := r.bytes(size)
	if err != nil {
		return 0, err
	}
	b := &reader{b: body}
	n, err := b.count()
	if err != nil {
		return 0, err
	}
	var total uint64
	for i := uint64(0); i < n; i++ {
		c, err := b.u64()
		if err != nil {
			return 0, err
		}
		if err := b.valtype(); err != nil {
			return 0, err
		}
		total += c
		if total > maxLocals {
			return 0, fmt.Errorf("函数声明了超过 %d 个局部变量", maxLocals)
		}
	}
	return total, nil
}

func validateData(r *reader) error {
	flag, err := r.u64()
	if err != nil {
		return err
	}
	switch flag {
	case 0:
		err = r.skipConstExpr()
	case 1:
	case 2:
		if err = r.skipLEB(); err == nil {
			err = r.skipConstExpr()
		}
	default:
		return fmt.Errorf("未知的数据段标志 %d", flag)
	}
	if err != nil {
		return err
	}
	return r.name() // 字节长度前缀 + 内容
}

// validateCustom 校验自定义段；“name”段会被 wazero 解析（按声明的个数先分配映射），需要校验它的各级个数。
func validateCustom(r *reader) error {
	nameLen, err := r.u64()
	if err != nil {
		return err
	}
	name, err := r.bytes(nameLen)
	if err != nil {
		return err
	}
	if string(name) != "name" {
		return nil
	}
	for r.remaining() > 0 {
		sub, err := r.byte()
		if err != nil {
			return err
		}
		size, err := r.u64()
		if err != nil {
			return err
		}
		body, err := r.bytes(size)
		if err != nil {
			return err
		}
		s := &reader{b: body}
		switch sub {
		case 0: // 模块名
			err = s.name()
		case 1: // 函数名：vec(索引, 名称)
			err = eachEntry(s, func() error {
				if err := s.skipLEB(); err != nil {
					return err
				}
				return s.name()
			})
		case 2: // 局部变量名：vec(函数索引, vec(局部索引, 名称))
			err = eachEntry(s, func() error {
				if err := s.skipLEB(); err != nil {
					return err
				}
				return eachEntry(s, func() error {
					if err := s.skipLEB(); err != nil {
						return err
					}
					return s.name()
				})
			})
		}
		if err == nil && sub <= 2 && s.remaining() != 0 {
			err = fmt.Errorf("子段 %d 的内容没有被完整消费（多出 %d 字节）", sub, s.remaining())
		}
		if err != nil {
			return fmt.Errorf("name 段：%w", err)
		}
	}
	return nil
}
