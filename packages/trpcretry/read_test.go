package trpcretry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

func TestReadOnlyBoundsNetworkRetries(t *testing.T) {
	attempts := 0
	err := ReadOnly()(context.Background(), nil, nil, func(context.Context, interface{}, interface{}) error {
		attempts++
		return errs.New(errs.RetClientNetErr, "network unavailable")
	})
	require.Error(t, err)
	require.Equal(t, 2, attempts)
}

func TestReadOnlyDoesNotRetryBusinessErrors(t *testing.T) {
	attempts := 0
	err := ReadOnly()(context.Background(), nil, nil, filter.ClientHandleFunc(func(context.Context, interface{}, interface{}) error {
		attempts++
		return errs.New(100101, "invalid request")
	}))
	require.Error(t, err)
	require.Equal(t, 1, attempts)
}
