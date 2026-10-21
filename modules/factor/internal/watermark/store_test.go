package watermark

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStorePersistsStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine", "watermarks.json")
	store, err := Open(path)
	require.NoError(t, err)
	_, ok := store.Get("s")
	require.False(t, ok)
	at := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	require.NoError(t, store.Put(State{SetID: "s", CommittedThrough: at, Target: at.Add(time.Minute), Universe: []string{"A"}}))
	reopened, err := Open(path)
	require.NoError(t, err)
	state, ok := reopened.Get("s")
	require.True(t, ok)
	require.True(t, state.Behind())
	require.Equal(t, []string{"A"}, state.Universe)
	require.Len(t, reopened.All(), 1)
}
