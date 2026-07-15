package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubAdminServiceProvider struct {
	details map[string]ServiceDetail
}

func (p stubAdminServiceProvider) ResolveAdminServiceDetail(_ context.Context, adminNodeID, serviceID string) (ServiceDetail, bool) {
	detail, ok := p.details[adminNodeID+":"+serviceID]
	return detail, ok
}

func TestResolveAdminServiceDetailUsesExplicitNode(t *testing.T) {
	provider := stubAdminServiceProvider{details: map[string]ServiceDetail{
		"node-a:auth": {Address: "127.0.0.1:8080", Path: "trpc.moox.infra.Auth"},
	}}

	detail, err := resolveAdminServiceDetail(context.Background(), provider, "node-a", "auth")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:8080", detail.Address)
	assert.Equal(t, "trpc.moox.infra.Auth", detail.Path)

	_, err = resolveAdminServiceDetail(context.Background(), provider, "node-b", "auth")
	require.Error(t, err)
}

func TestResolveAdminServiceDetailMissingProviderErrors(t *testing.T) {
	_, err := resolveAdminServiceDetail(context.Background(), nil, "node-a", "auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node-a")
}
