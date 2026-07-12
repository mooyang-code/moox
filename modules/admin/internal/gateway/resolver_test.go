package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveServiceDetail_WithResolver_ShouldReturnDetail(t *testing.T) {
	SetServiceDetailResolver(func(ctx context.Context, serviceID string) (ServiceDetail, bool) {
		if serviceID == "auth" {
			return ServiceDetail{Address: "127.0.0.1:8080", Path: "trpc.moox.infra.Auth"}, true
		}
		return ServiceDetail{}, false
	})
	t.Cleanup(func() { SetServiceDetailResolver(nil) })

	detail, err := resolveServiceDetail(context.Background(), "auth")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:8080", detail.Address)
	assert.Equal(t, "trpc.moox.infra.Auth", detail.Path)
}

func TestResolveServiceDetail_MissingDeployment_ShouldError(t *testing.T) {
	SetServiceDetailResolver(func(ctx context.Context, serviceID string) (ServiceDetail, bool) {
		return ServiceDetail{}, false
	})
	t.Cleanup(func() { SetServiceDetailResolver(nil) })

	_, err := resolveServiceDetail(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestResolveServiceDetail_NilResolver_ShouldError(t *testing.T) {
	SetServiceDetailResolver(nil)
	_, err := resolveServiceDetail(context.Background(), "auth")
	require.Error(t, err)
}
