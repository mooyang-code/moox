package resolver

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveNormalizesFiltersSortsAndCaches(t *testing.T) {
	var lookups atomic.Int32
	var probes atomic.Int32
	r := New(Config{
		Domains:         []string{"fapi.binance.com"},
		CacheTTL:        time.Minute,
		MaxIPsPerDomain: 4,
		LookupHost: func(context.Context, string) ([]string, error) {
			lookups.Add(1)
			return []string{"2001:db8::1", "1.1.1.1", "8.8.8.8", "8.8.8.8"}, nil
		},
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			probes.Add(1)
			if address == "1.1.1.1:443" {
				time.Sleep(2 * time.Millisecond)
			}
			return &fakeConn{}, nil
		},
	})
	first, err := r.Resolve(context.Background(), []string{" FAPI.BINANCE.COM. "}, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, "fapi.binance.com", first[0].Domain)
	require.Len(t, first[0].IPs, 1)
	require.Equal(t, "8.8.8.8", first[0].IPs[0].IP)
	second, err := r.Resolve(context.Background(), []string{"fapi.binance.com"}, 4)
	require.NoError(t, err)
	require.Len(t, second[0].IPs, 2)
	require.Equal(t, "8.8.8.8", second[0].IPs[0].IP)
	require.Equal(t, "1.1.1.1", second[0].IPs[1].IP)
	require.Equal(t, int32(1), lookups.Load(), "a max-IP=1 request must not poison the full cache")
	require.Equal(t, int32(2), probes.Load())
}

func TestResolveReturnsPartialProbeAndPerDomainFailures(t *testing.T) {
	r := New(Config{
		Domains: []string{"good.example.com", "bad.example.com"},
		LookupHost: func(_ context.Context, domain string) ([]string, error) {
			if domain == "bad.example.com" {
				return nil, errors.New("lookup failed")
			}
			return []string{"8.8.8.8", "1.1.1.1"}, nil
		},
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			if address == "1.1.1.1:443" {
				return nil, errors.New("probe failed")
			}
			return &fakeConn{}, nil
		},
	})
	results, err := r.Resolve(context.Background(), []string{"bad.example.com", "good.example.com"}, 4)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "bad.example.com", results[0].Domain)
	require.True(t, results[0].Unresolved)
	require.Equal(t, "lookup_failed", results[0].Reason)
	require.Equal(t, "good.example.com", results[1].Domain)
	require.False(t, results[1].Unresolved)
	require.Equal(t, []string{"8.8.8.8"}, []string{results[1].IPs[0].IP})
}

func TestResolveRejectsInvalidAndDisallowedDomains(t *testing.T) {
	r := New(Config{Domains: []string{"fapi.binance.com"}})
	_, err := r.Resolve(context.Background(), []string{"http://fapi.binance.com"}, 1)
	require.ErrorIs(t, err, ErrInvalidDomain)
	results, err := r.Resolve(context.Background(), []string{"api.binance.com"}, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Unresolved)
	require.Equal(t, "domain_not_allowed", results[0].Reason)
	_, err = r.Resolve(context.Background(), nil, 1)
	require.ErrorIs(t, err, ErrInvalidDomain)
}

func TestResolveRejectsMaxIPCapAboveProtocolLimit(t *testing.T) {
	r := New(Config{Domains: []string{"fapi.binance.com"}})
	_, err := r.Resolve(context.Background(), []string{"fapi.binance.com"}, 5)
	require.ErrorIs(t, err, ErrInvalidDomain)
}

func TestResolveHonorsLookupContext(t *testing.T) {
	r := New(Config{
		Domains:       []string{"fapi.binance.com"},
		LookupTimeout: 5 * time.Millisecond,
		LookupHost: func(ctx context.Context, _ string) ([]string, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	results, err := r.Resolve(context.Background(), []string{"fapi.binance.com"}, 1)
	require.NoError(t, err)
	require.True(t, results[0].Unresolved)
	require.Equal(t, "lookup_failed", results[0].Reason)
}

func TestResolveRejectsNonPublicIPv4AndProbesBeyondOutputLimit(t *testing.T) {
	var probed []string
	var probedMu sync.Mutex
	r := New(Config{
		Domains:         []string{"fapi.binance.com"},
		MaxIPsPerDomain: 2,
		LookupHost: func(context.Context, string) ([]string, error) {
			return []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "203.0.113.1", "8.8.8.8", "1.1.1.1"}, nil
		},
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			probedMu.Lock()
			probed = append(probed, address)
			probedMu.Unlock()
			return &fakeConn{}, nil
		},
	})
	results, err := r.Resolve(context.Background(), []string{"fapi.binance.com"}, 2)
	require.NoError(t, err)
	require.False(t, results[0].Unresolved)
	require.Equal(t, []string{"1.1.1.1", "8.8.8.8"}, []string{results[0].IPs[0].IP, results[0].IPs[1].IP})
	probedMu.Lock()
	gotProbed := append([]string(nil), probed...)
	probedMu.Unlock()
	require.ElementsMatch(t, []string{"8.8.8.8:443", "1.1.1.1:443"}, gotProbed)
}

type fakeConn struct{}

func (*fakeConn) Read([]byte) (int, error)         { return 0, errors.New("not implemented") }
func (*fakeConn) Write([]byte) (int, error)        { return 0, errors.New("not implemented") }
func (*fakeConn) Close() error                     { return nil }
func (*fakeConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (*fakeConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (*fakeConn) SetDeadline(time.Time) error      { return nil }
func (*fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (*fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }
