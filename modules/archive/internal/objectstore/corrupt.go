package objectstore

import (
	"io"
	"net/http"
)

// CorruptPutBody 返回一个在 PUT 请求体里翻转第一个字节的 RoundTripper：objectstore-check 用它确认
// 服务端会按 Content-MD5 拒绝途中被改的内容。只用于冒烟。
func CorruptPutBody(base http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut && r.Body != nil {
			clone := r.Clone(r.Context())
			clone.Body = &flipFirstByte{ReadCloser: r.Body}
			clone.GetBody = nil
			return base.RoundTrip(clone)
		}
		return base.RoundTrip(r)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type flipFirstByte struct {
	io.ReadCloser
	done bool
}

func (f *flipFirstByte) Read(p []byte) (int, error) {
	n, err := f.ReadCloser.Read(p)
	if n > 0 && !f.done {
		p[0] ^= 0xff
		f.done = true
	}
	return n, err
}
