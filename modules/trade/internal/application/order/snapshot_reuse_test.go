package order

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestReusedSnapshotLoadsOncePerAccountWithinPass(t *testing.T) {
	loads := 0
	load := func(context.Context) (exchange.AccountSnapshot, error) {
		loads++
		return exchange.AccountSnapshot{}, nil
	}
	ctx := WithSnapshotReuse(context.Background())
	for range 3 {
		_, err := reusedSnapshot(ctx, "a", load)
		require.NoError(t, err)
	}
	_, err := reusedSnapshot(ctx, "b", load)
	require.NoError(t, err)
	require.Equal(t, 2, loads)

	// 没有复用范围时每次都重算。
	_, _ = reusedSnapshot(context.Background(), "a", load)
	_, _ = reusedSnapshot(context.Background(), "a", load)
	require.Equal(t, 4, loads)
}
