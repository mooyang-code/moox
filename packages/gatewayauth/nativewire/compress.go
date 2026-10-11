package nativewire

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	"trpc.group/trpc-go/trpc-go/codec"
)

// ErrDecompressedTooLarge 表示解压后的数据超过进程级帧上限。
var ErrDecompressedTooLarge = errors.New("decompressed payload exceeds the frame limit")

// boundedGzip 替换框架自带的 gzip 压缩器：框架的 Decompress 用 io.ReadAll 无上限展开，
// 且发生在验签之前，未认证的小体积压缩包就能耗尽内存。压缩方向沿用原实现。
type boundedGzip struct{ original codec.Compressor }

func (c boundedGzip) Compress(in []byte) ([]byte, error) { return c.original.Compress(in) }

func (c boundedGzip) Decompress(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return in, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	out, err := io.ReadAll(io.LimitReader(reader, MaxFrameBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > MaxFrameBytes {
		return nil, ErrDecompressedTooLarge
	}
	return out, nil
}

// unsupportedDecompress 拒绝解压框架内置的其他压缩算法：它们同样无上限展开，
// 而本系统的客户端只使用 gzip。压缩方向沿用原实现。
type unsupportedDecompress struct {
	original codec.Compressor
	name     string
}

func (c unsupportedDecompress) Compress(in []byte) ([]byte, error) {
	if c.original == nil {
		return nil, fmt.Errorf("compression %s is not supported", c.name)
	}
	return c.original.Compress(in)
}

func (c unsupportedDecompress) Decompress(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return in, nil
	}
	return nil, fmt.Errorf("compression %s is not supported", c.name)
}

func installBoundedCompressors() {
	if original := codec.GetCompressor(codec.CompressTypeGzip); original != nil {
		codec.RegisterCompressor(codec.CompressTypeGzip, boundedGzip{original: original})
	}
	for compressType, name := range map[int]string{
		codec.CompressTypeSnappy:       "snappy",
		codec.CompressTypeZlib:         "zlib",
		codec.CompressTypeStreamSnappy: "stream-snappy",
		codec.CompressTypeBlockSnappy:  "block-snappy",
		codec.CompressTypeStreamLZ4:    "stream-lz4",
		codec.CompressTypeBlockLZ4:     "block-lz4",
	} {
		codec.RegisterCompressor(compressType, unsupportedDecompress{original: codec.GetCompressor(compressType), name: name})
	}
}
