package httpclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDNSHelpers_ShouldPreferAvailableIPs(t *testing.T) {
	Init()
	updateDNSRecords([]*DNSRecord{{
		Domain: "api.example.com",
		IPList: []*IPInfo{
			{IP: "1.1.1.1", Available: false, Latency: 10},
			{IP: "2.2.2.2", Available: true, Latency: 5, LastPing: time.Now()},
			{IP: "3.3.3.3", Available: true, Latency: 8},
		},
		Success: true, ResolveAt: time.Now(),
	}})

	assert.Equal(t, "2.2.2.2", GetBestIP("api.example.com"))
	assert.Equal(t, []string{"2.2.2.2", "3.3.3.3"}, GetAvailableIPs("api.example.com"))
	assert.Equal(t, "3.3.3.3", GetNextAvailableIP("api.example.com", []string{"2.2.2.2"}))
	assert.Equal(t, "", GetBestIP("missing.example.com"))

	rec := GetDNSRecord("api.example.com")
	assert.NotNil(t, rec)
	assert.True(t, rec.Success)

	all := GetAllDNSRecords()
	assert.Contains(t, all, "api.example.com")
}
