package events

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/wirecompress"
)

// ContentTypeZstd 标识事件信封经 zstd 字典压缩后的字节。发布器默认压缩；
// 消费端经 DecodeRaw 透明解压，业务代码不感知。字典由帧头里的字典 ID 决定。
const ContentTypeZstd = ContentType + "+zstd"

// maxInflatedBytes 是解压后信封的上限，防止异常数据撑爆内存；正常的行事件远小于它。
const maxInflatedBytes = 64 << 20

// compressMinBytes 是压缩的最小信封字节数：有字典时小信封也划算（1KB 以下实测仍有 2～3 倍）。
const compressMinBytes = 256

var inflater = wirecompress.NewDecoder(wirecompress.UseEvent, maxInflatedBytes)

// compressBody 压缩信封字节；压缩后没有明显变小时返回 false，调用方按原样发布。
func compressBody(body []byte) ([]byte, bool) {
	packed := wirecompress.Encode(wirecompress.UseEvent, body)
	if len(packed) == 0 || len(packed) >= len(body)*9/10 {
		return nil, false
	}
	return packed, true
}

func inflateBody(raw []byte) ([]byte, error) {
	body, err := inflater.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("解压事件信封：%w", err)
	}
	return body, nil
}

// pack 压缩信封。
func (p *Publisher) pack(body []byte) ([]byte, string) {
	if len(body) >= compressMinBytes {
		if compressed, ok := compressBody(body); ok {
			return compressed, ContentTypeZstd
		}
	}
	return body, ContentType
}
