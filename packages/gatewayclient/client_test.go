package gatewayclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

const secretService = "trpc.moox.ops.SecretMgr"

type sourceStub struct {
	mu        sync.Mutex
	directory servicecatalog.Directory
	err       error
	calls     int
}

func (s *sourceStub) Fetch(_ context.Context, version string) (DirectoryUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return DirectoryUpdate{}, s.err
	}
	return DirectoryUpdate{Changed: version != s.directory.Version, Directory: s.directory.Clone()}, nil
}

type tunnelFunc func(context.Context, string) (string, error)

func (f tunnelFunc) Resolve(ctx context.Context, id string) (string, error) { return f(ctx, id) }

func testDirectory(t *testing.T, hosts ...string) servicecatalog.Directory {
	t.Helper()
	d := servicecatalog.Directory{Services: map[string][]string{secretService: hosts}, Hosts: map[string]servicecatalog.DirectoryHost{}}
	for _, host := range hosts {
		d.Hosts[host] = servicecatalog.DirectoryHost{Address: host + ".example.test"}
	}
	var err error
	d.Version, err = d.VersionHash()
	require.NoError(t, err)
	return d
}

func tunnelConfig(source DirectorySource) Config {
	return Config{Mode: Tunnel, Credentials: gatewayauth.Credentials{Caller: "moox-cli", KeyID: "test", Secret: "test-secret"},
		Source: source, RefreshInterval: time.Hour, Tunnels: tunnelFunc(func(context.Context, string) (string, error) { return "127.0.0.1:12345", nil })}
}

func newTestClient(t *testing.T, config Config) *Client {
	t.Helper()
	c, err := New(config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}

func TestRefreshCacheFallbackAndRejectsInvalidSnapshot(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "b", "a")}
	config := tunnelConfig(source)
	config.CachePath = filepath.Join(t.TempDir(), "gatewayclient", "directory.json")
	c := newTestClient(t, config)
	cached, err := readCache(config.CachePath)
	require.NoError(t, err)
	require.Equal(t, c.Directory(), cached)
	stat, err := os.Stat(config.CachePath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), stat.Mode().Perm())
	owned := c.Directory()
	owned.Services[secretService][0] = "modified"
	require.Equal(t, "b", c.Directory().Services[secretService][0])
	source.directory.Version = "invalid"
	require.Error(t, c.Refresh(context.Background()))
	require.Equal(t, cached, c.Directory())
	source.err = errors.New("local gateway offline")
	offline := newTestClient(t, config)
	require.Equal(t, cached, offline.Directory())
	require.NoError(t, os.WriteFile(config.CachePath, []byte(`{"version":"invalid"}`), 0o600))
	_, err = New(config)
	require.ErrorContains(t, err, "initial directory unavailable")
}

func TestReadRetryMovesHostAndResignsWhileWritesRunOnce(t *testing.T) {
	for _, method := range []string{"GetSecret", "UpdateSecret"} {
		t.Run(method, func(t *testing.T) {
			source := &sourceStub{directory: testDirectory(t, "b", "a")}
			c := newTestClient(t, tunnelConfig(source))
			calls := 0
			var nonce string
			body := []byte(" {\"key\": \"value\"} \n")
			c.invoke = func(_ context.Context, ep endpoint, service, method string, serialization int, raw []byte, headers http.Header) ([]byte, error) {
				calls++
				require.Equal(t, body, raw)
				require.Equal(t, codec.SerializationTypeJSON, serialization)
				claims, err := gatewayauth.Verify(c.config.Credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: ep.target, Callee: service, Func: method, Body: raw}, headers, time.Now())
				require.NoError(t, err)
				if calls == 1 {
					require.Equal(t, "a", ep.target)
					nonce = claims.Nonce
					return nil, errs.NewFrameError(errs.RetServerNoService, "service moved")
				}
				require.Equal(t, "b", ep.target)
				require.NotEqual(t, nonce, claims.Nonce)
				return raw, nil
			}
			response, err := c.Forward(context.Background(), secretService, method, codec.SerializationTypeJSON, body)
			if method == "GetSecret" {
				require.NoError(t, err)
				require.Equal(t, body, response)
				require.Equal(t, 2, calls)
			} else {
				require.Error(t, err)
				require.Equal(t, 1, calls)
			}
			require.GreaterOrEqual(t, source.calls, 2, "route failure refreshes even for a write")
		})
	}
}

