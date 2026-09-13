package taskrunner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestDatasetWindowPythonErrorDoesNotWrite(t *testing.T) {
	base := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk([]time.Time{base})}}
	exec := &fakeExecutor{err: engine.NonRetryableError{Err: errors.New("python compute failed")}}
	runner := NewService(1, storage, exec)

	require.ErrorContains(t, runner.Run(context.Background(), oneBarTask("BTC-USDT", base)), "python compute failed")
	require.Equal(t, 1, exec.callCount())
	require.Empty(t, storage.writeSizes)
}
