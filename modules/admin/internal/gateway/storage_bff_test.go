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
		{method: "GetDataSource", service: "storage_metadata", allowed: true},
		{method: "ListSubjectSymbols", service: "storage_metadata", allowed: true},
		{method: "WriteRecordRows", service: "storage_access", allowed: true},
		{method: "SearchRecordRows", service: "storage_view", allowed: true},
		{method: "ClaimViewIndexBuild", allowed: false},
		{method: "ScanRecordRows", allowed: false},
		{method: "WriteViewIndex", allowed: false},
		{method: "DeletePrimaryRows", allowed: false},
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

func TestAdminRouterStorageBFFForwardsToMappedStorageService(t *testing.T) {
	SetConfig(&Config{
		CORS:    CORSConfig{AllowedOrigins: []string{"*"}},
		Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/storage/GetDataSource"}},
	})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/trpc.moox.storage.Metadata/GetDataSource", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"data_source":{"id":"source-1"}}`))
	}))
	t.Cleanup(upstream.Close)
	provider := &fakeGatewayControlProvider{details: map[string]ServiceDetail{
		"admin-node-test:storage_metadata": {Address: upstream.Listener.Addr().String(), Path: "trpc.moox.storage.Metadata"},
	}}
	router := NewHTTPRouter(NewGatewayHandle(), provider, "admin-node-test").buildRouter()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/storage/GetDataSource", bytes.NewBufferString(`{"space_id":"space-1","data_source_id":"source-1"}`))

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"source-1"`)
	assert.Equal(t, "admin-node-test", provider.lastNode)
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
