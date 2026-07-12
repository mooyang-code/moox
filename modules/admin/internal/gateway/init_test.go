package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestInitGatewayServices_NilConfig_ShouldError(t *testing.T) {
	SetConfig(nil)
	err := InitGatewayServices(&server.Server{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "网关配置未初始化")
}
