package tencentcloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_MissingSecretID_ShouldReturnError(t *testing.T) {
	_, err := NewClient(ClientOptions{SecretKey: "key", Region: "ap-guangzhou"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret id is required")
}

func TestNewClient_ValidOptions_ShouldReturnClient(t *testing.T) {
	client, err := NewClient(ClientOptions{
		SecretID:  "sid",
		SecretKey: "skey",
		Region:    "ap-guangzhou",
	})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewCreateFirewallRulesRequest_ValidTCPRule_ShouldBuildRequest(t *testing.T) {
	req, err := NewCreateFirewallRulesRequest(CreateFirewallRulesOptions{
		InstanceID:  "lhins-123",
		Protocol:    "tcp",
		Ports:       "8080",
		CidrBlock:   "0.0.0.0/0",
		Description: "test rule",
	})
	require.NoError(t, err)
	assert.Equal(t, "lhins-123", req.InstanceID)
	require.Len(t, req.FirewallRules, 1)
	assert.Equal(t, "TCP", req.FirewallRules[0].Protocol)
	assert.Equal(t, "8080", req.FirewallRules[0].Port)
	assert.Equal(t, "ACCEPT", req.FirewallRules[0].Action)
}

func TestNewCreateFirewallRulesRequest_InvalidPortRange_ShouldReturnError(t *testing.T) {
	_, err := NewCreateFirewallRulesRequest(CreateFirewallRulesOptions{
		InstanceID: "lhins-123",
		Protocol:   "TCP",
		Ports:      "9000-8000",
		CidrBlock:  "0.0.0.0/0",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid port range")
}

func TestClient_ResolveInstanceIDByPublicIP_FoundInstance_ShouldReturnID(t *testing.T) {
	const wantInstanceID = "lhins-abc"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "DescribeInstances", r.Header.Get("X-TC-Action"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var payload describeInstancesRequest
		require.NoError(t, json.Unmarshal(body, &payload))
		require.Len(t, payload.Filters, 1)
		assert.Equal(t, "public-ip-address", payload.Filters[0].Name)
		assert.Equal(t, []string{"1.2.3.4"}, payload.Filters[0].Values)

		_ = json.NewEncoder(w).Encode(apiResponse{
			Response: responseBody{
				RequestID:  "req-1",
				TotalCount: 1,
				InstanceSet: []InstanceBrief{{
					InstanceID:      wantInstanceID,
					PublicAddresses: []string{"1.2.3.4"},
				}},
			},
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(ClientOptions{
		SecretID:   "sid",
		SecretKey:  "skey",
		Region:     "ap-guangzhou",
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})
	require.NoError(t, err)
	client.now = func() time.Time { return time.Unix(1700000000, 0) }

	got, err := client.ResolveInstanceIDByPublicIP(context.Background(), "1.2.3.4")
	require.NoError(t, err)
	assert.Equal(t, wantInstanceID, got)
}

func TestClient_CreateFirewallRules_APIError_ShouldReturnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(apiResponse{
			Response: responseBody{
				RequestID: "req-2",
				Error:     &apiError{Code: "InvalidParameter", Message: "bad request"},
			},
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(ClientOptions{
		SecretID:   "sid",
		SecretKey:  "skey",
		Region:     "ap-guangzhou",
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})
	require.NoError(t, err)
	client.now = func() time.Time { return time.Unix(1700000000, 0) }

	req, err := NewCreateFirewallRulesRequest(CreateFirewallRulesOptions{
		InstanceID: "lhins-123",
		Protocol:   "TCP",
		Ports:      "8080",
		CidrBlock:  "0.0.0.0/0",
	})
	require.NoError(t, err)

	_, err = client.CreateFirewallRules(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidParameter")
}
