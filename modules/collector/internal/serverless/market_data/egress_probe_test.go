package marketdata

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/stretchr/testify/require"
)

// 出口探测的方式取市场的 market.yaml；未登记的空间直接拒绝，不再默认去探测币安。
func TestEgressProbeRejectsUnregisteredSpace(t *testing.T) {
	raw, err := json.Marshal(model.CloudFunctionEvent{Action: model.EventActionEgressProbe, RequestID: "probe"})
	require.NoError(t, err)
	response, err := Execute(context.Background(), raw, Env{SpaceID: "no_such_space", Handler: &marketfetch.Handler{}})
	require.NoError(t, err)
	require.False(t, response.Success)
	require.Contains(t, response.Message, "invalid_space")
}