func TestInternalSelectsLocalOtherwiseTLSWithPrivateCA(t *testing.T) {
	ca, _, _ := testCertificates(t)
	source := &sourceStub{directory: testDirectory(t, "b", "a")}
	config := tunnelConfig(source)
	config.Mode = Internal
	config.LocalHostID = "b"
	config.Tunnels = nil
	config.CAFile = ca
	c := newTestClient(t, config)
	ep, err := c.endpoint(context.Background(), secretService, "")
	require.NoError(t, err)
	require.Equal(t, endpoint{address: "127.0.0.1:11002", target: "b"}, ep)
	ep, err = c.endpoint(context.Background(), secretService, "b")
	require.NoError(t, err)
	require.Equal(t, endpoint{address: "a.example.test:11003", target: "a", caFile: ca, serverName: "a"}, ep)
	for _, invalid := range []string{"", "none", "root"} {
		config.CAFile = invalid
		_, err = New(config)
		require.Error(t, err)
	}
}

func TestExternalWhitelistAndTunnelLoopbackBoundary(t *testing.T) {
	config := Config{Mode: External, Credentials: gatewayauth.Credentials{Caller: "moox-skill", KeyID: "skill", Secret: "secret"}, AccessAddress: "access.example.test:11004", AccessInstanceID: "access@storage"}
	c := newTestClient(t, config)
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		t.Fatal("forbidden call reached transport")
		return nil, nil
	}
	_, err := c.Forward(context.Background(), secretService, "GetSecretValue", codec.SerializationTypePB, nil)
	require.ErrorContains(t, err, "not allowed")
	config.AccessInstanceID = "storage"
	_, err = New(config)
	require.Error(t, err)
	config.AccessInstanceID = "access@storage"
	config.Source = &sourceStub{}
	_, err = New(config)
	require.ErrorContains(t, err, "does not use directory")
	tunnel := tunnelConfig(&sourceStub{directory: testDirectory(t, "a")})
	tunnel.Tunnels = tunnelFunc(func(context.Context, string) (string, error) { return "192.0.2.1:11002", nil })
	c = newTestClient(t, tunnel)
	_, err = c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypeJSON, nil)
	require.ErrorContains(t, err, "non-loopback")
	_, err = NewLocalDirectorySource("192.0.2.1:11002", time.Second)
	require.Error(t, err)
}

func TestExternalReadsResignRetriesAndPeriodWritesRunOnce(t *testing.T) {
	for _, call := range []struct {
		service, method string
		attempts        int
	}{
		{"trpc.moox.storage.PrimaryStore", "GetDatasetPeriodStatus", 2},
		{"trpc.moox.storage.PrimaryStore", "EnsureDatasetPeriod", 1},
		{"trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", 1},
		{"trpc.moox.storage.PrimaryStore", "RecordDatasetPeriodFailures", 1},
		{"trpc.moox.collector.MarketFetchRuntime", "ClaimTimerBatch", 1},
	} {
		t.Run(call.method, func(t *testing.T) {
			config := Config{Mode: External, Credentials: gatewayauth.Credentials{Caller: "scf-collector", KeyID: "assigned-scf-key-71", Secret: "fixture-key"},
				AccessAddress: "access.example.test:11004", AccessInstanceID: "access@storage"}
			client := newTestClient(t, config)
			body := []byte{0x0a, 1, 'a', 0x0a, 1, 'b'}
			attempts := 0
			nonces := map[string]bool{}
			client.invoke = func(_ context.Context, ep endpoint, service, method string, serialization int, raw []byte, headers http.Header) ([]byte, error) {
				attempts++
				require.Equal(t, endpoint{address: config.AccessAddress, target: config.AccessInstanceID}, ep)
				require.Equal(t, codec.SerializationTypePB, serialization)
				require.Equal(t, body, raw)
				claims, err := gatewayauth.Verify(config.Credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method,
					TargetNode: config.AccessInstanceID, Callee: service, Func: method, Body: raw}, headers, time.Now())
				require.NoError(t, err)
				require.False(t, nonces[claims.Nonce], "every send must use a fresh nonce")
				nonces[claims.Nonce] = true
				if attempts == 1 {
					return nil, errs.NewFrameError(errs.RetClientNetErr, "unknown remote outcome")
				}
				return raw, nil
			}
			response, err := client.Forward(context.Background(), call.service, call.method, codec.SerializationTypePB, body)
			if call.attempts == 1 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, body, response)
			}
			require.Equal(t, call.attempts, attempts)
		})
	}
}

