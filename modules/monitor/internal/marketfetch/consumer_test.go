package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStoreIgnoresE2EDataset(t *testing.T) {
	store := NewStore()
	store.Observe("crypto", &marketfetchpb.MarketFetchBatchCompleted{
		DatasetId:   "e2e_binance_symbols",
		Frequency:   "1m",
		CompletedAt: timestamppb.New(time.Now().UTC()),
	})

	require.Zero(t, store.Count())
}

func TestStoreKeepsProductionDataset(t *testing.T) {
	store := NewStore()
	store.Observe("crypto", &marketfetchpb.MarketFetchBatchCompleted{
		DatasetId:   "binance_symbols",
		Frequency:   "1m",
		CompletedAt: timestamppb.New(time.Now().UTC()),
	})

	require.Equal(t, 1, store.Count())
}
