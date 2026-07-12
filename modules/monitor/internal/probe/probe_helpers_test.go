package probe

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiRunnerUnsupportedKind(t *testing.T) {
	r := DefaultRunner()
	got := r.Run(context.Background(), domain.Check{Kind: "udp", CheckID: "c1", SpaceID: "s1"})
	assert.False(t, got.Success)
	assert.Equal(t, domain.CheckStatusDown, got.Status)
	assert.Contains(t, got.ErrorMessage, "unsupported check kind")
	assert.NotEmpty(t, got.ResultID)
}

func TestCheckTimeoutDefaults(t *testing.T) {
	assert.Equal(t, 3*time.Second, checkTimeout(domain.Check{}))
	assert.Equal(t, 1500*time.Millisecond, checkTimeout(domain.Check{TimeoutMS: 1500}))
}

func TestFailResult(t *testing.T) {
	got := failResult(domain.Check{CheckID: "c", SpaceID: "s"}, 12*time.Millisecond, "boom")
	assert.False(t, got.Success)
	assert.Equal(t, "boom", got.ErrorMessage)
	assert.Equal(t, int64(12), got.LatencyMS)
	require.NotEmpty(t, got.ResultID)
}
