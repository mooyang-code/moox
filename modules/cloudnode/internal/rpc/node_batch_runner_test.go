package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/log"
)

func TestNodeBatchRunnerRunsTakenBatchWithTRPCGoAndWait(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-concurrent", 7)
	var active atomic.Int32
	var maxActive atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 7)
	var batchesMu sync.Mutex
	var batchSizes []int
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			current := active.Add(1)
			for {
				previous := maxActive.Load()
				if current <= previous || maxActive.CompareAndSwap(previous, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return "done", nil
		},
		nodeBatchTakenHook: func(items []store.NodeBatchItem) {
			batchesMu.Lock()
			batchSizes = append(batchSizes, len(items))
			batchesMu.Unlock()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 3, 100*time.Millisecond))
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("first batch did not start")
		}
	}
	assert.Equal(t, int32(3), maxActive.Load())
	close(release)
	waitForNodeBatchTerminal(t, catalog, "runner-concurrent")
	assert.Equal(t, int32(3), maxActive.Load())
	batchesMu.Lock()
	assert.Equal(t, []int{3, 3, 1}, batchSizes)
	batchesMu.Unlock()
}

func TestNodeBatchRunnerDoesNotTakeNextBatchUntilCurrentBatchFinishes(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-barrier", 4)
	block := make(chan struct{})
	firstStarted := make(chan struct{})
	var once sync.Once
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(_ context.Context, item store.NodeBatchItem) (string, error) {
			if item.ItemIndex == 0 {
				once.Do(func() { close(firstStarted) })
				<-block
			}
			return "done", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 3, 100*time.Millisecond))
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first batch did not start")
	}
	require.Eventually(t, func() bool {
		aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "runner-barrier")
		return err == nil && aggregate.PendingCount == 1 && aggregate.RunningCount == 1 && aggregate.SuccessCount == 2
	}, time.Second, 10*time.Millisecond)
	close(block)
	waitForNodeBatchTerminal(t, catalog, "runner-barrier")
}

func TestNodeBatchRunnerCompletesOtherItemsAfterOneFailure(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-partial", 3)
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(_ context.Context, item store.NodeBatchItem) (string, error) {
			if item.ItemIndex == 1 {
				return "", errors.New("provider rejected item")
			}
			return fmt.Sprintf("done %s", item.NodeID), nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 3, 100*time.Millisecond))
	aggregate := waitForNodeBatchTerminal(t, catalog, "runner-partial")
	assert.Equal(t, store.NodeBatchPartial, aggregate.Job.Status)
	assert.Equal(t, 2, aggregate.SuccessCount)
	assert.Equal(t, 1, aggregate.FailedCount)
}

func TestNodeBatchRunnerRetriesTransientProviderFailure(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-provider-retry", 1)
	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			if calls.Add(1) < nodeBatchProviderAttempts {
				return "", errors.New("ClientError.NetworkError: TLS handshake timeout")
			}
			return "done after retry", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))

	aggregate := waitForNodeBatchTerminal(t, catalog, "runner-provider-retry")
	assert.Equal(t, store.NodeBatchSuccess, aggregate.Job.Status)
	assert.Equal(t, int32(nodeBatchProviderAttempts), calls.Load())
	assert.Equal(t, "done after retry", aggregate.Items[0].ResultSummary)
}

func TestNodeBatchRunnerDoesNotRetryPermanentProviderFailure(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-provider-permanent", 1)
	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			calls.Add(1)
			return "", errors.New("LimitExceeded.Function: function quota reached")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))

	aggregate := waitForNodeBatchTerminal(t, catalog, "runner-provider-permanent")
	assert.Equal(t, store.NodeBatchFailed, aggregate.Job.Status)
	assert.Equal(t, int32(1), calls.Load())
}

func TestNodeBatchRunnerStopsWithRuntimeContext(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-stopped", 1)
	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			calls.Add(1)
			return "done", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))
	time.Sleep(30 * time.Millisecond)
	assert.Zero(t, calls.Load())
	aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "runner-stopped")
	require.NoError(t, err)
	assert.Equal(t, 1, aggregate.PendingCount)
}

func TestNodeBatchRunnerLeavesRunningItemForStartupRecoveryWhenCanceledDuringExecution(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-canceled-active", 1)
	started := make(chan struct{})
	returned := make(chan struct{})
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(ctx context.Context, _ store.NodeBatchItem) (string, error) {
			close(started)
			<-ctx.Done()
			close(returned)
			return "", ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("node batch item did not start")
	}

	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("node batch executor did not observe runtime cancellation")
	}

	require.Never(t, func() bool {
		aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "runner-canceled-active")
		return err != nil || aggregate == nil ||
			aggregate.PendingCount != 0 || aggregate.RunningCount != 1 ||
			aggregate.SuccessCount != 0 || aggregate.FailedCount != 0
	}, 250*time.Millisecond, 10*time.Millisecond)
}

