package nativewire

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/wirecompress"
	"trpc.group/trpc-go/trpc-go/codec"
)

// unsupported 拒绝框架内置的压缩算法：框架的解压器无上限展开，且发生在验签之前，
// 未认证的小体积压缩包就能耗尽内存。本系统只使用 wirecompress 注册的 zstd 字典压缩类型，
// 不再有任何调用方需要内置算法，所以压缩与解压两个方向一并拒绝。
type unsupported struct{ name string }

func (c unsupported) Compress(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return in, nil
	}
	return nil, fmt.Errorf("compression %s is not supported", c.name)
}

func (c unsupported) Decompress(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return in, nil
	}
	return nil, fmt.Errorf("compression %s is not supported", c.name)
}

func installBoundedCompressors() {
	// wirecompress 的 init 已注册 zstd 字典压缩类型（32 起）；这里导入它保证每个引用 gatewayauth 的进程都有。
	_ = wirecompress.MaxFrameBytes
	for compressType, name := range map[int]string{
		codec.CompressTypeGzip:         "gzip",
		codec.CompressTypeSnappy:       "snappy",
		codec.CompressTypeZlib:         "zlib",
		codec.CompressTypeStreamSnappy: "stream-snappy",
		codec.CompressTypeBlockSnappy:  "block-snappy",
		codec.CompressTypeStreamLZ4:    "stream-lz4",
		codec.CompressTypeBlockLZ4:     "block-lz4",
	} {
		codec.RegisterCompressor(compressType, unsupported{name: name})
	}
}
