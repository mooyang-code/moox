package events

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// ContentTypeGzip 标识事件信封经 gzip 压缩后的字节。压缩只发生在发布端显式开启之后；
// 消费端经 DecodeRaw 透明解压，业务代码不感知。
const ContentTypeGzip = ContentType + "+gzip"

// maxInflatedBytes 是解压后信封的上限，防止异常数据撑爆内存；正常的行事件远小于它。
const maxInflatedBytes = 64 << 20

// compressBody 压缩信封字节；压缩后没有明显变小时返回 false，调用方按原样发布。
func compressBody(body []byte) ([]byte, bool) {
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestSpeed)
	if err != nil {
		return nil, false
	}
	if _, err := writer.Write(body); err != nil || writer.Close() != nil {
		return nil, false
	}
	if buffer.Len() >= len(body)*9/10 {
		return nil, false
	}
	return buffer.Bytes(), true
}

func inflateBody(raw []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("解压事件信封：%w", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxInflatedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("解压事件信封：%w", err)
	}
	if len(body) > maxInflatedBytes {
		return nil, fmt.Errorf("解压后的事件信封超过 %d 字节", maxInflatedBytes)
	}
	return body, nil
}

// SetCompression 让发布器对不小于 minBytes 的信封启用 gzip；0 关闭（默认）。
// 只在所有消费端都已升级到支持 ContentTypeGzip 之后开启。
func (p *Publisher) SetCompression(minBytes int) {
	if p != nil {
		p.compressMin = minBytes
	}
}

func (p *Publisher) pack(body []byte) ([]byte, string) {
	if p.compressMin > 0 && len(body) >= p.compressMin {
		if compressed, ok := compressBody(body); ok {
			return compressed, ContentTypeGzip
		}
	}
	return body, ContentType
}
