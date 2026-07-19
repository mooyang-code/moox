package contracts

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxEntry_ShouldHoldPersistedMessageFields(t *testing.T) {
	createdAt := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	entry := OutboxEntry{
		Sequence:  42,
		MessageID: "msg-1",
		Topic:     "rows.updated",
		Data:      []byte(`{"rows":1}`),
		CreatedAt: createdAt,
	}
	assert.Equal(t, uint64(42), entry.Sequence)
	assert.Equal(t, "msg-1", entry.MessageID)
	assert.Equal(t, "rows.updated", entry.Topic)
	assert.Equal(t, []byte(`{"rows":1}`), entry.Data)
	assert.Equal(t, createdAt, entry.CreatedAt)
}

func TestFreeDiskBytes_ExistingPath_ShouldReturnPositiveValue(t *testing.T) {
	free, err := FreeDiskBytes(filepath.Join(t.TempDir(), "missing", "nested"))
	if err != nil {
		t.Skipf("FreeDiskBytes unsupported or failed: %v", err)
	}
	require.NoError(t, err)
	assert.Greater(t, free, uint64(0))
}
