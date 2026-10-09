package router

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

const primary = "trpc.moox.storage.PrimaryStore"

type forwardFunc func(context.Context, servicecatalog.Route, int, []byte, codec.MetaData) ([]byte, error)

func (f forwardFunc) Forward(ctx context.Context, route servicecatalog.Route, serialization int, body []byte, metadata codec.MetaData) ([]byte, error) {
	return f(ctx, route, serialization, body, metadata)
}

func proxyFixture(t *testing.T, forwarder Forwarder) (*Proxy, *snapshot.State) {
	t.Helper()
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, nonces.Close()) })
	state := &snapshot.State{}
	applySnapshot(t, state, testsnapshot.New(t, "storage", "storage-primary"))
	p, err := NewService(ServiceOptions{State: state, Nonces: nonces, Forwarder: forwarder})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Close()) })
	return p, state
}

func applySnapshot(t *testing.T, state *snapshot.State, raw *pb.HostGatewaySnapshot) {
	t.Helper()
	testsnapshot.Rehash(t, raw)
	view, err := snapshot.Build("storage", raw)
	require.NoError(t, err)
	state.Apply(view)
}

func dispatch(ctx context.Context, p *Proxy, method string, body []byte, headers http.Header) ([]byte, error) {
	ctx, message := codec.WithNewMessage(ctx)
	defer codec.PutBackMessage(message)
	message.WithServerRPCName("/" + primary + "/" + method)
	message.WithSerializationType(codec.SerializationTypePB)
	metadata := codec.MetaData{}
	for key := range headers {
		metadata[key] = []byte(headers.Get(key))
	}
	message.WithServerMetaData(metadata)
	response, err := p.handle(nil, ctx, func(input interface{}) (filter.ServerChain, error) {
		input.(*codec.Body).Data = body
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return response.(*codec.Body).Data, nil
}

func signed(t *testing.T, caller, method string, body []byte) http.Header {
	t.Helper()
	headers, err := testrpc.Signed(testsnapshot.Credential(caller), "storage", primary, method, body)
	require.NoError(t, err)
	return headers
}

func TestProxyRejectsUnauthorizedOrAlteredCallsBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	p, _ := proxyFixture(t, forwardFunc(func(_ context.Context, route servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		calls.Add(1)
		require.Equal(t, "127.0.0.1:20102", route.Address)
		return body, nil
	}))
	for _, denied := range []struct{ caller, method, contains string }{
		{"console", "ReadTimeSeriesRows", "not allowed"},
		{"moox-skill", "ReadTimeSeriesRows", "authentication failed"},
		{"collector", "NotRegistered", "not deployed"},
	} {
		_, err := dispatch(context.Background(), p, denied.method, nil, signed(t, denied.caller, denied.method, nil))
		require.ErrorContains(t, err, denied.contains)
	}
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", []byte("altered"), signed(t, "collector", "ReadTimeSeriesRows", []byte("original")))
	require.ErrorContains(t, err, "authentication failed")
	_, err = dispatch(context.Background(), p, "UpsertFields", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.ErrorContains(t, err, "authentication failed")
	require.Zero(t, calls.Load())
	body := []byte{0x0a, 1, 'a', 0x0a, 1, 'b'}
	headers := signed(t, "collector", "ReadTimeSeriesRows", body)
	response, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", body, headers)
	require.NoError(t, err)
	require.Equal(t, body, response)
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", body, headers)
	require.ErrorContains(t, err, "replayed")
	require.EqualValues(t, 1, calls.Load())
}

func TestProxyAppliesCallerChangesWithoutRestart(t *testing.T) {
	p, state := proxyFixture(t, forwardFunc(func(_ context.Context, _ servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		return body, nil
	}))
	original := state.Load().Proto()
	revoked := state.Load().Proto()
	for _, route := range revoked.Routes {
		var callers []string
		for _, caller := range route.Callers {
			if caller != "factor-mgr" {
				callers = append(callers, caller)
			}
		}
		route.Callers = callers
	}
	var keys []*pb.GatewayVerificationKey
	for _, key := range revoked.VerificationKeys {
		if key.Caller != "factor-mgr" {
			keys = append(keys, key)
		}
	}
	revoked.VerificationKeys = keys
	applySnapshot(t, state, revoked)
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "factor-mgr", "ReadTimeSeriesRows", nil))
	require.ErrorContains(t, err, "authentication failed")
	applySnapshot(t, state, original)
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "factor-mgr", "ReadTimeSeriesRows", nil))
	require.NoError(t, err)
	removed := state.Load().Proto()
	var routes []*pb.HostGatewayRoute
	for _, route := range removed.Routes {
		if route.Method != "ReadTimeSeriesRows" {
			routes = append(routes, route)
		}
	}
	removed.Routes = routes
	applySnapshot(t, state, removed)
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.ErrorContains(t, err, "not deployed")
}

