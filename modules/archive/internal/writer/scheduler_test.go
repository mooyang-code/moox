package writer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSchedulerRequiresWriter(t *testing.T) {
	s := Scheduler{}
	require.Error(t, s.MaterializeOnce(context.Background()))
	require.Error(t, s.FlushOnShutdown(context.Background()))
}

func TestSchedulerHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := Scheduler{Writer: &Writer{}, PendingRows: 10, DedupeRetention: time.Hour}
	require.ErrorIs(t, s.MaterializeOnce(ctx), context.Canceled)
	require.ErrorIs(t, s.FlushOnShutdown(ctx), context.Canceled)
}
