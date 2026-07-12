package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerPoolNilGuards(t *testing.T) {
	var p *WorkerPool
	_, err := p.Execute(context.Background(), &FactorTask{}, &DataFrame{})
	require.Error(t, err)
	assert.Equal(t, WorkerPoolStatus{}, p.Status())
	assert.NoError(t, p.Close())

	empty := &WorkerPool{}
	_, err = empty.Execute(context.Background(), &FactorTask{}, &DataFrame{})
	require.Error(t, err)
	assert.Equal(t, 0, empty.Status().Workers)
	assert.NoError(t, empty.Close())
}

func TestRuntimeExecutorNilGuards(t *testing.T) {
	var e *RuntimeExecutor
	assert.Equal(t, WorkerPoolStatus{}, e.Status())
	assert.NoError(t, e.Close())
}

func TestWorkerPoolExecuteRotatesWorkers(t *testing.T) {
	workers := []*fakeWorker{{result: &FactorResult{ElapsedMS: 1}}, {result: &FactorResult{ElapsedMS: 2}}}
	pool := &WorkerPool{workers: []Executor{workers[0], workers[1]}}

	first, err := pool.Execute(context.Background(), &FactorTask{TaskID: "task-1"}, &DataFrame{})
	require.NoError(t, err)
	second, err := pool.Execute(context.Background(), &FactorTask{TaskID: "task-2"}, &DataFrame{})
	require.NoError(t, err)
	third, err := pool.Execute(context.Background(), &FactorTask{TaskID: "task-3"}, &DataFrame{})
	require.NoError(t, err)

	assert.Same(t, workers[0].result, first)
	assert.Same(t, workers[1].result, second)
	assert.Same(t, workers[0].result, third)
	assert.Equal(t, 2, pool.Status().Workers)
	assert.Equal(t, uint64(3), pool.Status().Next)
	assert.Equal(t, 2, workers[0].calls)
	assert.Equal(t, 1, workers[1].calls)
}

func TestWorkerPoolCloseReturnsFirstErrorAndClosesAll(t *testing.T) {
	firstErr := errors.New("first close")
	pool := &WorkerPool{workers: []Executor{
		&fakeWorker{closeErr: firstErr},
		&fakeWorker{closeErr: errors.New("second close")},
		&fakeWorker{},
	}}

	err := pool.Close()

	require.ErrorIs(t, err, firstErr)
	for _, worker := range pool.workers {
		assert.True(t, worker.(*fakeWorker).closed)
	}
}

type fakeWorker struct {
	result   *FactorResult
	closeErr error
	calls    int
	closed   bool
}

func (f *fakeWorker) Execute(context.Context, *FactorTask, *DataFrame) (*FactorResult, error) {
	f.calls++
	return f.result, nil
}

func (f *fakeWorker) Close() error {
	f.closed = true
	return f.closeErr
}
