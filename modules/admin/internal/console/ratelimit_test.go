package console

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMethodLimitersDefaultsAndConfigOverrides(t *testing.T) {
	limiters := newMethodLimiters(nil)
	assert.Contains(t, limiters, "/api/admin/auth/Login")
	assert.Equal(t, 2, limiters["/api/admin/auth/Login"].Burst())

	limiters = newMethodLimiters(&Config{RateLimit: RateLimitConfig{MethodLimits: map[string]MethodLimit{"/api/admin/auth/Login": {QPS: 3, Burst: 7}}}})
	assert.Equal(t, 7, limiters["/api/admin/auth/Login"].Burst())
	assert.Contains(t, limiters, "/api/admin/auth/GetUserInfo", "没写的接口用默认限额")
}

func TestMethodLimitersOnlyLimitConfiguredMethods(t *testing.T) {
	limiters := newMethodLimiters(&Config{RateLimit: RateLimitConfig{MethodLimits: map[string]MethodLimit{"/api/admin/auth/Login": {QPS: 1, Burst: 1}}}})
	ctx := context.Background()
	assert.True(t, limiters.allow(ctx, "/api/admin/auth/Login"))
	assert.False(t, limiters.allow(ctx, "/api/admin/auth/Login"))
	for range 50 {
		assert.True(t, limiters.allow(ctx, "/api/admin/monitor/GetHealthOverview"))
	}
}
