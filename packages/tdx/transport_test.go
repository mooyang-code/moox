package tdx

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

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
