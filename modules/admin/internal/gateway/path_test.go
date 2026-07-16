package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsAdminAPIPath_ValidPrefix_ShouldReturnTrue(t *testing.T) {
	assert.True(t, IsAdminAPIPath("/api/admin/auth/login"))
	assert.False(t, IsAdminAPIPath("/api/service/trade/place"))
	assert.False(t, IsAdminAPIPath("/healthz"))
}
