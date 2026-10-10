package gatewayclient

import (
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	trpc "trpc.group/trpc-go/trpc-go"
)

// tRPC 的单帧上限必须不小于组件目录里声明的最大请求体，否则目录里的 max_body_bytes 在 tRPC 层就被拒绝。
func TestFrameLimitCoversDeclaredBodyLimits(t *testing.T) {
	largest := int64(0)
	for _, component := range servicecatalog.Default().Components {
		for _, service := range component.Services {
			largest = max(largest, service.MaxBodyBytes)
		}
	}
	require.NotZero(t, largest, "目录里应当声明了较大的请求体上限")
	require.GreaterOrEqual(t, int64(trpc.DefaultMaxFrameSize), largest)
}
