package listener

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"

	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
	trpcpb "trpc.group/trpc/trpc-protocol/pb/go/trpc"
)

const (
	frameHeadLen = 16
	// initialFrameBuffer 是读包体时的起始缓冲；之后按实际收到的字节翻倍增长，到帧头声明的长度为止。
	initialFrameBuffer = 64 << 10
)

// withGrowingFramer 换掉 tRPC 默认的帧读取器：默认实现读完 16 字节帧头就按声明的长度（最大 10MB）一次分配，
// 对端只发帧头不发包体，每个连接就白占 10MB；这里只按实际到达的字节分配，输出的帧与默认实现逐字节相同。
// 必须排在 server.WithProtocol("trpc") 之后：两者都设置帧读取器，后设置的生效。
func withGrowingFramer() server.Option {
	return func(o *server.Options) {
		o.ServeOptions = append(o.ServeOptions, transport.WithServerFramerBuilder(growingFramerBuilder{}))
	}
}

type growingFramerBuilder struct{}

func (growingFramerBuilder) New(reader io.Reader) codec.Framer { return &growingFramer{reader: reader} }

type growingFramer struct {
	reader io.Reader
	header [frameHeadLen]byte
}

// ReadFrame 的校验与默认实现相同：魔数、总长不小于帧头、不超过 trpc.DefaultMaxFrameSize。
func (f *growingFramer) ReadFrame() ([]byte, error) {
	if _, err := io.ReadFull(f.reader, f.header[:]); err != nil {
		return nil, err
	}
	if magic := binary.BigEndian.Uint16(f.header[:2]); magic != uint16(trpcpb.TrpcMagic_TRPC_MAGIC_VALUE) {
		return nil, fmt.Errorf("trpc framer: read framer head magic %d != %d, not match", magic, uint16(trpcpb.TrpcMagic_TRPC_MAGIC_VALUE))
	}
	total := int(binary.BigEndian.Uint32(f.header[4:8]))
	if total < frameHeadLen {
		return nil, fmt.Errorf("trpc framer: read frame header total len %d < %d, invalid", total, frameHeadLen)
	}
	if total > trpc.DefaultMaxFrameSize {
		return nil, fmt.Errorf("trpc framer: read frame header total len %d > %d, too large", total, trpc.DefaultMaxFrameSize)
	}
	msg := make([]byte, frameHeadLen, min(total, initialFrameBuffer))
	copy(msg, f.header[:])
	for len(msg) < total {
		if len(msg) == cap(msg) {
			msg = slices.Grow(msg, min(len(msg), total-len(msg)))
		}
		n, err := io.ReadFull(f.reader, msg[len(msg):min(cap(msg), total)])
		msg = msg[:len(msg)+n]
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
	return msg, nil
}

// IsSafe 表示每次返回新分配的切片，传输层不必再复制。
func (f *growingFramer) IsSafe() bool { return true }
