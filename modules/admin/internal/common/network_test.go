package common

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetInternalIP_ValidNetwork_ShouldReturnIPOrUnknown(t *testing.T) {
	ip := GetInternalIP()
	if ip == "unknown" {
		t.Skip("network unavailable in test environment")
	}
	parsed := net.ParseIP(ip)
	assert.NotNil(t, parsed, "GetInternalIP should return a valid IP or unknown")
}

func TestGetPublicIP_UnreachableOrSuccess_ShouldReturnValue(t *testing.T) {
	ip := GetPublicIP()
	assert.True(t, ip == "unknown" || net.ParseIP(ip) != nil)
}
