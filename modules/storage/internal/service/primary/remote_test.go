package primary

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetInfoErrorReturnsNilOnSuccess(t *testing.T) {
	require.NoError(t, retInfoError(&pb.RetInfo{Code: pb.ErrorCode_SUCCESS}))
}

func TestRetInfoErrorReturnsErrorOnFailure(t *testing.T) {
	err := retInfoError(&pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "bad"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INVALID_PARAM")
}

func TestRemoteClientOptionsUsesServiceName(t *testing.T) {
	opts := remoteClientOptions("moox-primary", "")
	require.Len(t, opts, 1)
}

func TestRemoteClientOptionsUsesIPTarget(t *testing.T) {
	opts := remoteClientOptions("moox-primary", "127.0.0.1:8080")
	require.Len(t, opts, 2)
}

func TestRemoteClientOptionsUsesURLTarget(t *testing.T) {
	opts := remoteClientOptions("moox-primary", "ip://127.0.0.1:8080")
	require.Len(t, opts, 2)
}

func TestRemoteClientProxyForCachesByEndpoint(t *testing.T) {
	c := NewRemoteClient("moox-primary")
	a := c.proxyFor(&pb.PrimaryStoreTarget{Endpoint: "127.0.0.1:8080"})
	b := c.proxyFor(&pb.PrimaryStoreTarget{Endpoint: "127.0.0.1:8080"})
	assert.Equal(t, a, b)
}

func TestRemoteClientOptionsLocalEndpointSkipsTarget(t *testing.T) {
	opts := remoteClientOptions("moox-primary", "local")
	assert.Len(t, opts, 1)
}
