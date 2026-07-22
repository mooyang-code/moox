package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStorageBFFMethodRouteMapsPublicMethodsAndRejectsInternalMethods(t *testing.T) {
	for _, test := range []struct {
		method  string
		service string
		allowed bool
	}{
		{method: "GetDataSource", service: "storage-primary", allowed: true},
		{method: "ListSubjectSymbols", service: "storage-primary", allowed: true},
		{method: "RegisterDataSubject", service: "storage-primary", allowed: true},
		{method: "GetDataNode", service: "storage-primary", allowed: true},
		{method: "ListDataNodes", service: "storage-primary", allowed: true},
		{method: "UpdateDataNode", service: "storage-primary", allowed: true},
		{method: "DeleteDataNode", service: "storage-primary", allowed: true},
		{method: "CheckDatasetActivation", service: "storage-primary", allowed: true},
		{method: "ActivateDataset", service: "storage-primary", allowed: true},
		{method: "RebindDatasetDataNode", service: "storage-primary", allowed: true},
		{method: "MergeRecordRows", service: "storage-primary", allowed: true},
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
	provider := &fakeGatewayControlProvider{}
	router := NewHTTPRouter(NewGatewayHandle(), provider, "admin-node-test").buildRouter()
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
	t.Setenv("MOOX_NODE_GATEWAY_NATIVE_URL", "")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "service-secret")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "moox-gateway-service")
	t.Setenv("MOOX_NODE_GATEWAY_NODE_ID", "node-a")
	router := NewHTTPRouter(NewGatewayHandle(), &fakeGatewayControlProvider{}, "admin-node-test").buildRouter()
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
	router := NewHTTPRouter(NewGatewayHandle(), provider, "admin-node-test").buildRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/admin/storage/ClaimViewIndexBuild", nil))

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Empty(t, provider.lastNode)
}
