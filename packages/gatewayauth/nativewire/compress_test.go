package nativewire

import (
	"bytes"
	"errors"
	"testing"

	"trpc.group/trpc-go/trpc-go/codec"
)

func TestGzipDecompressIsBoundedByFrameLimit(t *testing.T) {
	compressor := codec.GetCompressor(codec.CompressTypeGzip)
	// 重复内容压缩后极小，展开后超过帧上限：未认证请求不能借此耗尽内存
	huge := bytes.Repeat([]byte{'a'}, MaxFrameBytes+1)
	packed, err := compressor.Compress(huge)
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) > 1<<20 {
		t.Fatalf("测试数据压缩后应当很小: %d", len(packed))
	}
	if _, err := compressor.Decompress(packed); !errors.Is(err, ErrDecompressedTooLarge) {
		t.Fatalf("超限展开应被拒绝: %v", err)
	}
	small := []byte("hello gateway")
	packed, err = compressor.Compress(small)
	if err != nil {
		t.Fatal(err)
	}
	got, err := compressor.Decompress(packed)
	if err != nil || !bytes.Equal(got, small) {
		t.Fatalf("正常数据应能往返: %q %v", got, err)
	}
}

func TestOtherBuiltInCompressionsAreRejectedOnDecompress(t *testing.T) {
	for _, compressType := range []int{
		codec.CompressTypeSnappy, codec.CompressTypeZlib, codec.CompressTypeStreamSnappy,
		codec.CompressTypeBlockSnappy, codec.CompressTypeStreamLZ4, codec.CompressTypeBlockLZ4,
	} {
		compressor := codec.GetCompressor(compressType)
		if compressor == nil {
			continue
		}
		if _, err := compressor.Decompress([]byte{1, 2, 3}); err == nil {
			t.Fatalf("压缩类型 %d 的解压应被拒绝", compressType)
		}
	}
}
