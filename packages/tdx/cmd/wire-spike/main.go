// Command wire-spike records one complete TDX session for protocol review.
// It is intentionally a small, bounded diagnostic tool: it performs one
// source-specific probe and never loops over a server list.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tdx "github.com/mooyang-code/moox/packages/tdx"
)

type captureConn struct {
	net.Conn
	mu       sync.Mutex
	writes   []byte
	reads    []byte
	readCall int
}

func (conn *captureConn) Write(data []byte) (int, error) {
	conn.mu.Lock()
	conn.writes = append(conn.writes, data...)
	conn.mu.Unlock()
	return conn.Conn.Write(data)
}

func (conn *captureConn) Read(data []byte) (int, error) {
	n, err := conn.Conn.Read(data)
	if n > 0 {
		conn.mu.Lock()
		conn.reads = append(conn.reads, data[:n]...)
		conn.readCall++
		conn.mu.Unlock()
	}
	return n, err
}

func (conn *captureConn) snapshot() (writes, reads []byte, readCalls int) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return append([]byte(nil), conn.writes...), append([]byte(nil), conn.reads...), conn.readCall
}

type report struct {
	Host             string   `json:"host"`
	Port             int      `json:"port"`
	Variant          string   `json:"variant"`
	StartedAt        string   `json:"started_at"`
	FinishedAt       string   `json:"finished_at"`
	Success          bool     `json:"success"`
	Error            string   `json:"error,omitempty"`
	WritesBytes      int      `json:"writes_bytes"`
	ReadsBytes       int      `json:"reads_bytes"`
	ReadCalls        int      `json:"read_calls"`
	ResponsePrefixes []string `json:"response_prefixes,omitempty"`
}

func main() {
	host := flag.String("host", "", "TDX host or IP")
	port := flag.Int("port", 0, "TDX port; defaults to 7709 or 7727")
	variantName := flag.String("variant", "normal", "normal, ex_classic, or ex_mac")
	timeout := flag.Duration("timeout", 8*time.Second, "whole-session timeout")
	outDir := flag.String("out", "", "optional directory for raw request/response streams and report.json")
	flag.Parse()

	variant, err := parseVariant(*variantName)
	if err != nil {
		fatal(err)
	}
	if strings.TrimSpace(*host) == "" {
		fatal(fmt.Errorf("-host is required"))
	}
	if *port == 0 {
		if variant == tdx.ProtocolNormal {
			*port = 7709
		} else {
			*port = 7727
		}
	}
	started := time.Now().UTC()
	var captured *captureConn
	client, err := tdx.NewClient(tdx.ClientOptions{
		Host: *host, Port: *port, Variant: variant, Timeout: *timeout,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, dialErr := (&net.Dialer{}).DialContext(ctx, network, address)
			if dialErr != nil {
				return nil, dialErr
			}
			captured = &captureConn{Conn: conn}
			return captured, nil
		},
	})
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	runErr := client.Connect(ctx)
	if runErr == nil {
		switch variant {
		case tdx.ProtocolNormal:
			_, runErr = (&tdx.NormalClient{Client: client}).SecurityCount(ctx, tdx.MarketSZ)
		case tdx.ProtocolExClassic:
			_, _, runErr = client.Execute(ctx, tdx.ExtendedMarketsRequest())
		case tdx.ProtocolExMAC:
			runErr = (&tdx.ExtendedClient{Client: client}).Login(ctx)
		}
	}
	_ = client.Close()
	finished := time.Now().UTC()

	result := report{Host: *host, Port: *port, Variant: string(variant), StartedAt: started.Format(time.RFC3339Nano), FinishedAt: finished.Format(time.RFC3339Nano), Success: runErr == nil}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	var writes, reads []byte
	if captured != nil {
		writes, reads, result.ReadCalls = captured.snapshot()
		result.WritesBytes, result.ReadsBytes = len(writes), len(reads)
		result.ResponsePrefixes = responsePrefixes(reads)
	}
	if *outDir != "" {
		if err := writeCapture(*outDir, writes, reads, result); err != nil {
			fatal(err)
		}
	}
	encoded, _ := json.Marshal(result)
	fmt.Println(string(encoded))
	if runErr != nil {
		os.Exit(1)
	}
}

func parseVariant(raw string) (tdx.ProtocolVariant, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "normal", "tdx_normal":
		return tdx.ProtocolNormal, nil
	case "ex_classic", "tdx_ex_classic":
		return tdx.ProtocolExClassic, nil
	case "ex_mac", "tdx_ex_mac":
		return tdx.ProtocolExMAC, nil
	default:
		return "", fmt.Errorf("unsupported -variant %q", raw)
	}
}

func responsePrefixes(raw []byte) []string {
	const headerSize = 16
	result := make([]string, 0)
	for offset := 0; offset+headerSize <= len(raw); {
		zipSize := int(raw[offset+12]) | int(raw[offset+13])<<8
		frameEnd := offset + headerSize + zipSize
		if frameEnd > len(raw) {
			break
		}
		result = append(result, hex.EncodeToString(raw[offset:offset+headerSize]))
		offset = frameEnd
	}
	return result
}

func writeCapture(dir string, writes, reads []byte, result report) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create capture directory: %w", err)
	}
	for name, data := range map[string][]byte{"request-stream.bin": writes, "response-stream.bin": reads} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
