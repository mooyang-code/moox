package marketfetch

import (
	"testing"
	"time"

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

func TestAttemptTimeoutFloorReadsRequestLimitsOnly(t *testing.T) {
	require.Zero(t, attemptTimeoutFloor(nil))
	require.Zero(t, attemptTimeoutFloor(&FeedLimits{}))
	require.Equal(t, 3*time.Second, attemptTimeoutFloor(&FeedLimits{RequestTimeoutMS: 3000}))
}
