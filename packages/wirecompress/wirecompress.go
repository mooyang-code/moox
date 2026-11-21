// Package wirecompress 是系统内部传输与编码统一使用的压缩：zstd 加训练字典。
//
// 编码用注册表里 default 指向的字典版本。字典版本不可变，解码只看 zstd 帧头里的字典 ID。
package wirecompress

import (
	"errors"
	"fmt"
	"sync"

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

	// maxWindow 是编码窗口。
	maxWindow = 1 << 20

	// maxBlock 是 zstd 块的最大输出（128KB）。
	maxBlock = 128 << 10
)

var (
	// ErrTooLarge 表示压缩数据声明或展开后的长度超过解码器的上限。
	ErrTooLarge = errors.New("decompressed payload exceeds the limit")
	// ErrUnknownDict 表示帧头里的字典 ID 不在注册表里。
	ErrUnknownDict = errors.New("unknown dictionary id")
	// ErrCorrupt 表示数据不是本包编码器会产生的 zstd 帧（含没有声明原始长度的帧、多帧拼接等）。
	ErrCorrupt = errors.New("corrupt zstd frame")
)

// CompressType 返回当前字典版本对应的 tRPC 压缩类型号。
func CompressType() int { return global.current.compressType }

// Known 报告压缩类型号是否是本包注册的字典版本。
func Known(compressType int) bool {
	_, ok := global.byType[compressType]
	return ok
}

// Encode 用当前字典版本压缩；空输入返回 nil。
func Encode(use Use, src []byte) []byte {
	return encodeWith(use, global.current, src)
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

// inflightBytes 是全进程同时在解压的输出缓冲总量上限，所有解码器共用：放得下两个最大帧。
// 解压发生在验签之前（tRPC 先解压请求体再进过滤器），几十字节的合法帧就能解出几十 MB，
// 不加总量限制时，并发的未认证请求能让网关同时占住许多份最大缓冲。
const inflightBytes = 2 * (MaxFrameBytes + maxBlock + 256)

var inflight = newBudget(inflightBytes)

// budget 是按字节计的额度：用满时后来的解码排队等待，解码只要毫秒级，不直接拒绝，免得合法大请求在并发时失败。
// 不保证先来先得：小解码持续涌入时大解码可能一直排不上（已知差别，见 docs/模块/主机网关.md）。
type budget struct {
	mu    sync.Mutex
	cond  sync.Cond
	used  int
	limit int
}

func newBudget(limit int) *budget {
	b := &budget{limit: limit}
	b.cond.L = &b.mu
	return b
}

// acquire 取 n 字节额度，不够时等待；超过总额的请求按总额计，保证总能取到。
func (b *budget) acquire(n int) int {
	n = min(n, b.limit)
	b.mu.Lock()
	for b.used+n > b.limit {
		b.cond.Wait()
	}
	b.used += n
	b.mu.Unlock()
	return n
}

func (b *budget) release(n int) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
	b.cond.Broadcast()
}

// Decoder 是固定解压上限的解码器：按帧头里的字典 ID 自动选字典。
// 每个使用点按自己的上限在初始化时构造一个，进程内复用，可并发调用。
type Decoder struct {
	use    Use
	limit  int
	reg    *registry
	dec    *zstd.Decoder
	budget *budget
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
	return &Decoder{use: use, limit: limit, reg: reg, dec: dec, budget: inflight}
}

// Decode 解压一个 zstd 帧。必须是声明了原始长度、且不超过上限的单帧；
// 先按声明长度分配缓冲，再解压，所以单次调用最多分配 limit 字节；分配之前先取全进程的解压额度，解完即还。
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
	if err := checkFrameShape(src, &header); err != nil {
		return nil, d.fail("corrupt", err)
	}
	dictName := "none"
	if header.DictionaryID != 0 {
		v, ok := d.reg.byID[header.DictionaryID]
		if !ok {
			return nil, d.fail("unknown_dict", fmt.Errorf("%w: %#x", ErrUnknownDict, header.DictionaryID))
		}
		dictName = v.name
	}
	// 容量 = 声明长度 + 一个块（128KB）的余量。库在每个块解完之后检查累计输出是否超过声明长度（超过即报错），
	// 一个块最多输出 128KB，所以输出缓冲永远不会越过这个容量、也就不会触发 append 扩容（扩容会一次多分配约 25%，
	// 并在新旧缓冲同时存在时翻倍占用）；压缩块里的序列执行另受剩余容量限制。声明长度为 0 的帧也有非零容量。
	size := int(header.FrameContentSize) + maxBlock + 256
	held := d.budget.acquire(size)
	out, err := d.dec.DecodeAll(src, make([]byte, 0, size))
	d.budget.release(held)
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

// checkFrameShape 在分配和解压之前走一遍块头，确认输入恰好是一个完整的 zstd 帧，且原始块和 RLE 块的输出不超过声明长度。
//
// 库的 WithDecodeAllCapLimit 只在压缩块里按剩余容量限制输出；原始块与 RLE 块是直接追加到输出缓冲的，
// 追加越过容量时先扩容、之后才报错，一个几 KB 的输入就能让缓冲在拒绝之前翻倍。块头只有 3 字节、
// 块的输出大小能直接读出（原始块和 RLE 块），所以这里先把形状查清，库只会看到不会越界的单帧。
// 每个压缩块最多输出一个块（128KB），块数不够填满声明长度的帧也在这里拒绝，不按声明长度分配。
func checkFrameShape(src []byte, header *zstd.Header) error {
	pos := header.HeaderSize
	var literal uint64    // 原始块与 RLE 块的输出字节数
	var compressed uint64 // 压缩块个数
	for {
		if pos+3 > len(src) {
			return fmt.Errorf("%w: 块头被截断", ErrCorrupt)
		}
		h := uint32(src[pos]) | uint32(src[pos+1])<<8 | uint32(src[pos+2])<<16
		last, kind, size := h&1 == 1, (h>>1)&3, int(h>>3)
		pos += 3
		switch kind {
		case 0: // 原始块
			literal += uint64(size)
			pos += size
		case 1: // RLE 块：一个字节重复 size 次
			literal += uint64(size)
			pos++
		case 2: // 压缩块
			compressed++
			pos += size
		default:
			return fmt.Errorf("%w: 保留的块类型", ErrCorrupt)
		}
		if size > maxBlock || pos > len(src) {
			return fmt.Errorf("%w: 块超出边界", ErrCorrupt)
		}
		if literal > header.FrameContentSize {
			return fmt.Errorf("%w: 块输出超过声明的原始长度", ErrCorrupt)
		}
		if last {
			break
		}
	}
	if header.HasCheckSum {
		pos += 4
	}
	if pos != len(src) {
		return fmt.Errorf("%w: 帧之后还有数据（拼接帧）", ErrCorrupt)
	}
	if literal+compressed*maxBlock < header.FrameContentSize {
		return fmt.Errorf("%w: 块数不足以输出声明的原始长度", ErrCorrupt)
	}
	return nil
}
