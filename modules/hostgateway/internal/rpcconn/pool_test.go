package rpcconn

import (
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

// 上游已关闭的空闲明文连接不能被复用：否则请求发到死连接上，读响应时只会得到 EOF（tRPC 171）。
func TestPlainPoolDropsPeerClosedIdleConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	pool := New(nil)
	defer pool.Close()

	first, err := pool.Get("tcp", ln.Addr().String(), connpool.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstLocal := first.LocalAddr().String()
	if err := first.Close(); err != nil { // 放回连接池
		t.Fatal(err)
	}
	server := <-accepted
	server.Close() // 上游主动关闭这条空闲连接
	defer func() {
		select {
		case conn := <-accepted:
			conn.Close()
		default:
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		second, err := pool.Get("tcp", ln.Addr().String(), connpool.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		reused := second.LocalAddr().String() == firstLocal
		second.Close()
		if !reused {
			return
		}
		// 对端的 FIN 可能还没到达本端套接字，稍等后重试；超时说明连接池没能识别死连接。
		if time.Now().After(deadline) {
			t.Fatal("连接池复用了已被上游关闭的空闲连接")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDialOnlyExposesRawSocketForPlaintext(t *testing.T) {
	plainLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer plainLn.Close()
	go func() {
		for {
			conn, err := plainLn.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()
	plain := New(nil)
	defer plain.Close()
	conn, err := plain.dial(&connpool.DialOptions{Network: "tcp", Address: plainLn.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, ok := conn.(syscall.Conn); !ok {
		t.Fatal("明文连接应透出原始套接字，供连接池探测对端关闭")
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	secure := New(server.Client().Transport.(*http.Transport).TLSClientConfig)
	defer secure.Close()
	tlsConn, err := secure.dial(&connpool.DialOptions{Network: "tcp", Address: server.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer tlsConn.Close()
	if _, ok := tlsConn.(syscall.Conn); ok {
		t.Fatal("TLS 连接不能透出原始套接字：连接池直接读套接字会吞掉 TLS 记录")
	}
}
