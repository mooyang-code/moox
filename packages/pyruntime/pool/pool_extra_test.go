package pool

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolNewClampsToOne(t *testing.T) {
	p := New(0, func(context.Context) (process.Worker, error) {
		return &stubWorker{state: process.StateReady}, nil
	})
	require.NotNil(t, p)
	assert.Len(t, p.workers, 1)
	require.NoError(t, p.Close())
}

func TestPoolPickEmptyPool(t *testing.T) {
	var p *Pool
	_, err := p.pick("x")
	require.Error(t, err)

	empty := &Pool{}
	_, err = empty.Run(context.Background(), Request{Run: process.RunRequest{RequestID: "r"}})
	require.Error(t, err)
	_, err = empty.RunLoaded(context.Background(), "s", process.LoadRequest{}, process.RunRequest{})
	require.Error(t, err)
	_, err = empty.RunLoadedMany(context.Background(), "s", nil, process.RunRequest{})
	require.Error(t, err)
	assert.NoError(t, empty.Close())
}
