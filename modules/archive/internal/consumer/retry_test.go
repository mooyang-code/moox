package consumer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryDelay(t *testing.T) {
	assert.Equal(t, time.Second, retryDelay(0))
	assert.Equal(t, 2*time.Second, retryDelay(2))
	assert.Equal(t, 30*time.Second, retryDelay(100))
}

func TestRetryScheduledError(t *testing.T) {
	err := &RetryScheduledError{Delay: 2 * time.Second}
	assert.Contains(t, err.Error(), "2s")
}
