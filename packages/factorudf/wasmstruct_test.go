package factorudf

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var wasmHeader = []byte("\x00asm\x01\x00\x00\x00")

func forged(id byte, payload ...byte) []byte {
	return append(append([]byte{}, wasmHeader...), section(id, payload)...)
}

// wazero 解码数据段、代码段、名称段时先按声明的长度或个数分配：这些畸形结构必须在静态扫描里就被拒绝。
func TestScanRejectsDeclaredSizesLargerThanTheirSection(t *testing.T) {
	huge := []byte{0xff, 0xff, 0xff, 0xff, 0x0f} // 4294967295
	cases := map[string][]byte{
		"数据段字节长度":  forged(11, append([]byte{1, 1}, huge...)...),                                           // 1 个被动段，长度 4GiB
		"数据段偏移表达式": forged(11, 1, 0, 0x41, 0x00),                                                           // 主动段的偏移没有 end
		"代码段局部变量":  forged(10, append(append([]byte{1, 9, 1}, huge...), 0x7f)...),                          // 1 个局部变量组，个数 4G
		"局部变量总数":   forged(10, append([]byte{1, 8, 2, 0xa0, 0x9c, 0x01, 0x7f, 0xa0, 0x9c, 0x01}, 0x7f)...), // 两组各 20000 以上
		"导出名长度":    forged(7, append([]byte{1}, huge...)...),
		"元素段向量":    forged(9, append([]byte{1, 1, 0}, huge...)...), // 标志 1：元素种类 0，向量个数 4G
		"名称段映射个数":  forged(0, append(append(append([]byte{4}, "name"...), 1, 6, 1), huge...)...),
	}
	// 变长引用类型（堆类型）与未完整消费的 name 子段：预扫描与 wazero 的解码位置对不上，一律拒绝。
	cases["类型段的引用类型"] = forged(1, 1, 0x60, 1, 0x63, 0, 0xff, 0xff, 0xff, 0xff, 0x0f)
	cases["局部变量的引用类型"] = forged(10, 1, 0x0b, 2, 0, 0x63, 0, 0xff, 0xff, 0xff, 0xff, 0x0f, 0x7f, 0x0b)
	cases["name 子段没有消费完"] = forged(0, append([]byte{4, 'n', 'a', 'm', 'e'}, 0, 8, 0, 1, 1, 0xff, 0xff, 0xff, 0xff, 0x0f, 3, 0)...)
	// 每个函数各自不超过单函数上限，合计超过模块上限。
	var body []byte
	body = append(body, leb(1)...)
	body = append(body, leb(maxLocals)...)
	body = append(body, 0x7f)
	entry := append(leb(uint64(len(body))), body...)
	funcs := []byte{}
	funcs = append(funcs, leb(uint64(maxModuleLocals/maxLocals+2))...)
	for i := 0; i < maxModuleLocals/maxLocals+2; i++ {
		funcs = append(funcs, entry...)
	}
	cases["模块局部变量合计"] = forged(10, funcs...)
	for name, wasm := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := scanWasm(wasm)
			require.Error(t, err)
		})
	}
}

func TestScanAcceptsWellFormedSections(t *testing.T) {
	// 带偏移表达式的主动数据段、扩展常量表达式、函数名与局部名的名称段。
	data := forged(11, 1, 0, 0x41, 0x10, 0x0b, 2, 'a', 'b')
	_, err := scanWasm(data)
	require.NoError(t, err)
	names := forged(0, append([]byte{4, 'n', 'a', 'm', 'e'}, 1, 5, 1, 0, 2, 'f', 'n')...)
	_, err = scanWasm(names)
	require.NoError(t, err)
	elem := forged(9, 1, 0, 0x41, 0x00, 0x0b, 2, 0, 1) // 标志 0：偏移、2 个函数索引
	_, err = scanWasm(elem)
	require.NoError(t, err)
	simd := forged(6, append([]byte{1, 0x7b, 0, 0xfd, 0x0c}, append(make([]byte, 16), 0x0b)...)...) // v128 全局，v128.const 初始化
	_, err = scanWasm(simd)
	require.NoError(t, err)
}

// 记账量取底层数组的容量：增长一页时预留的额外容量也算进预算，预算不会少计。
func TestAccountedMemoryChargesCapacityNotLength(t *testing.T) {
	budget := &countingBudget{limit: 1 << 40}
	m := newMeter(budget)
	mem := &accountedMemory{m: m}
	first := mem.Reallocate(16 << 20)
	require.Len(t, first, 16<<20)
	require.Equal(t, int64(16<<20), m.Used())
	grown := mem.Reallocate(16<<20 + 65536)
	require.Len(t, grown, 16<<20+65536)
	require.Equal(t, int64(cap(grown)), m.Used(), "已申请量等于底层数组的容量")
	require.Greater(t, m.Used(), int64(16<<20+65536)-1)
	// 预算刚好够长度、不够容量：增长被拒绝（搬迁期间新旧数组同时存活，按两者之和申请峰值）。
	tight := &countingBudget{limit: 16<<20 + 65536}
	m2 := newMeter(tight)
	mem2 := &accountedMemory{m: m2}
	require.NotNil(t, mem2.Reallocate(16<<20))
	require.Nil(t, mem2.Reallocate(16<<20+65536))
	mem.Free()
	require.Zero(t, m.Used())
}
