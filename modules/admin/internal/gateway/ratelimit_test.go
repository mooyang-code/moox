package gateway

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDefaultRateLimitConfig_ShouldContainLoginLimit(t *testing.T) {
	cfg := getDefaultRateLimitConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, 10, cfg.DefaultQPS)
	assert.Equal(t, 2, cfg.MethodLimits["/api/admin/auth/Login"].Burst)
}

func TestLoadRateLimitConfig_NilGatewayConfig_ShouldUseDefaults(t *testing.T) {
	SetConfig(nil)
	cfg := loadRateLimitConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, 10, cfg.DefaultQPS)
}

func TestNewRateLimitManager_CheckRateLimit_ShouldAllowWithinBurst(t *testing.T) {
	manager := newRateLimitManager(getDefaultRateLimitConfig())
	assert.True(t, manager.checkRateLimit(context.Background(), "/api/admin/unknown"))
}

func TestCreateRateLimitResponse_ShouldReturnInnerErr(t *testing.T) {
	resp := createRateLimitResponse().(*middlewareResp)
	require.NotNil(t, resp.RetInfo)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, resp.RetInfo.Code)
	assert.Contains(t, resp.RetInfo.Msg, "请求过于频繁")
}

func TestLoadRateLimitConfig_WithPartialConfig_ShouldBackfillDefaults(t *testing.T) {
	SetConfig(&Config{RateLimit: RateLimitConfig{DefaultQPS: 20}})
	cfg := loadRateLimitConfig()
	assert.Equal(t, 20, cfg.DefaultQPS)
	assert.Equal(t, 20, cfg.DefaultBurst)
	assert.Contains(t, cfg.MethodLimits, "/api/admin/auth/Login")
}
