package pebble

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 并发写入时，outbox 事件编号与变更日志序号各自连续，并且彼此顺序一致：编号小的写入，变更序号也小。
// 事件中继按编号顺序发布、因子引擎按变更序号读取，两者顺序不一致会让读者越过尚未提交的写入。
func TestConcurrentWritesKeepOutboxIDsAndChangeSequencesInCommitOrder(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const writers, perWriter = 16, 8

	var mu sync.Mutex
	outboxID := map[string]uint64{}
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				subject := fmt.Sprintf("S%02d-%02d", w, i)
				entries, err := upsertTestRowsEntries(s, ctx, []*pb.RowFieldUpsert{tsRow(subject, "", base.Add(time.Duration(i)*time.Minute), float64(i))})
				if err != nil || len(entries) != 1 {
					t.Errorf("写入 %s: entries=%d err=%v", subject, len(entries), err)
					return
				}
				mu.Lock()
				outboxID[subject] = entries[0].ID
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	require.Len(t, outboxID, writers*perWriter)

	// outbox 编号恰好是 1..N。
	entries, err := s.ListOutbox(ctx, 0, writers*perWriter+10)
	require.NoError(t, err)
	require.Len(t, entries, writers*perWriter)
	for i, entry := range entries {
		require.Equal(t, uint64(i+1), entry.ID, "outbox 编号必须连续")
	}

	// 变更序号恰好是 1..N，且每条变更对应的标的与 outbox 编号同序。
	got, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: &pb.DatasetChangeCursor{NodeId: "node-1", StoreId: s.sourceStoreID}, Limit: 2000})
	require.NoError(t, err)
	require.False(t, got.GetGap())
	require.Len(t, got.GetChanges(), writers*perWriter)
	for i, change := range got.GetChanges() {
		require.Equal(t, uint64(i+1), change.GetSequence(), "变更序号必须连续")
		subject := change.GetRanges()[0].GetSubjectId()
		require.Equal(t, uint64(i+1), outboxID[subject], "第 %d 条变更（%s）的 outbox 编号必须与变更序号一致", i+1, subject)
	}
}

// 预处理完成后在等 outboxMu 期间请求被取消：拿到锁后不再提交，不留下 outbox 事件与变更日志，
// 否则调用方超时重试会写出重复事件。
func TestWriteCanceledWhileWaitingForLockIsNotCommitted(t *testing.T) {
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	s.outboxMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := upsertTestRowsEntries(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base, 1)})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // 让写入完成预处理并阻塞在 outboxMu 上
	cancel()
	s.outboxMu.Unlock()

	require.ErrorIs(t, <-done, context.Canceled)
	entries, err := s.ListOutbox(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Empty(t, entries)
	head, err := s.ChangeCursor()
	require.NoError(t, err)
	require.Equal(t, uint64(0), head.GetSequence())
}
