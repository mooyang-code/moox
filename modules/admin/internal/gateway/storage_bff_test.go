package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageBFFBodySignsInternalStorageAuth(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-secret")
	body := []byte(`{"auth_info":{"app_id":"browser","app_key":"public","operator":"e2e","request_id":"req-1"},"space_id":"crypto"}`)

	got, err := storageBFFBody("storage-view", body)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(got, &payload))
	assert.Equal(t, "crypto", payload["space_id"])
	assert.Equal(t, map[string]any{
		"app_id":     "admin-gateway",
		"app_key":    storageServiceAuthKey("view-secret", "admin-gateway"),
		"operator":   "e2e",
		"request_id": "req-1",
	}, payload["auth_info"])

	got, err = storageBFFBody("storage-primary", body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(got, &payload))
	assert.Equal(t, storageServiceAuthKey("primary-secret", "admin-gateway"),
		payload["auth_info"].(map[string]any)["app_key"])
}

func TestStorageBFFBodyRequiresTargetAuthSecret(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	if _, err := storageBFFBody("storage-primary", []byte(`{}`)); err == nil {
		t.Fatal("storageBFFBody() accepted a missing primary secret")
	}
}

func TestNormalizeNodeGatewayTarget(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "native target", raw: "ip://127.0.0.1:11003", want: "ip://127.0.0.1:11003"},
		{name: "native target with slash", raw: "ip://127.0.0.1:11003/", want: "ip://127.0.0.1:11003"},
		{name: "http target", raw: "http://127.0.0.1:11002", want: "ip://127.0.0.1:11002"},
		{name: "host and port", raw: "127.0.0.1:11003", want: "ip://127.0.0.1:11003"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeNodeGatewayTarget(test.raw)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}

	for _, raw := range []string{"", "tcp://127.0.0.1:11003"} {
		_, err := normalizeNodeGatewayTarget(raw)
		assert.Error(t, err, raw)
	}
}

func TestStorageBFFMethodRouteMapsPublicMethodsAndRejectsInternalMethods(t *testing.T) {
	for _, test := range []struct {
		method  string
		service string
		allowed bool
	}{
		{method: "GetDataSource", service: "storage-primary", allowed: true},
		{method: "GetFieldGroup", service: "storage-primary", allowed: true},
		{method: "ListSubjectSymbols", service: "storage-primary", allowed: true},
		{method: "RegisterDataSubject", service: "storage-primary", allowed: true},
		{method: "GetDataNode", service: "storage-primary", allowed: true},
		{method: "ListDataNodes", service: "storage-primary", allowed: true},
		{method: "UpdateDataNode", service: "storage-primary", allowed: true},
		{method: "DeleteDataNode", service: "storage-primary", allowed: true},
		{method: "CheckDatasetActivation", service: "storage-primary", allowed: true},
		{method: "ActivateDataset", service: "storage-primary", allowed: true},
		{method: "RebindDatasetDataNode", service: "storage-primary", allowed: true},
		{method: "UpsertFields", service: "storage-primary", allowed: true},
		{method: "SearchRecordRows", service: "storage-view", allowed: true},
		{method: "ClaimViewIndexBuild", allowed: false},
		{method: "ScanRecordRows", allowed: false},
		{method: "WriteViewIndex", allowed: false},
		{method: "DeletePrimaryRows", allowed: false},
		{method: "Register" + "DataNode", allowed: false},
		{method: "Create" + "PrimaryStore" + "Node", allowed: false},
		{method: "List" + "PrimaryStore" + "Nodes", allowed: false},
		{method: "Create" + "PrimaryStore" + "Route", allowed: false},
		{method: "List" + "PrimaryStore" + "Routes", allowed: false},
		{method: "UnknownStorageMethod", allowed: false},
	} {
		t.Run(test.method, func(t *testing.T) {
			service, ok := storageBFFServiceID(test.method)
			assert.Equal(t, test.allowed, ok)
			if test.allowed {
				assert.Equal(t, test.service, service)
			} else {
				assert.Empty(t, service)
			}
		})
	}
}

func TestAdminRouterStorageBFFRequiresNativeGatewayConfiguration(t *testing.T) {
	SetConfig(&Config{
		CORS:    CORSConfig{AllowedOrigins: []string{"*"}},
		Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/storage/GetDataSource"}},
	})
	t.Setenv("MOOX_NODE_GATEWAY_URL", "")
	t.Setenv("MOOX_NODE_GATEWAY_NATIVE_URL", "")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "")
	t.Setenv("MOOX_NODE_GATEWAY_NODE_ID", "")
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	provider := &fakeGatewayControlProvider{}
	router := NewHTTPRouter(NewGatewayHandle(), provider, "admin-node-test").buildControlRouter()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/storage/GetDataSource", bytes.NewBufferString(`{"space_id":"space-1","data_source_id":"source-1"}`))

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Node Service Gateway configuration")
	assert.Empty(t, provider.lastNode)
}

func TestAdminRouterStorageBFFDoesNotUseHTTPServiceDetail(t *testing.T) {
	SetConfig(&Config{
		CORS:    CORSConfig{AllowedOrigins: []string{"*"}},
		Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/storage/GetDataSource"}},
	})
	t.Setenv("MOOX_NODE_GATEWAY_URL", "http://127.0.0.1:1")
	t.Setenv("MOOX_NODE_GATEWAY_NATIVE_URL", "ip://127.0.0.1:1")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "service-secret")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "moox-gateway-service")
	t.Setenv("MOOX_NODE_GATEWAY_NODE_ID", "node-a")
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	router := NewHTTPRouter(NewGatewayHandle(), &fakeGatewayControlProvider{}, "admin-node-test").buildControlRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/admin/storage/GetDataSource", bytes.NewBufferString(`{"space_id":"space-1"}`)))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), `"ret_info":{"code":0}`)
}

func TestAdminRouterStorageBFFRejectsInternalMethodBeforeResolvingService(t *testing.T) {
	SetConfig(&Config{
		CORS:    CORSConfig{AllowedOrigins: []string{"*"}},
		Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/storage/ClaimViewIndexBuild"}},
	})
	provider := &fakeGatewayControlProvider{}
	router := NewHTTPRouter(NewGatewayHandle(), provider, "admin-node-test").buildControlRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/admin/storage/ClaimViewIndexBuild", nil))

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Empty(t, provider.lastNode)
}

func TestAdminRouterRejectsDirectStorageServiceIDsAndAliases(t *testing.T) {
	SetConfig(&Config{
		CORS: CORSConfig{AllowedOrigins: []string{"*"}},
		Gateway: GatewayConfig{NoAuthMethods: []string{
			"/api/admin/storage-primary/DeleteDataset",
			"/api/admin/storage-view/ClaimViewIndexBuild",
			"/api/admin/storage_alias/DeleteDataset",
		}},
	})
	upstreamCalls := 0
	provider := &fakeGatewayControlProvider{details: map[string]ServiceDetail{
		"admin-node-test:storage_alias": {
			Address: "127.0.0.1:1",
			Path:    "trpc.moox.storage.Metadata",
		},
	}}
	router := NewHTTPRouter(NewGatewayHandle(), provider, "admin-node-test").buildControlRouter()

	for _, path := range []string{
		"/api/admin/storage-primary/DeleteDataset",
		"/api/admin/storage-view/ClaimViewIndexBuild",
		"/api/admin/storage_alias/DeleteDataset",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`)))
		assert.Equal(t, http.StatusNotFound, recorder.Code, path)
	}
	assert.Equal(t, 0, upstreamCalls)
}