func TestNodeBatchRunnerRequeuesInterruptedItemsAtStartup(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-requeue", 1)
	taken, err := catalog.TakePendingNodeBatchItems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := &Service{catalog: catalog}

	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, time.Second))

	aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "runner-requeue")
	require.NoError(t, err)
	assert.Equal(t, store.NodeBatchPending, aggregate.Job.Status)
	assert.Equal(t, 1, aggregate.PendingCount)
	assert.Nil(t, aggregate.Items[0].StartedAt)
}

func TestNodeBatchRunnerNeverLogsRequestPayload(t *testing.T) {
	var logs bytes.Buffer
	original := log.GetDefaultLogger()
	log.SetLogger(&bufferLogger{out: &logs})
	t.Cleanup(func() { log.SetLogger(original) })

	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	raw, err := protojson.Marshal(&pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "pkg-a",
		Environment: map[string]string{"SECRET": "must-not-appear-in-logs"},
	})
	require.NoError(t, err)
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: "runner-log-redaction", Operation: nodeBatchOperationDeploy,
		Items: []store.NodeBatchItemCreate{{
			ItemID: "item-0", NodeID: "node-a", RequestJSON: string(raw),
		}},
	}))
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			return "", errors.New("provider failed")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))
	waitForNodeBatchTerminal(t, catalog, "runner-log-redaction")
	assert.NotContains(t, logs.String(), "must-not-appear-in-logs")
	assert.NotContains(t, logs.String(), `"SECRET"`)
}

func createRunnerBatch(t *testing.T, catalog *store.CatalogRepository, jobID string, count int) {
	t.Helper()
	items := make([]store.NodeBatchItemCreate, 0, count)
	for index := range count {
		raw, err := protojson.Marshal(&pb.NodeCreateItem{Region: "local", PackageId: fmt.Sprintf("pkg-%d", index)})
		require.NoError(t, err)
		items = append(items, store.NodeBatchItemCreate{
			ItemID: fmt.Sprintf("%s-%03d", jobID, index), ItemIndex: index,
			NodeID: fmt.Sprintf("node-%03d", index), RequestJSON: string(raw),
		})
	}
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: jobID, Operation: nodeBatchOperationCreate, Items: items,
	}))
}

func waitForNodeBatchTerminal(t *testing.T, catalog *store.CatalogRepository, jobID string) *store.NodeBatchAggregate {
	t.Helper()
	var aggregate *store.NodeBatchAggregate
	require.Eventually(t, func() bool {
		var err error
		aggregate, err = catalog.GetNodeBatch(context.Background(), "crypto", jobID)
		return err == nil && aggregate != nil &&
			aggregate.PendingCount == 0 && aggregate.RunningCount == 0
	}, 3*time.Second, 10*time.Millisecond)
	return aggregate
}

type bufferLogger struct {
	out *bytes.Buffer
}

func (l *bufferLogger) write(args ...any) { _, _ = fmt.Fprintln(l.out, args...) }
func (l *bufferLogger) writef(format string, args ...any) {
	_, _ = fmt.Fprintf(l.out, format+"\n", args...)
}
func (l *bufferLogger) Trace(args ...any)                 { l.write(args...) }
func (l *bufferLogger) Tracef(format string, args ...any) { l.writef(format, args...) }
func (l *bufferLogger) Debug(args ...any)                 { l.write(args...) }
func (l *bufferLogger) Debugf(format string, args ...any) { l.writef(format, args...) }
func (l *bufferLogger) Info(args ...any)                  { l.write(args...) }
func (l *bufferLogger) Infof(format string, args ...any)  { l.writef(format, args...) }
func (l *bufferLogger) Warn(args ...any)                  { l.write(args...) }
func (l *bufferLogger) Warnf(format string, args ...any)  { l.writef(format, args...) }
func (l *bufferLogger) Error(args ...any)                 { l.write(args...) }
func (l *bufferLogger) Errorf(format string, args ...any) { l.writef(format, args...) }
func (l *bufferLogger) Fatal(args ...any)                 { l.write(args...) }
func (l *bufferLogger) Fatalf(format string, args ...any) { l.writef(format, args...) }
func (l *bufferLogger) Sync() error                       { return nil }
func (l *bufferLogger) SetLevel(string, log.Level)        {}
func (l *bufferLogger) GetLevel(string) log.Level         { return log.LevelDebug }
func (l *bufferLogger) With(...log.Field) log.Logger      { return l }