func TestProxyBodyLimitsDeadlinesAndReplayStoreFailure(t *testing.T) {
	var calls atomic.Int32
	tooLarge := make([]byte, (32<<20)+1)
	p, _ := proxyFixture(t, forwardFunc(func(ctx context.Context, route servicecatalog.Route, _ int, _ []byte, _ codec.MetaData) ([]byte, error) {
		calls.Add(1)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), time.Duration(route.TimeoutMS)*time.Millisecond)
		return tooLarge, nil
	}))
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", tooLarge, signed(t, "collector", "ReadTimeSeriesRows", tooLarge))
	require.ErrorContains(t, err, "request exceeds")
	require.Zero(t, calls.Load())
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.ErrorContains(t, err, "response exceeds")
	require.EqualValues(t, 1, calls.Load())
	p.options.Forwarder = forwardFunc(func(ctx context.Context, _ servicecatalog.Route, _ int, _ []byte, _ codec.MetaData) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = dispatch(ctx, p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.Equal(t, errs.RetClientTimeout, errs.Code(err))
	// A failed durable nonce write must never reach the backend.
	p.options.Nonces = unavailableNonces{}
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.ErrorContains(t, err, "replay store unavailable")
}

type unavailableNonces struct{}

func (unavailableNonces) Consume(context.Context, string, string, time.Duration) (bool, error) {
	return false, errors.New("synthetic failure")
}

func TestUpstreamOnlyRetriesReadsAndStripsGatewayMetadata(t *testing.T) {
	opened, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var calls atomic.Int32
	var invalidMetadata atomic.Bool
	testrpc.Serve(t, opened, primary, func(ctx context.Context, _ []byte) ([]byte, error) {
		calls.Add(1)
		metadata := codec.Message(ctx).ServerMetaData()
		for key := range metadata {
			if strings.HasPrefix(strings.ToLower(key), "x-moox-") {
				invalidMetadata.Store(true)
			}
		}
		if string(metadata["trace-fixture"]) != "preserved" {
			invalidMetadata.Store(true)
		}
		return nil, errs.NewFrameError(errs.RetClientNetErr, "synthetic connection failure")
	})
	u := NewUpstream()
	defer u.Close()
	metadata := codec.MetaData{"X-Moox-Signature": []byte("secret"), "x-moox-extra": []byte("private"), "trace-fixture": []byte("preserved")}
	for _, readOnly := range []bool{true, false} {
		calls.Store(0)
		method := "UpsertFields"
		if readOnly {
			method = "ReadTimeSeriesRows"
		}
		_, err := u.Forward(context.Background(), servicecatalog.Route{Address: opened.Addr().String(), ServicePath: primary, Method: method, ReadOnly: readOnly, TimeoutMS: 1000}, codec.SerializationTypePB, nil, metadata)
		require.Error(t, err)
		expected := int32(1)
		if readOnly {
			expected = 2
		}
		require.Equal(t, expected, calls.Load())
	}
	require.False(t, invalidMetadata.Load())
	require.Equal(t, []byte("secret"), metadata["X-Moox-Signature"])
}
