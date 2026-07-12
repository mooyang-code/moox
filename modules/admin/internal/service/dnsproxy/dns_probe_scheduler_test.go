package dnsproxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandleDNSProbeSchedule_NoDomains_ShouldPass(t *testing.T) {
	SetConfig(&Config{DNSProxy: DNSProxyConfig{Domains: nil}})
	err := HandleDNSProbeSchedule(context.Background(), "")
	require.NoError(t, err)
}
