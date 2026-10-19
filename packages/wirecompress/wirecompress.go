// Package wirecompress 是系统内部传输与编码统一使用的压缩：zstd 加训练字典。
//
// 字典按空间绑定：空间有专用字典就用专用字典，否则用默认字典。字典版本不可变，
// 解码只看 zstd 帧头里的字典 ID，不需要知道空间。
package wirecompress

import (
	"context"
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// Use 标识压缩的使用点，只用于指标。
type Use string

const (
	UseTRPC  Use = "trpc"
	UseEvent Use = "event"
	UsePlan  Use = "plan"
)

const (
	// MaxFrameBytes 是进程级 tRPC 帧上限，也是帧内解压后正文的上限。
	MaxFrameBytes = 64 << 20

	// maxWindow 是编码窗口，也是解码允许的最大窗口。
	maxWindow = 1 << 20
)

var (
	// ErrTooLarge 表示压缩数据声明或展开后的长度超过解码器的上限。
	ErrTooLarge = errors.New("decompressed payload exceeds the limit")
	// ErrUnknownDict 表示帧头里的字典 ID 不在注册表里。
	ErrUnknownDict = errors.New("unknown dictionary id")
	// ErrCorrupt 表示数据不是本包编码器会产生的 zstd 帧（含没有声明原始长度的帧、多帧拼接等）。
	ErrCorrupt = errors.New("corrupt zstd frame")
)

type spaceKey struct{}

// WithSpace 把空间写进 context，供请求本身不带空间字段的调用指定字典。
func WithSpace(ctx context.Context, space string) context.Context {
	return context.WithValue(ctx, spaceKey{}, space)
}

// SpaceOf 返回调用应使用的空间：context 里显式指定的优先，其次是请求对象顶层的 GetSpaceId()，都没有返回空串。
func SpaceOf(ctx context.Context, request any) string {
	if space, _ := ctx.Value(spaceKey{}).(string); space != "" {
		return space
	}
	if carrier, ok := request.(interface{ GetSpaceId() string }); ok {
		return carrier.GetSpaceId()
	}
	return ""
}

// CompressType 返回空间当前绑定的字典版本对应的 tRPC 压缩类型号。
func CompressType(space string) int { return global.forSpace(space).compressType }

// Known 报告压缩类型号是否是本包注册的字典版本。
func Known(compressType int) bool {
	_, ok := global.byType[compressType]
	return ok
}

// Encode 用空间绑定的字典压缩；空输入返回 nil。
func Encode(use Use, space string, src []byte) []byte {
	return encodeWith(use, global.forSpace(space), src)
}

func encodeWith(use Use, v *version, src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	enc, err := v.encoder()
	if err != nil {
		// 字典在加载时已校验过；这里只会因 zstd 选项非法失败，属于编程错误。
		panic(fmt.Sprintf("wirecompress: 创建字典 %s 的编码器：%v", v.name, err))
	}
	out := enc.EncodeAll(src, make([]byte, 0, len(src)/2+64))
	recordEncode(use, v.name, len(src), len(out))
	return out
}

// windowLimit 是解码允许的最大窗口。单段帧的窗口等于声明的原始长度，所以窗口上限不能小于解压上限；zstd 要求至少 1KB。
func windowLimit(limit int) int { return max(limit, 1024) }

// Decoder 是固定解压上限的解码器：按帧头里的字典 ID 自动选字典。
// 每个使用点按自己的上限在初始化时构造一个，进程内复用，可并发调用。
type Decoder struct {
	use   Use
	limit int
	reg   *registry
	dec   *zstd.Decoder
}

// NewDecoder 创建解压上限为 limit 字节的解码器。
func NewDecoder(use Use, limit int) *Decoder { return newDecoder(global, use, limit) }

func newDecoder(reg *registry, use Use, limit int) *Decoder {
	if limit <= 0 {
		panic("wirecompress: 解压上限必须大于 0")
	}
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderDicts(reg.dicts()...),
		// 单段帧的窗口至少 1KB，库要求最大解码内存不小于窗口；真正的上限由 Decode 里的声明长度检查和输出缓冲容量保证。
		zstd.WithDecoderMaxMemory(uint64(max(limit, 1024))),
		zstd.WithDecoderMaxWindow(uint64(windowLimit(limit))),
		zstd.WithDecoderConcurrency(concurrency()),
		// 整个 DecodeAll 的累计输出不能超过传入缓冲的容量：声明长度之后再拼接的帧会在解码过程中被拒绝。
		zstd.WithDecodeAllCapLimit(true),
	)
	if err != nil {
		panic(fmt.Sprintf("wirecompress: 创建解码器：%v", err))
	}
	return &Decoder{use: use, limit: limit, reg: reg, dec: dec}
}

// Decode 解压一个 zstd 帧。必须是声明了原始长度、且不超过上限的单帧；
// 先按声明长度分配缓冲，再解压，所以单次调用最多分配 limit 字节。
func (d *Decoder) Decode(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, nil
	}
	var header zstd.Header
	if err := header.Decode(src); err != nil || header.Skippable || !header.HasFCS {
		return nil, d.fail("corrupt", fmt.Errorf("%w: 帧头无效或没有声明原始长度", ErrCorrupt))
	}
	if header.FrameContentSize > uint64(d.limit) {
		return nil, d.fail("too_large", fmt.Errorf("%w: 声明 %d 字节，上限 %d", ErrTooLarge, header.FrameContentSize, d.limit))
	}
	if !header.SingleSegment && header.WindowSize > uint64(windowLimit(d.limit)) {
		return nil, d.fail("too_large", fmt.Errorf("%w: 窗口 %d 超过上限", ErrTooLarge, header.WindowSize))
	}
	dictName := "none"
	if header.DictionaryID != 0 {
		v, ok := d.reg.byID[header.DictionaryID]
		if !ok {
			return nil, d.fail("unknown_dict", fmt.Errorf("%w: %#x", ErrUnknownDict, header.DictionaryID))
		}
		dictName = v.name
	}
	// 容量至少 1：声明长度 0 的帧也要有一个非零容量的缓冲，累计上限才有意义。
	buffer := make([]byte, 0, max(int(header.FrameContentSize), 1))
	out, err := d.dec.DecodeAll(src, buffer)
	if err != nil {
		reason := "corrupt"
		switch {
		case errors.Is(err, zstd.ErrDecoderSizeExceeded):
			reason, err = "too_large", fmt.Errorf("%w: %v", ErrTooLarge, err)
		case errors.Is(err, zstd.ErrUnknownDictionary):
			reason, err = "unknown_dict", fmt.Errorf("%w: %v", ErrUnknownDict, err)
		default:
			err = fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		return nil, d.fail(reason, err)
	}
	if uint64(len(out)) != header.FrameContentSize {
		return nil, d.fail("corrupt", fmt.Errorf("%w: 解压长度 %d 与声明的 %d 不一致", ErrCorrupt, len(out), header.FrameContentSize))
	}
	recordDecode(d.use, dictName, len(src), len(out))
	return out, nil
}

func (d *Decoder) fail(reason string, err error) error {
	recordError(d.use, "decode", reason)
	return err
}
