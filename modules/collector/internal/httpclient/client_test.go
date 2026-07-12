package httpclient

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTPClient_ShouldInitializeClient(t *testing.T) {
	client := NewHTTPClient()
	require.NotNil(t, client)
	require.NotNil(t, client.httpClient)
}

func TestHTTPClient_GetWithIP_UnreachableHost_ShouldReturnError(t *testing.T) {
	client := NewHTTPClient()
	var result map[string]string
	err := client.GetWithIP(context.Background(), "invalid.example.test", "/api/v3/ping", nil, &result, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "请求 invalid.example.test 失败")
}
