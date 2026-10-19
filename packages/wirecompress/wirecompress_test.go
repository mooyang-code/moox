package wirecompress

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

// sample 构造一个有重复结构的 JSON 样本，便于压缩。
func sample(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"dataset_id":"dataset_demo","rows":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"subject_id":"BTC-USDT","i":%d,"close":%d.5}`, i, 1000+i)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	decoder := NewDecoder(UseTRPC, 1<<20)
	for _, size := range []int{1, 2, 100, 1000, 20000} {
		src := sample(size)
		out := Encode(UseTRPC, "", src)
		require.Less(t, len(out), len(src), "size=%d", size)
		got, err := decoder.Decode(out)
		require.NoError(t, err)
		require.Equal(t, src, got)
	}
	one := Encode(UseEvent, "crypto", []byte{7})
	got, err := NewDecoder(UseEvent, 16).Decode(one)
	require.NoError(t, err)
	require.Equal(t, []byte{7}, got)
}

// 超过编码窗口（1MB）的大载荷也要能往返：帧是单段帧，窗口等于原始长度。
func TestLargePayloadRoundTrip(t *testing.T) {
	src := make([]byte, 0, 6<<20)
	for len(src) < 6<<20 {
		src = append(src, sample(2000)...)
	}
	frame := Encode(UseTRPC, "", src)
	require.Less(t, len(frame), len(src)/4)
	got, err := NewDecoder(UseTRPC, 8<<20).Decode(frame)
	require.NoError(t, err)
	require.Equal(t, src, got)
}

func TestEmptyInput(t *testing.T) {
	require.Nil(t, Encode(UseTRPC, "", nil))
	out, err := NewDecoder(UseTRPC, 16).Decode(nil)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestDecodeBoundaries(t *testing.T) {
	src := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64KB
	frame := Encode(UseTRPC, "", src)
	got, err := NewDecoder(UseTRPC, len(src)).Decode(frame)
	require.NoError(t, err, "刚好等于上限")
	require.Equal(t, src, got)
	_, err = NewDecoder(UseTRPC, len(src)-1).Decode(frame)
	require.ErrorIs(t, err, ErrTooLarge, "超过上限一个字节")
}

func TestLimitIsolation(t *testing.T) {
	src := bytes.Repeat([]byte("abcdefgh"), 256*1024) // 2MB
	frame := Encode(UseTRPC, "", src)
	_, err := NewDecoder(UsePlan, 1<<20).Decode(frame)
	require.ErrorIs(t, err, ErrTooLarge)
	got, err := NewDecoder(UseTRPC, 64<<20).Decode(frame)
	require.NoError(t, err)
	require.Len(t, got, len(src))
}

func TestDecompressionBomb(t *testing.T) {
	// 1GB 全零，压缩后只有几十 KB；声明长度远超上限，必须在分配之前被拒绝。
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	zeros := make([]byte, 1<<20)
	frame := enc.EncodeAll(bytes.Repeat(zeros, 1024), nil)
	require.Less(t, len(frame), 1<<20)
	_, err = NewDecoder(UseTRPC, MaxFrameBytes).Decode(frame)
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestFrameWithoutContentSizeIsRejected(t *testing.T) {
	var buf bytes.Buffer
	stream, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = stream.Write(sample(100))
	require.NoError(t, err)
	require.NoError(t, stream.Flush())
	_, err = stream.Write(sample(100))
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	var header zstd.Header
	require.NoError(t, header.Decode(buf.Bytes()))
	require.False(t, header.HasFCS, "流式编码在分次写入时不声明原始长度")
	_, err = NewDecoder(UseTRPC, 1<<20).Decode(buf.Bytes())
	require.ErrorIs(t, err, ErrCorrupt)
}

func TestConcatenatedFramesAreRejectedDuringDecode(t *testing.T) {
	first := bytes.Repeat([]byte("x"), 1<<20)
	second := bytes.Repeat([]byte("y"), 1<<20)
	a := Encode(UseTRPC, "", first)
	b := Encode(UseTRPC, "", second)
	_, err := NewDecoder(UseTRPC, 1<<20).Decode(append(append([]byte{}, a...), b...))
	require.Error(t, err, "第一帧刚好用满上限，后面再拼一帧必须被拒绝")
	require.True(t, errors.Is(err, ErrTooLarge) || errors.Is(err, ErrCorrupt), "got %v", err)
	// 比上限小一点的第一帧，加上第二帧超出声明容量也要被拒绝。
	_, err = NewDecoder(UseTRPC, 2<<20).Decode(append(append([]byte{}, a...), b...))
	require.Error(t, err)
}

func TestCorruptAndTruncatedFrames(t *testing.T) {
	decoder := NewDecoder(UseTRPC, 1<<20)
	frame := Encode(UseTRPC, "", sample(500))
	for _, bad := range [][]byte{
		{0x00, 0x01, 0x02},
		frame[:len(frame)/2],
		frame[:8],
		append([]byte{}, 0x28, 0xb5, 0x2f, 0xfd),
	} {
		require.NotPanics(t, func() {
			_, err := decoder.Decode(bad)
			require.Error(t, err)
		})
	}
	flipped := append([]byte{}, frame...)
	flipped[len(flipped)-1] ^= 0xff
	flipped[len(flipped)/2] ^= 0xff
	require.NotPanics(t, func() { _, _ = decoder.Decode(flipped) })
}

func TestUnknownDictionaryID(t *testing.T) {
	// 用一本不在注册表里的字典编码。
	dict := buildTestDict(t, 0x7F000001)
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderDict(dict))
	require.NoError(t, err)
	frame := enc.EncodeAll(sample(50), nil)
	_, err = NewDecoder(UseTRPC, 1<<20).Decode(frame)
	require.ErrorIs(t, err, ErrUnknownDict)
}

func TestConcurrentEncodeDecode(t *testing.T) {
	decoder := NewDecoder(UseEvent, 1<<20)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := sample(50 + i*10)
			for j := 0; j < 50; j++ {
				got, err := decoder.Decode(Encode(UseEvent, "", src))
				require.NoError(t, err)
				require.Equal(t, src, got)
			}
		}(i)
	}
	wg.Wait()
}

func TestSpaceOf(t *testing.T) {
	require.Equal(t, "", SpaceOf(context.Background(), nil))
	require.Equal(t, "crypto", SpaceOf(context.Background(), spaced{"crypto"}))
	require.Equal(t, "ashare", SpaceOf(WithSpace(context.Background(), "ashare"), spaced{"crypto"}), "context 优先")
	require.Equal(t, "crypto", SpaceOf(WithSpace(context.Background(), ""), spaced{"crypto"}), "空的 context 值不覆盖请求")
	require.Equal(t, "", SpaceOf(context.Background(), struct{}{}))
}

type spaced struct{ space string }

func (s spaced) GetSpaceId() string { return s.space }

func TestTRPCCompressorRegistered(t *testing.T) {
	require.NotNil(t, codec.GetCompressor(32))
	src := sample(300)
	out, err := codec.Compress(32, src)
	require.NoError(t, err)
	require.Less(t, len(out), len(src))
	back, err := codec.Decompress(32, out)
	require.NoError(t, err)
	require.Equal(t, src, back)
	// 空正文由框架直接放行
	empty, err := codec.Compress(32, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestShippedRegistry(t *testing.T) {
	require.Equal(t, 32, CompressType(""))
	require.Equal(t, 32, CompressType("crypto"))
	require.Equal(t, "default-v1", global.fallback.name)
	require.Equal(t, uint32(0x4D580001), global.fallback.id)
	// 默认字典真的被用到了（帧头里带字典 ID）
	var header zstd.Header
	require.NoError(t, header.Decode(Encode(UseTRPC, "", sample(10))))
	require.Equal(t, uint32(0x4D580001), header.DictionaryID)
	require.True(t, header.HasFCS)
}

// --- 注册表校验与空间绑定 ---

func buildTestDict(t *testing.T, id uint32) []byte {
	t.Helper()
	var contents [][]byte
	for i := 0; i < 20; i++ {
		contents = append(contents, sample(20+i))
	}
	history := bytes.Repeat(sample(40), 8)
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{ID: id, Contents: contents, History: history, Offsets: [3]int{1, 4, 8}})
	require.NoError(t, err)
	return dict
}

func fsWith(t *testing.T, files map[string][]byte) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys["dicts/"+name] = &fstest.MapFile{Data: data}
	}
	return fsys
}

func TestRegistryValidation(t *testing.T) {
	d1, d2 := buildTestDict(t, 0x4D580001), buildTestDict(t, 0x4D580002)
	files := fsWith(t, map[string][]byte{"a.zdict": d1, "b.zdict": d2})
	good := `
