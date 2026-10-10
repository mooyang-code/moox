package console

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestInitConsoleServices_NilConfig_ShouldError(t *testing.T) {
	SetConfig(nil)
	err := InitConsoleServices(&server.Server{}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "控制台配置未初始化")
}
