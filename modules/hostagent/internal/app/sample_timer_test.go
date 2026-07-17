package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/server"
)

type sampleContextKey struct{}

func TestSampleHandlerWaitsAndPreservesInvocationValues(t *testing.T) {
	release := make(chan struct{})
	done := make(chan error, 1)
	handler, err := newSampleHandler(time.Second, func(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
		assert.Equal(t, "sample-value", ctx.Value(sampleContextKey{}))
		<-release
		return &hostagentpb.RunOnceRsp{}, nil
	})
	require.NoError(t, err)

	go func() {
		done <- handler.Handle(context.WithValue(context.Background(), sampleContextKey{}, "sample-value"))
	}()
	select {
	case err := <-done:
		t.Fatalf("handler returned before RunOnce completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
}

func TestSampleHandlerReturnsExecutionError(t *testing.T) {
	want := errors.New("publish failed")
	handler, err := newSampleHandler(time.Second, func(context.Context) (*hostagentpb.RunOnceRsp, error) {
		return nil, want
	})
	require.NoError(t, err)
	assert.ErrorIs(t, handler.Handle(context.Background()), want)
}

func TestSampleHandlerReturnsTimeout(t *testing.T) {
	handler, err := newSampleHandler(time.Millisecond, func(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.NoError(t, err)
	assert.ErrorIs(t, handler.Handle(context.Background()), context.DeadlineExceeded)
}

func TestSampleHandlerDetachesInvocationDeadline(t *testing.T) {
	handler, err := newSampleHandler(time.Second, func(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
		assert.NoError(t, ctx.Err())
		return &hostagentpb.RunOnceRsp{}, nil
	})
	require.NoError(t, err)
	invocationCtx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, handler.Handle(invocationCtx))
}

func TestSampleHandlerCancelsActiveRunOnServerShutdown(t *testing.T) {
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	started := make(chan struct{})
	handler, err := newSampleHandlerWithShutdown(time.Second, shutdownCtx, func(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- handler.Handle(context.Background()) }()
	<-started
	shutdown()
	assert.ErrorIs(t, <-done, context.Canceled)
}

func TestScheduledAndManualRunsShareAgentGuard(t *testing.T) {
	a := testAgent(t)
	started := make(chan struct{})
	release := make(chan struct{})
	a.collector = blockingSnapshotCollector{started: started, release: release}
	handler, err := newSampleHandler(time.Second, func(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
		return a.runOnceGuarded(ctx)
	})
	require.NoError(t, err)

	scheduledDone := make(chan error, 1)
	go func() { scheduledDone <- handler.Handle(context.Background()) }()
	<-started
	rsp, manualErr := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	assert.Error(t, manualErr)
	assert.Equal(t, "collection already running", rsp.GetPublishError())
	assert.Equal(t, uint64(1), a.skipped.Load())
	close(release)
	assert.Error(t, <-scheduledDone)
}

func TestSampleTimerConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	require.NoError(t, err)
	text := string(data)
	for _, want := range []string{
		"name: " + sampleTimerService,
		"port: 11427",
		"network: \"*/15 * * * * *?startAtOnce=1\"",
		"protocol: timer",
		"timeout: 30000",
	} {
		assert.Truef(t, strings.Contains(text, want), "timer config missing %q", want)
	}
}

func TestSampleTimerStartAtOnceRunsBeforeServerStartupCompletes(t *testing.T) {
	server := newImmediateSampleTimerServer()
	started := make(chan struct{})
	require.NoError(t, registerSampleHandler(server, func(context.Context) error {
		close(started)
		return nil
	}))
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startAtOnce handler did not run")
	}
	select {
	case err := <-serveDone:
		t.Fatalf("server returned before shutdown after successful initial sample: %v", err)
	default:
	}
	require.NoError(t, server.Close(nil))
	require.NoError(t, <-serveDone)
}

func TestSampleTimerStartAtOnceErrorFailsServerStartup(t *testing.T) {
	server := newImmediateSampleTimerServer()
	want := errors.New("initial sample failed")
	require.NoError(t, registerSampleHandler(server, func(context.Context) error { return want }))
	err := server.Serve()
	assert.ErrorContains(t, err, want.Error())
}

func newImmediateSampleTimerServer() *server.Server {
	cfg := &trpc.Config{}
	cfg.Server.Service = []*trpc.ServiceConfig{{
		Name: sampleTimerService, IP: "127.0.0.1", Port: 11427,
		Network: "*/15 * * * * *?startAtOnce=1", Protocol: "timer", Timeout: 30000,
	}}
	return trpc.NewServerWithConfig(cfg)
}

type blockingSnapshotCollector struct {
	started chan struct{}
	release chan struct{}
}

func (c blockingSnapshotCollector) Collect(ctx context.Context) (*hostmetricpb.HostSnapshot, []*hostmetricpb.CollectorStatus, error) {
	close(c.started)
	select {
	case <-c.release:
		return nil, nil, errors.New("collection stopped after overlap test")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}
