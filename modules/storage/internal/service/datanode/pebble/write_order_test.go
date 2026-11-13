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

// 并发写入时，变更日志序号连续、每次写入一条：View 与因子机按序号跟随，序号有洞或重复会让读者越过或重复应用写入。
func TestConcurrentWritesKeepChangeSequencesContiguous(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const writers, perWriter = 16, 8

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				subject := fmt.Sprintf("S%02d-%02d", w, i)
				if err := upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow(subject, "", base.Add(time.Duration(i)*time.Minute), float64(i))}); err != nil {
					t.Errorf("写入 %s: %v", subject, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// 变更序号恰好是 1..N，每次写入一条。
	got, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: &pb.DatasetChangeCursor{StoreId: s.sourceStoreID}, Limit: 2000})
	require.NoError(t, err)
	require.False(t, got.GetGap())
	require.Len(t, got.GetChanges(), writers*perWriter)
	subjects := map[string]struct{}{}
	for i, change := range got.GetChanges() {
		require.Equal(t, uint64(i+1), change.GetSequence(), "变更序号必须连续")
		subjects[change.GetRanges()[0].GetSubjectId()] = struct{}{}
	}
	require.Len(t, subjects, writers*perWriter)
}

// 预处理完成后在等 outboxMu 期间请求被取消：拿到锁后不再提交，不留下变更日志，否则调用方超时重试会重复写入。
func TestWriteCanceledWhileWaitingForLockIsNotCommitted(t *testing.T) {
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	s.outboxMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base, 1)})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // 让写入完成预处理并阻塞在 outboxMu 上
	cancel()
	s.outboxMu.Unlock()

	require.ErrorIs(t, <-done, context.Canceled)
	head, err := s.ChangeCursor()
	require.NoError(t, err)
	require.Equal(t, uint64(0), head.GetSequence())
}
