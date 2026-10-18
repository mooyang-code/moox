package storageio

import (
	"context"
	"errors"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type unexpectedGatewayCall struct{ called bool }

func (g *unexpectedGatewayCall) Invoke(context.Context, string, string, any, any) error {
	g.called = true
	return errors.New("unexpected network call")
}

func TestGatewayStorageRejectsSDKTargetOverridesBeforeNetwork(t *testing.T) {
	gateway := &unexpectedGatewayCall{}
	storage := NewGatewayClient(gateway, nil)
	_, err := storage.metadata.GetDataset(t.Context(), &storagepb.GetDatasetReq{}, client.WithTarget("ip://192.0.2.99:20100"))
	require.ErrorContains(t, err, "instead of tRPC client options")
	_, err = storage.primary.ReportFactorPeriodComputed(t.Context(), &storagepb.ReportFactorPeriodComputedReq{}, client.WithTimeout(1))
	require.ErrorContains(t, err, "instead of tRPC client options")
	require.False(t, gateway.called)
}
