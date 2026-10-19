package nativewire

import (
	"bytes"
	"testing"

	"github.com/mooyang-code/moox/packages/wirecompress"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestBuiltInCompressionsAreRejected(t *testing.T) {
	for _, compressType := range []int{
		codec.CompressTypeGzip, codec.CompressTypeSnappy, codec.CompressTypeZlib, codec.CompressTypeStreamSnappy,
		codec.CompressTypeBlockSnappy, codec.CompressTypeStreamLZ4, codec.CompressTypeBlockLZ4,
	} {
		if _, err := codec.Decompress(compressType, []byte{1, 2, 3}); err == nil {
			t.Fatalf("压缩类型 %d 的解压应被拒绝", compressType)
		}
		if _, err := codec.Compress(compressType, []byte{1, 2, 3}); err == nil {
			t.Fatalf("压缩类型 %d 的压缩应被拒绝", compressType)
		}
	}
}

func TestZstdDictionaryCompressionIsRegistered(t *testing.T) {
	body := bytes.Repeat([]byte(`{"subject_id":"BTC-USDT","close":1000.5},`), 200)
	packed, err := codec.Compress(wirecompress.CompressType(""), body)
	if err != nil || len(packed) >= len(body) {
		t.Fatalf("zstd 字典压缩应已注册并有效: %d -> %d, %v", len(body), len(packed), err)
	}
	got, err := codec.Decompress(wirecompress.CompressType(""), packed)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("正常数据应能往返: %v", err)
	}
}

func TestZstdDecompressIsBoundedByFrameLimit(t *testing.T) {
	// 声明长度超过帧上限的帧，必须在分配之前被拒绝（未认证请求不能借此耗尽内存）。
	var frame []byte
	frame = append(frame, 0x28, 0xb5, 0x2f, 0xfd)    // 魔数
	frame = append(frame, 0x63)                      // 单段、8 字节原始长度
	frame = append(frame, 0, 0, 0, 0, 0, 0, 0, 0x10) // 声明约 1EB
	frame = append(frame, 0x01, 0x00, 0x00)
	if _, err := codec.Decompress(wirecompress.CompressType(""), frame); err == nil {
		t.Fatal("声明长度超限的帧应被拒绝")
	}
}
