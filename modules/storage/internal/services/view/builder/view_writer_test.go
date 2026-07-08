package builder

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestViewWriterSerializesWritesPerView(t *testing.T) {
	ctx := context.Background()
	sink := &serializingSink{}
	pool := newViewWriterPool(sink)
	defer pool.close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := pool.insert(ctx, "active_spot_view", []*pb.TimeSeriesRow{
				builderTestTSRow("crypto", "kline", "BTC-USDT", "2026-07-08T10:00:00Z", builderTestValue("close", 1)),
			}); err != nil {
				t.Errorf("insert: %v", err)
			}
		}()
	}
	wg.Wait()

	if sink.maxActive != 1 {
		t.Fatalf("max concurrent writes = %d, want 1", sink.maxActive)
	}
	if sink.writes != 8 {
		t.Fatalf("writes = %d, want 8", sink.writes)
	}
}

type serializingSink struct {
	mu        sync.Mutex
	active    int
	maxActive int
	writes    int
}

func (s *serializingSink) InsertRows(context.Context, string, []*pb.TimeSeriesRow) error {
	s.mu.Lock()
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()

	time.Sleep(5 * time.Millisecond)

	s.mu.Lock()
	s.active--
	s.writes++
	s.mu.Unlock()
	return nil
}
