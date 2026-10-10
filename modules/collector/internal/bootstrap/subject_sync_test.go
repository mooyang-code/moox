package bootstrap

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestSubjectSyncTimerConfig(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	require.NoError(t, err)
	var config struct {
		Server struct {
			Services []struct {
				Name, Network, Protocol string
				Timeout                 int
			} `yaml:"service"`
		}
	}
	require.NoError(t, yaml.Unmarshal(raw, &config))
	want := map[string]string{subjectTagsTimerService: "30 * * * * *", subjectAttributesTimerService: "45 * * * * *"}
	for _, service := range config.Server.Services {
		if schedule, ok := want[service.Name]; ok {
			require.Equal(t, schedule, service.Network)
			require.Equal(t, "timer", service.Protocol)
			require.Equal(t, 600000, service.Timeout)
			delete(want, service.Name)
		}
	}
	require.Empty(t, want)
}

func TestSubjectSyncTimerRejectsMissingServices(t *testing.T) {
	process := newRuntime(context.Background(), nil)
	defer process.Close()
	run := func(context.Context) error { return nil }
	require.ErrorContains(t, registerSubjectSyncTimers(nil, process, run, run), "require a server")
	s := &server.Server{}
	require.ErrorContains(t, registerSubjectSyncTimers(s, process, run, run), subjectTagsTimerService)
	s.AddService(subjectTagsTimerService, server.New(server.WithProtocol("timer")))
	require.ErrorContains(t, registerSubjectSyncTimers(s, process, run, run), subjectAttributesTimerService)
	s.AddService(subjectAttributesTimerService, server.New(server.WithProtocol("timer")))
	require.NoError(t, registerSubjectSyncTimers(s, process, run, run))
}

func TestSubjectSyncTimerSkipsOverlapAndJoinsBeforeClientClose(t *testing.T) {
	s := &server.Server{}
	process := newRuntime(context.Background(), s)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	httpClosed, databaseClosed := make(chan struct{}), make(chan struct{})
	process.beforeClose = []func() error{func() error { close(httpClosed); return nil }}
	process.closeDatabase = func() error { close(databaseClosed); return nil }
	var calls atomic.Int32
	job, err := subjectSyncJob("collector_subject_overlap_test", process, func(ctx context.Context) error {
		calls.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > subjectSyncTimeout || time.Until(deadline) < 9*time.Minute {
			t.Error("subject sync timer has no bounded deadline")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	})
	require.NoError(t, err)
	finished := make(chan error, 1)
	go func() { finished <- job.Handle(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timer did not start")
	}
	require.NoError(t, job.Handle(context.Background()), "next minute skips a still-running invocation")
	require.Equal(t, int32(1), calls.Load())
	// A slow tag sync must leave the independent periodic planner available.
	planned := false
	require.NoError(t, process.run(context.Background(), func(context.Context) error { planned = true; return nil }))
	require.True(t, planned)
	closed := make(chan error, 1)
	go func() {
		_ = s.Close(nil)
		closed <- process.Close()
	}()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the timer")
	}
	select {
	case <-httpClosed:
		t.Fatal("HTTP closed before timer joined")
	case <-databaseClosed:
		t.Fatal("database closed before timer joined")
	default:
	}
	close(release)
	require.ErrorIs(t, <-finished, context.Canceled)
	require.NoError(t, <-closed)
	<-httpClosed
	<-databaseClosed
	require.ErrorIs(t, job.Handle(context.Background()), context.Canceled)
	require.Equal(t, int32(1), calls.Load(), "no callbacks after shutdown")
}