dictionaries:
  - {name: a-v1, file: a.zdict, id: 0x4D580001, compress_type: 32}
  - {name: b-v1, file: b.zdict, id: 0x4D580002, compress_type: 33}
bindings:
  default: a-v1
  spaces: {ashare: b-v1}
`
	reg, err := parseRegistry(files, []byte(good))
	require.NoError(t, err)
	require.Equal(t, "b-v1", reg.forSpace("ashare").name)
	require.Equal(t, "b-v1", reg.forSpace("  AShare ").name, "空间大小写和空白不敏感")
	require.Equal(t, "a-v1", reg.forSpace("crypto").name)
	require.Equal(t, "a-v1", reg.forSpace("").name)

	cases := map[string]string{
		"重复 ID":     strings.Replace(good, "id: 0x4D580002", "id: 0x4D580001", 1),
		"重复类型号":     strings.Replace(good, "compress_type: 33", "compress_type: 32", 1),
		"重复版本名":     strings.Replace(good, "name: b-v1", "name: a-v1", 1),
		"重复文件":      strings.Replace(good, "file: b.zdict", "file: a.zdict", 1),
		"类型号太小":     strings.Replace(good, "compress_type: 33", "compress_type: 7", 1),
		"类型号太大":     strings.Replace(good, "compress_type: 33", "compress_type: 256", 1),
		"文件 ID 不一致": strings.Replace(good, "id: 0x4D580002", "id: 0x4D580009", 1),
		"默认绑定不存在":   strings.Replace(good, "default: a-v1", "default: nope", 1),
		"空间绑定不存在":   strings.Replace(good, "ashare: b-v1", "ashare: nope", 1),
		"空间名带大写":    strings.Replace(good, "ashare:", "AShare:", 1),
		"未知字段":      good + "extra: 1\n",
		"文件不存在":     strings.Replace(good, "file: b.zdict", "file: missing.zdict", 1),
		"没有任何字典":    "dictionaries: []\nbindings: {default: x}\n",
	}
	for name, raw := range cases {
		_, err := parseRegistry(files, []byte(raw))
		require.Error(t, err, name)
	}
	notDict := fsWith(t, map[string][]byte{"a.zdict": d1, "b.zdict": []byte("not a dictionary at all")})
	_, err = parseRegistry(notDict, []byte(good))
	require.Error(t, err)
	bad := append([]byte{}, d2...)
	binary.LittleEndian.PutUint32(bad[:4], 0x12345678)
	_, err = parseRegistry(fsWith(t, map[string][]byte{"a.zdict": d1, "b.zdict": bad}), []byte(good))
	require.Error(t, err, "魔数不对")
}

func TestSpaceBindingAndVersionEvolution(t *testing.T) {
	d1, d2 := buildTestDict(t, 0x4D580001), buildTestDict(t, 0x4D580002)
	files := fsWith(t, map[string][]byte{"a.zdict": d1, "b.zdict": d2})
	v1Only := `