type sourceFunc func(context.Context, string) (DirectoryUpdate, error)

func (f sourceFunc) Fetch(ctx context.Context, version string) (DirectoryUpdate, error) {
	return f(ctx, version)
}

func TestRefreshWaitingOnAnotherFetchHonorsContext(t *testing.T) {
	c := newTestClient(t, tunnelConfig(&sourceStub{directory: testDirectory(t, "a")}))
	started := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan error, 1)
	c.config.Source = sourceFunc(func(ctx context.Context, _ string) (DirectoryUpdate, error) {
		close(started)
		select {
		case <-finish:
			return DirectoryUpdate{}, errors.New("done")
		case <-ctx.Done():
			return DirectoryUpdate{}, ctx.Err()
		}
	})
	go func() { done <- c.Refresh(context.Background()) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, c.Refresh(ctx), context.DeadlineExceeded)
	close(finish)
	require.Error(t, <-done)
}

func TestWithdrawalAppliesWhenCacheCannotBeReplaced(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a", "b")}
	config := tunnelConfig(source)
	config.CachePath = filepath.Join(t.TempDir(), "directory.json")
	c := newTestClient(t, config)
	// Simulate a persist failure after a valid directory has been accepted.
	require.NoError(t, os.Remove(config.CachePath))
	require.NoError(t, os.Mkdir(config.CachePath, 0o700))
	source.directory = testDirectory(t, "b")
	require.ErrorContains(t, c.Refresh(context.Background()), "persist directory")
	ep, err := c.endpoint(context.Background(), secretService, "")
	require.NoError(t, err)
	require.Equal(t, "b", ep.target, "a withdrawn host must not remain active after a disk failure")
	require.NoError(t, c.Close())
	_, err = c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypePB, nil)
	require.ErrorIs(t, err, net.ErrClosed)
	require.ErrorIs(t, c.Refresh(context.Background()), net.ErrClosed)
}

func TestBusinessErrorsDoNotRetryOrRefresh(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a", "b")}
	c := newTestClient(t, tunnelConfig(source))
	calls, fetches := 0, source.calls
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		calls++
		return nil, errs.New(12345, "business error")
	}
	_, err := c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypePB, nil)
	require.EqualValues(t, 12345, errs.Code(err))
	require.Equal(t, 1, calls)
	require.Equal(t, fetches, source.calls)
}

func TestInvokeSanitizesMalformedResponseAndDoesNotRetryDecode(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a")}
	config := tunnelConfig(source)
	config.Serialization = codec.SerializationTypeJSON
	c := newTestClient(t, config)
	calls, fetches := 0, source.calls
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		calls++
		return []byte("recognizable-secret-response"), nil
	}
	err := c.Invoke(t.Context(), secretService, "GetSecret", &emptypb.Empty{}, &emptypb.Empty{})
	require.EqualValues(t, errs.RetClientDecodeFail, errs.Code(err))
	require.NotContains(t, err.Error(), "recognizable-secret-response")
	require.Equal(t, 1, calls)
	require.Equal(t, fetches, source.calls)
}
