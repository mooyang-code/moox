package marketfetch

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestProviderResponseErrorsRemainRetryableForLaterCandidateWindow(t *testing.T) {
	for _, err := range []error{marketdata.ErrProtocol, marketdata.ErrNoClosedBar, marketdata.ErrHistoryOutOfRange, marketdata.ErrHistoryCoverage} {
		outcome := classifyError(err)
		require.Equal(t, domain.ItemOutcomeProviderError, outcome, "error=%v", err)
		require.True(t, isRetryable(outcome), "error=%v", err)
	}
}

func TestRuntimeKlineBreakerThresholdScalesWithFeedConcurrency(t *testing.T) {
	t.Setenv("MOOX_FETCH_FEED_CONCURRENCY", "")
	require.Equal(t, RuntimeKlineProviderAttemptBudget(), RuntimeKlineBreakerThreshold())
	t.Setenv("MOOX_FETCH_FEED_CONCURRENCY", "50")
	require.Equal(t, 50*RuntimeKlineProviderAttemptBudget(), RuntimeKlineBreakerThreshold())
}
