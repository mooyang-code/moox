package factorudf

// 测试里手工拼 wasm 二进制：用来构造 SDK 写不出来的不合格模块（有导入、有 start、共享内存、错误签名……）。

type funcType struct{ params, results []byte }

const (
	i32 byte = 0x7f
)

type fn struct {
	typ  int
	body []byte // 函数体（不含局部变量声明与 end）
}

type modBuilder struct {
	types    []funcType
	imports  [][2]string // 导入函数（类型 0）
	funcs    []fn
	exports  []exportDef
	memory   []byte // 完整的内存段条目（limits）；nil 表示没有内存
	memories [][]byte
	tables   [][]byte
	start    *int
	customs  [][2][]byte
}

type exportDef struct {
	name string
	kind byte // 0 函数 1 表 2 内存
	idx  int
}

func leb(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			return append(out, b)
		}
	}
}

func vec(items ...[]byte) []byte {
	out := leb(uint64(len(items)))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

func str(s string) []byte { return append(leb(uint64(len(s))), s...) }

func section(id byte, payload []byte) []byte {
	return append(append([]byte{id}, leb(uint64(len(payload)))...), payload...)
}

func (b *modBuilder) build() []byte {
	out := []byte("\x00asm\x01\x00\x00\x00")
	var types [][]byte
	for _, t := range b.types {
		types = append(types, append(append([]byte{0x60}, vec(bytesToItems(t.params)...)...), vec(bytesToItems(t.results)...)...))
	}
	out = append(out, section(1, vec(types...))...)
	if len(b.imports) > 0 {
		var imps [][]byte
		for _, im := range b.imports {
			imps = append(imps, append(append(str(im[0]), str(im[1])...), 0x00, 0x00))
		}
		out = append(out, section(2, vec(imps...))...)
	}
	var funcs [][]byte
	for _, f := range b.funcs {
		funcs = append(funcs, leb(uint64(f.typ)))
	}
	out = append(out, section(3, vec(funcs...))...)
	if len(b.tables) > 0 {
		out = append(out, section(4, vec(b.tables...))...)
	}
	mems := b.memories
	if b.memory != nil {
		mems = append(mems, b.memory)
	}
	if len(mems) > 0 {
		out = append(out, section(5, vec(mems...))...)
	}
	var exps [][]byte
	for _, e := range b.exports {
		exps = append(exps, append(append(str(e.name), e.kind), leb(uint64(e.idx))...))
	}
	out = append(out, section(7, vec(exps...))...)
	if b.start != nil {
		out = append(out, section(8, leb(uint64(*b.start)))...)
	}
	var codes [][]byte
	for _, f := range b.funcs {
		body := append(append([]byte{0x00}, f.body...), 0x0b)
		codes = append(codes, append(leb(uint64(len(body))), body...))
	}
	out = append(out, section(10, vec(codes...))...)
	for _, c := range b.customs {
		out = append(out, section(0, append(str(string(c[0])), c[1]...))...)
	}
	return out
}

func bytesToItems(b []byte) [][]byte {
	out := make([][]byte, len(b))
	for i, x := range b {
		out[i] = []byte{x}
	}
	return out
}

func constI32(v int) []byte { return append([]byte{0x41}, sleb(int64(v))...) }

func sleb(v int64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

const validManifest = `{"abi":1,"sdk":"test","name":"hand","kind":"series","inputs":["x"],"params":[{"name":"n","type":"int","min":1,"max":10}],"lookback":"n","nan":"strict","min_valid":1}`

// validBuilder 是一个最小的、通过全部检查的模块：eval 什么都不做，返回 0。
func validBuilder() *modBuilder {
	return &modBuilder{
		types: []funcType{
			{results: []byte{i32}},                                               // () -> i32
			{params: []byte{i32}, results: []byte{i32}},                          // (i32) -> i32
			{params: []byte{i32, i32, i32, i32, i32, i32}, results: []byte{i32}}, // eval
		},
		funcs: []fn{
			{typ: 0, body: constI32(1)},
			{typ: 1, body: constI32(65536)},
			{typ: 2, body: constI32(0)},
		},
		exports: []exportDef{{"memory", 2, 0}, {"moox_udf_abi", 0, 0}, {"moox_udf_reserve", 0, 1}, {"moox_udf_eval", 0, 2}},
		memory:  []byte{0x00, 0x02}, // 初始 2 页，无最大值
		customs: [][2][]byte{{[]byte(CustomSection), []byte(validManifest)}},
	}
}
