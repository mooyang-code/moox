// Package fakes3 是测试用的最小 S3 服务：只支持单次 PUT、HEAD、GET，记录每个请求供断言请求头。
// 不用第三方实现：通用实现不解析 aws-chunked，“请求里不该出现 aws-chunked”这类断言会测成假服务器的限制。
package fakes3

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Object 是存进假服务器的对象。
type Object struct {
	Body         []byte
	ContentType  string
	StorageClass string
	Meta         map[string]string // 去掉 x-amz-meta- 前缀、转小写的键
}

// Request 是收到的一个请求。
type Request struct {
	Method   string
	Host     string
	Path     string
	RawQuery string
	Header   http.Header
}

// Server 是假 S3。Hook 可以按请求注入错误（返回 handled=true 时直接以 status、code 回应）；
// MutateGet 可以改写 GET 返回的内容（模拟远端内容损坏）。
type Server struct {
	t      testing.TB
	bucket string

	mu        sync.Mutex
	objects   map[string]*Object
	requests  []Request
	Hook      func(r *http.Request, key string) (status int, code string, handled bool)
	MutateGet func(key string, body []byte) []byte

	http *httptest.Server
}

// New 启动假服务器；useTLS 为真时用 HTTPS。测试结束自动关闭。
func New(t testing.TB, bucket string, useTLS bool) *Server {
	s := &Server{t: t, bucket: bucket, objects: map[string]*Object{}}
	if useTLS {
		s.http = httptest.NewTLSServer(s)
	} else {
		s.http = httptest.NewServer(s)
	}
	t.Cleanup(s.http.Close)
	return s
}

// Addr 返回监听地址 host:port。
func (s *Server) Addr() string { return s.http.Listener.Addr().String() }

// Transport 返回把所有连接都拨到本服务器的 Transport（虚拟主机风格的域名不需要能解析；HTTPS 不校验证书）。
func (s *Server) Transport() *http.Transport {
	addr := s.Addr()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 只连测试服务器
	}
}

// Requests 返回收到的请求副本。
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Reset 清空请求记录。
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

// Object 返回桶内 key 的对象（不存在时为 nil）。
func (s *Server) Object(key string) *Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[key]
}

// Put 直接放一个对象（测试准备用）。
func (s *Server) Put(key string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = &Object{Body: append([]byte(nil), body...), Meta: map[string]string{}}
}

// Delete 直接删掉一个对象（模拟远端丢失）。
func (s *Server) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
}

// Keys 返回全部对象键。
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	return keys
}

// split 从虚拟主机风格（<bucket>.<host>）或路径风格（/<bucket>/<key>）解析出桶名与键。
func (s *Server) split(r *http.Request) (bucket, key string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if strings.HasPrefix(host, s.bucket+".") {
		return s.bucket, path
	}
	bucket, key, _ = strings.Cut(path, "/")
	return bucket, key
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Host: r.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header.Clone()})
	hook, mutate := s.Hook, s.MutateGet
	s.mu.Unlock()

	bucket, key := s.split(r)
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=") {
		writeError(w, r, http.StatusForbidden, "AccessDenied", key)
		return
	}
	if bucket != s.bucket || key == "" {
		writeError(w, r, http.StatusNotFound, "NoSuchBucket", key)
		return
	}
	if hook != nil {
		if status, code, handled := hook(r, key); handled {
			if status < 300 {
				w.WriteHeader(status)
				return
			}
			writeError(w, r, status, code, key)
			return
		}
	}
	switch r.Method {
	case http.MethodPut:
		s.put(w, r, key)
	case http.MethodHead, http.MethodGet:
		s.mu.Lock()
		obj := s.objects[key]
		s.mu.Unlock()
		if obj == nil {
			writeError(w, r, http.StatusNotFound, "NoSuchKey", key)
			return
		}
		body := obj.Body
		if r.Method == http.MethodGet && mutate != nil {
			body = mutate(key, append([]byte(nil), body...))
		}
		sum := md5.Sum(obj.Body)
		h := w.Header()
		h.Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		h.Set("Content-Length", fmt.Sprint(len(body)))
		h.Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		h.Set("Content-Type", obj.ContentType)
		if obj.StorageClass != "" {
			h.Set("X-Amz-Storage-Class", obj.StorageClass)
		}
		for name, value := range obj.Meta {
			h.Set("X-Amz-Meta-"+name, value)
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	default:
		writeError(w, r, http.StatusNotImplemented, "NotImplemented", key)
	}
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, key string) {
	query := r.URL.Query()
	if query.Has("uploads") || query.Has("partNumber") || query.Has("uploadId") {
		s.t.Errorf("假 S3 收到分片上传请求 %s %s?%s：本批只用单次 PUT", r.Method, r.URL.Path, r.URL.RawQuery)
		writeError(w, r, http.StatusBadRequest, "NotImplemented", key)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "IncompleteBody", key)
		return
	}
	sum := md5.Sum(body)
	if want := r.Header.Get("Content-MD5"); want != "" && want != base64.StdEncoding.EncodeToString(sum[:]) {
		writeError(w, r, http.StatusBadRequest, "BadDigest", key)
		return
	}
	obj := &Object{Body: body, ContentType: r.Header.Get("Content-Type"), StorageClass: r.Header.Get("X-Amz-Storage-Class"), Meta: map[string]string{}}
	for name, values := range r.Header {
		if lower := strings.ToLower(name); strings.HasPrefix(lower, "x-amz-meta-") && len(values) > 0 {
			obj.Meta[strings.TrimPrefix(lower, "x-amz-meta-")] = values[0]
		}
	}
	s.mu.Lock()
	s.objects[key] = obj
	s.mu.Unlock()
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	w.WriteHeader(http.StatusOK)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, key string) {
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>fake %s</Message><Key>%s</Key><RequestId>fake</RequestId></Error>`, code, code, key)
}
