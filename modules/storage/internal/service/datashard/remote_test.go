//go:build legacy_storage

package datashard

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
	opts := remoteClientOptions("moox-primary", nil)
	require.Len(t, opts, 1)
}

func TestRemoteClientOptionsDoesNotUsePhysicalEndpoint(t *testing.T) {
	opts := remoteClientOptions("moox-primary", &pb.ShardTarget{Endpoint: "127.0.0.1:8080"})
	require.Len(t, opts, 1)
}

func TestRemoteClientOptionsUsesGatewayTarget(t *testing.T) {
	opts := remoteClientOptions("moox-primary", &pb.ShardTarget{GatewayTarget: "ip://127.0.0.1:11003"})
	require.Greater(t, len(opts), 1)
}

func TestRemoteClientProxyForCachesByEndpoint(t *testing.T) {
	c := NewRemoteClient("moox-primary")
	a := c.proxyFor(&pb.ShardTarget{GatewayTarget: "ip://127.0.0.1:11003", Endpoint: "127.0.0.1:8080"})
	b := c.proxyFor(&pb.ShardTarget{GatewayTarget: "ip://127.0.0.1:11003", Endpoint: "127.0.0.1:8080"})
	assert.Equal(t, a, b)
}

func TestRemoteClientOptionsLocalEndpointSkipsTarget(t *testing.T) {
	opts := remoteClientOptions("moox-primary", &pb.ShardTarget{Endpoint: "local"})
	assert.Len(t, opts, 1)
}

func TestRemoteClientRejectsPhysicalOnlyTarget(t *testing.T) {
	err := NewRemoteClient("moox-primary").WriteRows(nil, &pb.ShardTarget{Endpoint: "127.0.0.1:20106"}, nil)
	require.ErrorContains(t, err, "gateway_target")
}
