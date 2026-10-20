package wirecompress

import (
	"fmt"

	"trpc.group/trpc-go/trpc-go/codec"
)

// frameDecoder 解压 tRPC 帧正文；上限是进程级帧上限。解码不看类型号，由帧头里的字典 ID 选字典。
var frameDecoder = NewDecoder(UseTRPC, MaxFrameBytes)

// trpcCompressor 把一个字典版本注册为 tRPC 压缩类型。压缩器接口没有上下文，
// 空间只能通过类型号传递，所以每个字典版本占一个类型号。
type trpcCompressor struct{ v *version }

func (c trpcCompressor) Compress(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return in, nil
	}
	if _, err := c.v.encoder(); err != nil {
		return nil, fmt.Errorf("字典 %s：%w", c.v.name, err)
	}
	return encodeWith(UseTRPC, c.v, in), nil
}

func (trpcCompressor) Decompress(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return in, nil
	}
	return frameDecoder.Decode(in)
}

func init() {
	for _, v := range global.versions {
		codec.RegisterCompressor(v.compressType, trpcCompressor{v: v})
	}
}
