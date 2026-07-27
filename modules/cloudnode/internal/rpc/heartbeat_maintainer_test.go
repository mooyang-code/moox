package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeartbeatMaintainerBuildsCommunicationOnlyKeepalivePayload(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	seedHeartbeatAccountAndNode(t, catalog, "node-a", "function-a")
	invoker := &heartbeatSCFClient{}
	svc := heartbeatService(catalog, invoker)
	maintainer := NewHeartbeatMaintainer(svc, HeartbeatTargets{
		ServiceGatewayTarget:    "https://gateway.example.com/service",
		StorageRPCGatewayTarget: "ip://gateway.example.com:11003",
	})
	maintainer.now = func() time.Time { return time.Date(2026, 7, 27, 3, 4, 5, 0, time.UTC) }
	maintainer.requestID = func(string, time.Time) string { return "keepalive-node-a" }

	require.NoError(t, maintainer.Handle(context.Background()))
	requests := invoker.requestsSnapshot()
	require.Len(t, requests, 1)
	req := requests[0]
	assert.Equal(t, "Event", req.InvokeType)
	assert.Equal(t, "function-a", req.FunctionName)

	raw, err := json.Marshal(req.EventData)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	assert.Equal(t, map[string]any{
		"action":                     "keepalive",
		"source":                     "keepalive_probe",
		"timestamp":                  "2026-07-27T03:04:05Z",
		"request_id":                 "keepalive-node-a",
		"data":                       map[string]any{"node_id": "node-a"},
		"service_gateway_target":     "https://gateway.example.com/service",
		"storage_rpc_gateway_target": "ip://gateway.example.com:11003",
	}, payload)
	for _, forbidden := range []string{"job_queue", "job_type", "execute_at", "heartbeat"} {
		assert.NotContains(t, payload, forbidden)
	}
}

func TestHeartbeatMaintainerContinuesAfterOneNodeInvokeFails(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	seedHeartbeatAccountAndNode(t, catalog, "node-a", "function-a")
	seedHeartbeatAccountAndNode(t, catalog, "node-b", "function-b")
	invoker := &heartbeatSCFClient{failFunction: "function-a"}
	maintainer := NewHeartbeatMaintainer(heartbeatService(catalog, invoker), HeartbeatTargets{})

	require.NoError(t, maintainer.Handle(context.Background()))
	requests := invoker.requestsSnapshot()
	require.Len(t, requests, 2)
	got := map[string]string{}
	for _, req := range requests {
		got[req.FunctionName] = req.InvokeType
	}
	assert.Equal(t, map[string]string{"function-a": "Event", "function-b": "Event"}, got)
}

func TestHeartbeatMaintainerNoEligibleNodesIsNoop(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "poller", Provider: "tencent-scf", NodeType: "scf-polling",
		Status: "online",
	}))
	invoker := &heartbeatSCFClient{}
	maintainer := NewHeartbeatMaintainer(heartbeatService(catalog, invoker), HeartbeatTargets{})

	require.NoError(t, maintainer.Handle(context.Background()))
	assert.Empty(t, invoker.requestsSnapshot())
}

func TestHeartbeatMaintainerInvokesNodesSequentially(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	for i := 0; i < 25; i++ {
		seedHeartbeatAccountAndNode(t, catalog, fmt.Sprintf("node-%d", i), fmt.Sprintf("function-%d", i))
	}
	invoker := &heartbeatSCFClient{invokeDelay: time.Millisecond}
	maintainer := NewHeartbeatMaintainer(heartbeatService(catalog, invoker), HeartbeatTargets{})

	require.NoError(t, maintainer.Handle(context.Background()))
	assert.Equal(t, 1, invoker.maxConcurrency())
	assert.Len(t, invoker.requestsSnapshot(), 25)
}

func TestHeartbeatMaintainerContinuesWithNextNodeAfterDeadline(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	for i := 0; i < keepaliveBatchSize+1; i++ {
		seedHeartbeatAccountAndNode(t, catalog, fmt.Sprintf("node-%02d", i), fmt.Sprintf("function-%02d", i))
	}
	invoker := &heartbeatSCFClient{invokeDelay: 250 * time.Millisecond}
	maintainer := NewHeartbeatMaintainer(heartbeatService(catalog, invoker), HeartbeatTargets{})

	firstCtx, firstCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer firstCancel()
	require.NoError(t, maintainer.Handle(firstCtx))

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer secondCancel()
	require.NoError(t, maintainer.Handle(secondCtx))

	requests := invoker.requestsSnapshot()
	require.Len(t, requests, 2)
	assert.Equal(t, "function-00", requests[0].FunctionName)
	assert.Equal(t, "function-01", requests[1].FunctionName)
}

func seedHeartbeatAccountAndNode(t *testing.T, catalog *store.CatalogRepository, nodeID string, functionName string) {
	t.Helper()
	require.NoError(t, catalog.UpsertAccount(context.Background(), store.CloudAccount{
		AccountID: "heartbeat-account", Provider: "tencent", CredentialSecretID: "secret-1",
	}))
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID:        "crypto",
		NodeID:         nodeID,
		Provider:       "tencent-scf",
		CloudAccountID: "heartbeat-account",
		NodeType:       "scf-event",
		Region:         "ap-guangzhou",
		Namespace:      "collector",
		FunctionName:   functionName,
		Status:         "unknown",
	}))
}

func heartbeatService(catalog *store.CatalogRepository, invoker *heartbeatSCFClient) *Service {
	return &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "sid", SecretKey: "skey"}},
		scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner {
			return invoker
		},
	}
}

type heartbeatSCFClient struct {
	mu           sync.Mutex
	requests     []tencentscf.InvokeFunctionRequest
	failFunction string
	invokeDelay  time.Duration
	active       int
	maxActive    int
}

func (c *heartbeatSCFClient) InvokeFunction(ctx context.Context, req tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.active++
	if c.active > c.maxActive {
		c.maxActive = c.active
	}
	c.mu.Unlock()
	if c.invokeDelay > 0 {
		select {
		case <-time.After(c.invokeDelay):
		case <-ctx.Done():
		}
	}
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	if req.FunctionName == c.failFunction {
		return nil, errors.New("invoke failed")
	}
	return &tencentscf.InvokeFunctionResponse{RequestID: "request-" + req.FunctionName}, nil
}

func (c *heartbeatSCFClient) GetFunction(context.Context, tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error) {
	return &tencentscf.FunctionInfo{Status: "Active"}, nil
}

func (c *heartbeatSCFClient) CreateFunction(context.Context, tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error) {
	return &tencentscf.CreateFunctionResponse{}, nil
}

func (c *heartbeatSCFClient) UpdateFunctionCode(context.Context, tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error) {
	return &tencentscf.UpdateFunctionCodeResponse{}, nil
}

func (c *heartbeatSCFClient) UpdateFunctionConfiguration(context.Context, tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error) {
	return &tencentscf.UpdateFunctionConfigurationResponse{}, nil
}

func (c *heartbeatSCFClient) requestsSnapshot() []tencentscf.InvokeFunctionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]tencentscf.InvokeFunctionRequest(nil), c.requests...)
}

func (c *heartbeatSCFClient) maxConcurrency() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxActive
}
