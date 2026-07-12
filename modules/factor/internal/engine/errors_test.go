package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRetryableError_UnwrapsUnderlyingError(t *testing.T) {
	root := fmt.Errorf("transient")
	err := retryable("storage down: %s", root.Error())
	var retry RetryableError
	assert.True(t, errors.As(err, &retry))
	assert.Contains(t, err.Error(), "storage down")
	assert.Error(t, retry.Unwrap())
}

func TestNonRetryableError_UnwrapsUnderlyingError(t *testing.T) {
	root := fmt.Errorf("bad input")
	err := nonRetryable("invalid factor: %s", root.Error())
	var nerr NonRetryableError
	assert.True(t, errors.As(err, &nerr))
	assert.Error(t, nerr.Unwrap())
}
