package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRetryableError_UnwrapsUnderlyingError(t *testing.T) {
	root := fmt.Errorf("transient")
	err := RetryableError{Err: fmt.Errorf("storage down: %w", root)}
	var retry RetryableError
	assert.True(t, errors.As(err, &retry))
	assert.Contains(t, err.Error(), "storage down")
	assert.Error(t, retry.Unwrap())
}

func TestNonRetryableError_UnwrapsUnderlyingError(t *testing.T) {
	root := fmt.Errorf("bad input")
	err := NonRetryableError{Err: fmt.Errorf("invalid factor: %w", root)}
	var nerr NonRetryableError
	assert.True(t, errors.As(err, &nerr))
	assert.Error(t, nerr.Unwrap())
}
