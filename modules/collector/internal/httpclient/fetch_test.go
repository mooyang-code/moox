package httpclient

import (
	"context"
	"testing"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetScheduledDomains_NoDNSProxy_ReturnsEmpty(t *testing.T) {
	old := runtimeapp.LocalAppConfig
	t.Cleanup(func() { runtimeapp.LocalAppConfig = old })
	runtimeapp.LocalAppConfig = &runtimeapp.AppConfig{}

	assert.Nil(t, getScheduledDomains())
}

func TestFetchDNSRecords_NoDomains_ReturnsNil(t *testing.T) {
	old := runtimeapp.LocalAppConfig
	t.Cleanup(func() { runtimeapp.LocalAppConfig = old })
	runtimeapp.LocalAppConfig = &runtimeapp.AppConfig{}

	require.NoError(t, FetchDNSRecords(context.Background()))
}

func TestParseBestIPs_TrimsEntries(t *testing.T) {
	assert.Equal(t, []string{"1.1.1.1", "2.2.2.2"}, parseBestIPs("1.1.1.1+2.2.2.2"))
	assert.Nil(t, parseBestIPs(""))
}
