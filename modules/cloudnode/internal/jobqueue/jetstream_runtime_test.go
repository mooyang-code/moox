package jobqueue

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntime_NilSafeMethods(t *testing.T) {
	var rt *Runtime
	assert.Nil(t, rt.Client())
	assert.Nil(t, rt.JetStream())

	err := rt.EnsureStreams(config.JetStreamConfig{}, config.JobItemConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")

	_, err = rt.KeyValue("bucket")
	require.Error(t, err)

	assert.NoError(t, rt.Close())
}

func TestRuntime_CloseInvokesHook(t *testing.T) {
	rt := &Runtime{}
	hookErr := errors.New("hook failed")
	rt.SetCloseHook(func() error { return hookErr })
	// client is nil, Close returns nil without calling hook on nil client path
	assert.NoError(t, rt.Close())
}

func TestContains_MatchesTarget(t *testing.T) {
	assert.True(t, contains([]string{"a", "b"}, "b"))
	assert.False(t, contains([]string{"a"}, "c"))
	assert.False(t, contains(nil, "a"))
}

func TestNewJetStreamQueue_AppliesDefaults(t *testing.T) {
	q := NewJetStreamQueue(nil, QueueConfig{})
	assert.Equal(t, DefaultExecStream, q.cfg.ExecStream)
	assert.Equal(t, 2*time.Minute, q.cfg.AckWait)
	assert.Equal(t, 3, q.cfg.MaxDeliver)
	assert.Equal(t, defaultFetchMaxWait, q.cfg.FetchMaxWait)
	assert.Equal(t, 10, q.cfg.DefaultMaxBatch)
}