dictionaries:
  - {name: default-v1, file: a.zdict, id: 0x4D580001, compress_type: 32}
  - {name: default-v2, file: b.zdict, id: 0x4D580002, compress_type: 34}
bindings:
  default: default-v1
  spaces: {}
`
	bound := strings.Replace(v1Only, "default: default-v1", "default: default-v2", 1)
	before, err := parseRegistry(files, []byte(v1Only))
	require.NoError(t, err)
	after, err := parseRegistry(files, []byte(bound))
	require.NoError(t, err)

	src := sample(80)
	// 绑定在 v1 时：v2 的数据（滞后/超前的对端）同样能被解开；绑定切到 v2 后：v1 的数据仍能解开。
	oldFrame := encodeWith(UseTRPC, before.byType[32], src)
	newFrame := encodeWith(UseTRPC, before.byType[34], src)
	for _, reg := range []*registry{before, after} {
		decoder := newDecoder(reg, UseTRPC, 1<<20)
		for _, frame := range [][]byte{oldFrame, newFrame} {
			got, err := decoder.Decode(frame)
			require.NoError(t, err)
			require.Equal(t, src, got)
		}
	}
	// 帧头里的字典 ID 对应各自的版本
	var h zstd.Header
	require.NoError(t, h.Decode(oldFrame))
	require.Equal(t, uint32(0x4D580001), h.DictionaryID)
	require.NoError(t, h.Decode(newFrame))
	require.Equal(t, uint32(0x4D580002), h.DictionaryID)
	require.Equal(t, 32, before.forSpace("x").compressType)
	require.Equal(t, 34, after.forSpace("x").compressType)
}
