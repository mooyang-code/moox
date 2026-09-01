package tdx

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestTransportErrorsAreClassifiedForRouteRecovery(t *testing.T) {
	client, err := NewClient(ClientOptions{Host: "quotes.example", Port: 7709, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Execute(context.Background(), []byte{1})
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("unconnected execute error = %v, want ErrTransport", err)
	}
}

func TestConnectPreservesUnderlyingDialError(t *testing.T) {
	dialErr := errors.New("dial sentinel")
	client, err := NewClient(ClientOptions{
		Host: "quotes.example", Port: 7709, Timeout: time.Second,
		Dial: func(context.Context, string, string) (net.Conn, error) { return nil, dialErr },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Connect(context.Background())
	if !errors.Is(err, ErrTransport) || !errors.Is(err, dialErr) {
		t.Fatalf("connect error = %v, want ErrTransport and original dial error", err)
	}
}

func TestClientAddressFormatsIPv6Endpoint(t *testing.T) {
	client, err := NewClient(ClientOptions{Host: "2001:db8::1", Port: 7709})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := client.Address(), "[2001:db8::1]:7709"; got != want {
		t.Fatalf("IPv6 address = %q, want %q", got, want)
	}
}

func TestClientClassifiesHTTPProxyResponseAsProtocolError(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		request := make([]byte, 1)
		if _, err := io.ReadFull(serverConn, request); err != nil {
			serverDone <- err
			return
		}
		_, err := serverConn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
		serverDone <- err
	}()

	client, err := NewClient(ClientOptions{
		Host: "quotes.example", Port: 7727, Variant: ProtocolExClassic, Timeout: time.Second,
		Dial: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Execute(context.Background(), []byte{1})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("HTTP proxy response error = %v, want ErrProtocol", err)
	}
	if !strings.Contains(err.Error(), "HTTP/1.1 403") {
		t.Fatalf("HTTP proxy response error = %v, want status prefix", err)
	}
	<-serverDone
}

func TestClientCancellationInterruptsBlockedRoundTrip(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	requestRead := make(chan error, 1)
	go func() {
		request := make([]byte, 1)
		_, err := io.ReadFull(serverConn, request)
		requestRead <- err
	}()

	client, err := NewClient(ClientOptions{
		Host: "quotes.example", Port: 7727, Variant: ProtocolExClassic, Timeout: time.Minute,
		Dial: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, executeErr := client.Execute(ctx, []byte{1})
		result <- executeErr
	}()
	if err := <-requestRead; err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled execute error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled execute did not unblock the TCP read")
	}
}

func TestSecurityBarsDropsConnectionAfterPayloadProtocolError(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		request := make([]byte, 40)
		if _, err := io.ReadFull(serverConn, request); err != nil {
			serverDone <- err
			return
		}
		body := []byte{1, 0}
		frame := make([]byte, HeaderSize+len(body))
		binary.LittleEndian.PutUint16(frame[12:14], uint16(len(body)))
		binary.LittleEndian.PutUint16(frame[14:16], uint16(len(body)))
		copy(frame[HeaderSize:], body)
		_, err := serverConn.Write(frame)
		serverDone <- err
	}()

	client, err := NewClient(ClientOptions{
		Host: "quotes.example", Port: 7709, Variant: ProtocolExClassic, Timeout: time.Second,
		Dial: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	normal := &NormalClient{Client: client}
	if err := normal.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = normal.SecurityBars(context.Background(), MarketSH, "600000", CategoryDay, 0, 1, false)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("malformed bars error = %v, want ErrProtocol", err)
	}
	if client.conn != nil {
		t.Fatal("client connection remains reusable after a payload protocol error")
	}
	<-serverDone
}

func TestNormalClientCompletesSetupAndCountOverLongLivedTCPConnection(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		lengths := []int{len(setupCommands[0]), len(setupCommands[1]), len(setupCommands[2]), len(SecurityCountRequest(MarketSZ))}
		for index, length := range lengths {
			request := make([]byte, length)
			if _, err := io.ReadFull(serverConn, request); err != nil {
				serverErr <- err
				return
			}
			body := []byte{0, 0}
			if index == len(lengths)-1 {
				body = []byte{42, 0}
			}
			frame := make([]byte, HeaderSize+len(body))
			binary.LittleEndian.PutUint16(frame[12:14], uint16(len(body)))
			binary.LittleEndian.PutUint16(frame[14:16], uint16(len(body)))
			if _, err := serverConn.Write(frame[:HeaderSize]); err != nil {
				serverErr <- err
				return
			}
			if _, err := serverConn.Write(body); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	client, err := NewClient(ClientOptions{Host: "quotes.example", Port: 7709, Variant: ProtocolNormal, Timeout: time.Second, Dial: func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	normal := &NormalClient{Client: client}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := normal.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	count, err := normal.SecurityCount(ctx, MarketSZ)
	if err != nil {
		t.Fatal(err)
	}
	if count != 42 {
		t.Fatalf("security count = %d, want 42", count)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestClientReconnectChangesTargetAndRunsSetup(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	serverErr := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		for _, length := range []int{len(setupCommands[0]), len(setupCommands[1]), len(setupCommands[2])} {
			request := make([]byte, length)
			if _, err := io.ReadFull(serverConn, request); err != nil {
				serverErr <- err
				return
			}
			frame := make([]byte, HeaderSize+2)
			binary.LittleEndian.PutUint16(frame[12:14], 2)
			binary.LittleEndian.PutUint16(frame[14:16], 2)
			if _, err := serverConn.Write(frame); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()
	client, err := NewClient(ClientOptions{Host: "old.example", Port: 7709, Variant: ProtocolNormal, Timeout: time.Second, Dial: func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Reconnect(context.Background(), "new.example", 7709); err != nil {
		t.Fatal(err)
	}
	if client.Address() != "new.example:7709" {
		t.Fatalf("reconnected address = %s", client.Address())
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}
