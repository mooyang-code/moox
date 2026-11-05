package netguard

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := map[string]Class{
		"8.8.8.8":          Public,
		"104.18.0.1":       Public,
		"2606:4700::1111":  Public,
		"127.0.0.1":        Blocked,
		"::1":              Blocked,
		"::ffff:127.0.0.1": Blocked,
		"169.254.169.254":  Blocked,
		"fe80::1":          Blocked,
		"224.0.0.1":        Blocked,
		"ff02::1":          Blocked,
		"0.0.0.0":          Blocked,
		"::":               Blocked,
		"255.255.255.255":  Blocked,
		"192.0.2.1":        Blocked,
		"198.18.0.1":       Blocked,
		"2001:db8::1":      Blocked,
		"64:ff9b::808:808": Blocked,
		"10.1.2.3":         Private,
		"172.16.0.1":       Private,
		"172.31.255.255":   Private,
		"192.168.1.1":      Private,
		"100.64.0.1":       Private,
		"100.127.255.255":  Private,
		"fd00::1":          Private,
		"::ffff:10.0.0.1":  Private,
		"172.32.0.1":       Public,
		"100.128.0.1":      Public,
	}
	for raw, want := range cases {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("无法解析 %s", raw)
		}
		if got := Classify(ip); got != want {
			t.Errorf("Classify(%s) = %d，期望 %d", raw, got, want)
		}
	}
	if Classify(net.IP{1, 2, 3}) != Blocked {
		t.Error("长度非法的地址必须拒绝")
	}
}

func TestAllowed(t *testing.T) {
	if Allowed(net.ParseIP("10.0.0.1"), false) || !Allowed(net.ParseIP("10.0.0.1"), true) {
		t.Error("内网地址只有明确允许才放行")
	}
	for _, raw := range []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "224.0.0.1"} {
		if Allowed(net.ParseIP(raw), true) {
			t.Errorf("%s 即使允许内网也必须拒绝", raw)
		}
	}
}

func TestDialContext(t *testing.T) {
	var dialed []string
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}
	lookup := func(addresses ...string) func(context.Context, string) ([]string, error) {
		return func(context.Context, string) ([]string, error) { return addresses, nil }
	}

	// 解析结果里混有内网和回环地址时，只对公网地址拨号，并且用 IP 而不是域名拨号。
	conn, err := NewDialContext(DialOptions{Lookup: lookup("127.0.0.1", "10.0.0.5", "93.184.216.34"), Dial: dial})(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("拨号地址 = %v", dialed)
	}

	// 域名只解析到不允许的地址：不拨号。
	dialed = nil
	_, err = NewDialContext(DialOptions{Lookup: lookup("127.0.0.1", "::1", "169.254.169.254", "10.0.0.5"), Dial: dial})(context.Background(), "tcp", "rebind.example:443")
	if !errors.Is(err, ErrNoReachableAddress) || len(dialed) != 0 {
		t.Fatalf("err = %v，拨号 = %v", err, dialed)
	}

	// 允许内网后，内网地址放行，回环依然拒绝。
	conn, err = NewDialContext(DialOptions{Lookup: lookup("127.0.0.1", "10.0.0.5"), Dial: dial, AllowPrivate: true})(context.Background(), "tcp", "intranet.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if dialed[len(dialed)-1] != "10.0.0.5:8080" {
		t.Fatalf("拨号地址 = %v", dialed)
	}

	// IP 字面量直接检查，不走解析。
	lookupCalled := false
	guarded := NewDialContext(DialOptions{Lookup: func(context.Context, string) ([]string, error) { lookupCalled = true; return nil, nil }, Dial: dial})
	for _, address := range []string{"127.0.0.1:80", "[::1]:80", "169.254.169.254:80", "10.0.0.1:80", "[fd00::1]:80"} {
		if _, err := guarded(context.Background(), "tcp", address); !errors.Is(err, ErrNoReachableAddress) {
			t.Errorf("%s: err = %v", address, err)
		}
	}
	if lookupCalled {
		t.Error("IP 字面量不应该做域名解析")
	}

	// 端口限制、解析失败。
	if _, err := NewDialContext(DialOptions{Lookup: lookup("93.184.216.34"), Dial: dial, AllowedPorts: []string{"443"}})(context.Background(), "tcp", "example.com:80"); !errors.Is(err, ErrPortNotAllowed) {
		t.Errorf("端口限制: %v", err)
	}
	failing := func(context.Context, string) ([]string, error) { return nil, errors.New("nxdomain") }
	if _, err := NewDialContext(DialOptions{Lookup: failing, Dial: dial})(context.Background(), "tcp", "nx.example:80"); !errors.Is(err, ErrLookupFailed) {
		t.Errorf("解析失败: %v", err)
	}
}
