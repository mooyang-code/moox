package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartBackgroundServices_ShouldInitializeDNSProxy(t *testing.T) {
	services, err := StartBackgroundServices(context.Background())
	require.NoError(t, err)
	require.NotNil(t, services)
}
