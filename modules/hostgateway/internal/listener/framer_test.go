package listener

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	trpc "trpc.group/trpc-go/trpc-go"
)

// frame 拼一个总长为 total 的 tRPC 帧（帧头之外的字节随意，读取器只看帧头）。
func frame(total int, rng *rand.Rand) []byte {
	buf := make([]byte, total)
	for i := range buf {
		buf[i] = byte(rng.IntN(256))
	}
	binary.BigEndian.PutUint16(buf[0:2], 0x930)
	buf[2], buf[3] = 0, 0 // 一元调用帧
	binary.BigEndian.PutUint32(buf[4:8], uint32(total))
	return buf
}

// 逐字节与 tRPC 默认读取器一致：帧头、跨越起始缓冲与多次翻倍的包体、连续多帧。
func TestGrowingFramerMatchesDefault(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var stream bytes.Buffer
	sizes := []int{frameHeadLen, frameHeadLen + 1, initialFrameBuffer - 1, initialFrameBuffer, initialFrameBuffer + 1, 3*initialFrameBuffer + 7, trpc.DefaultMaxFrameSize}
	for _, size := range sizes {
		stream.Write(frame(size, rng))
	}
	data := stream.Bytes()
	ours := growingFramerBuilder{}.New(bytes.NewReader(data))
	theirs := trpc.DefaultFramerBuilder.New(bytes.NewReader(data))
	for _, size := range sizes {
		got, err := ours.ReadFrame()
		require.NoError(t, err, size)
		want, err := theirs.ReadFrame()
		require.NoError(t, err, size)
		require.Len(t, got, size)
		require.Equal(t, want, got, size)
	}
	_, err := ours.ReadFrame()
	require.ErrorIs(t, err, io.EOF)
}

func TestGrowingFramerRejectsBadFrames(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	truncated := frame(initialFrameBuffer*2, rng)[:initialFrameBuffer+100]
	_, err := growingFramerBuilder{}.New(bytes.NewReader(truncated)).ReadFrame()
	require.ErrorIs(t, err, io.ErrUnexpectedEOF, "包体没收全")

	header := frame(frameHeadLen, rng)
	binary.BigEndian.PutUint32(header[4:8], uint32(trpc.DefaultMaxFrameSize+1))
	_, err = growingFramerBuilder{}.New(bytes.NewReader(header)).ReadFrame()
	require.ErrorContains(t, err, "too large")

	binary.BigEndian.PutUint32(header[4:8], frameHeadLen-1)
	_, err = growingFramerBuilder{}.New(bytes.NewReader(header)).ReadFrame()
	require.ErrorContains(t, err, "invalid")

	stream := frame(frameHeadLen+8, rng)
	stream[2] = 1 // 流式帧
	_, err = growingFramerBuilder{}.New(bytes.NewReader(stream)).ReadFrame()
	require.ErrorContains(t, err, "not unary", "网关没有流式调用，流式帧在读帧时就拒绝")

	binary.BigEndian.PutUint16(header[0:2], 0x1234)
	_, err = growingFramerBuilder{}.New(bytes.NewReader(header)).ReadFrame()
	require.ErrorContains(t, err, "magic")
}

// 只发声明 10MB 的帧头、不发包体的连接不再各占 10MB（默认读取器下 30 个连接常驻约 300MB）。
func TestHeaderOnlyConnectionsDoNotPreallocate(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := TRPC(ln, "trpc.moox.hostgateway.FramerTest")
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	time.Sleep(200 * time.Millisecond)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	head := frame(frameHeadLen, rand.New(rand.NewPCG(5, 6)))
	binary.BigEndian.PutUint32(head[4:8], uint32(trpc.DefaultMaxFrameSize))
	var conns []net.Conn
	for range 30 {
		conn, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		conns = append(conns, conn)
		_, err = conn.Write(head)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	time.Sleep(time.Second)
	runtime.GC()
	runtime.ReadMemStats(&after)
	grown := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("30 个只发帧头的连接：堆增加 %.1f MB", float64(grown)/1024/1024)
	require.Less(t, grown, int64(50<<20))
}
