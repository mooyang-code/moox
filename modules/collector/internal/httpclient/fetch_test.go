package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendSingleRequestSignsTargetNode(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-sk")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "gateway-gz-122", r.Header.Get("X-Moox-Target-Node"))
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"records":[]}`))
	}))
	defer server.Close()
	var body []byte
	require.NoError(t, sendSingleRequest(context.Background(), server.URL, server.Client(), &body))
}

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
