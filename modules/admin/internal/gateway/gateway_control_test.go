package gateway

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testGatewayControlSecret = "gateway-control-test-secret"

type fakeGatewayControlProvider struct {
	snapshots map[string]gatewayproxy.Snapshot
	err       error
	reports   []gatewayproxy.GatewayStatusReport
	details   map[string]ServiceDetail
	lastNode  string
}

func (p *fakeGatewayControlProvider) ResolveAdminServiceDetail(_ context.Context, nodeID, serviceID string) (ServiceDetail, bool) {
	p.lastNode = nodeID
	detail, ok := p.details[nodeID+":"+serviceID]
	return detail, ok
}

func (p *fakeGatewayControlProvider) CompileGatewaySnapshot(_ context.Context, nodeID string) (gatewayproxy.Snapshot, error) {
	if p.err != nil {
		return gatewayproxy.Snapshot{}, p.err
	}
	snapshot, ok := p.snapshots[nodeID]
	if !ok {
		return gatewayproxy.Snapshot{}, gatewayproxy.ErrGatewayNodeNotFound
	}
	return snapshot, nil
}

func (p *fakeGatewayControlProvider) ReportGatewayStatus(_ context.Context, report gatewayproxy.GatewayStatusReport) error {
	if p.err != nil {
		return p.err
	}
	p.reports = append(p.reports, report)
	return nil
}

func signedGatewayControlRequest(t *testing.T, method, target, body string, at time.Time) *http.Request {
	t.Helper()
	path := "/api/gateway-control/status"
	if method == http.MethodGet {
		path = "/api/gateway-control/routes?node_id=" + target
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	headers, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: "moox-gateway-control", Secret: testGatewayControlSecret}, gatewayauth.Request{Method: method, Path: req.URL.EscapedPath(), TargetNode: target, Body: []byte(body)}, at)
	require.NoError(t, err)
	req.Header = headers
	return req
}

func setupGatewayControlRouter(t *testing.T, provider GatewayProvider) (*HTTPRouter, *fakeRequestAuthStore) {
	t.Helper()
	t.Setenv("MOOX_GATEWAY_CONTROL_SECRET_KEY", testGatewayControlSecret)
	store := &fakeRequestAuthStore{nonces: map[string]bool{}}
	SetRequestAuthStore(store)
	t.Cleanup(func() { SetRequestAuthStore(nil) })
	return NewHTTPRouter(NewGatewayHandle(), provider, "admin-node-test"), store
}

func TestGatewayControlRoutesReturnsOnlySignedTargetSnapshot(t *testing.T) {
	provider := &fakeGatewayControlProvider{snapshots: map[string]gatewayproxy.Snapshot{
		"node-a": {NodeID: "node-a", RouteHash: strings.Repeat("a", 64)},
		"node-b": {NodeID: "node-b", RouteHash: strings.Repeat("b", 64)},
	}}
	router, _ := setupGatewayControlRouter(t, provider)
	recorder := httptest.NewRecorder()
	router.buildControlRouter().ServeHTTP(recorder, signedGatewayControlRequest(t, http.MethodGet, "node-a", "", time.Now()))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), `"node_id":"node-a"`)
	assert.NotContains(t, recorder.Body.String(), "node-b")
}

func TestGatewayControlStatusReportsExactFieldsWithServerTime(t *testing.T) {
	provider := &fakeGatewayControlProvider{}
	router, _ := setupGatewayControlRouter(t, provider)
	before := time.Now().UTC()
	body := `{"node_id":"node-a","applied_route_hash":"` + strings.Repeat("a", 64) + `","route_count":3,"last_error":"reload failed"}`
	recorder := httptest.NewRecorder()
	router.buildControlRouter().ServeHTTP(recorder, signedGatewayControlRequest(t, http.MethodPost, "node-a", body, time.Now()))
	after := time.Now().UTC()
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Len(t, provider.reports, 1)
	report := provider.reports[0]
	assert.Equal(t, "node-a", report.NodeID)
	assert.Equal(t, strings.Repeat("a", 64), report.AppliedRouteHash)
	assert.Equal(t, int32(3), report.RouteCount)
	assert.Equal(t, "reload failed", report.LastError)
	assert.False(t, report.LastSeenAt.Before(before))
	assert.False(t, report.LastSeenAt.After(after))
}

func TestGatewayControlAuthenticationFailures(t *testing.T) {
	provider := &fakeGatewayControlProvider{snapshots: map[string]gatewayproxy.Snapshot{"node-a": {NodeID: "node-a"}}}
	router, _ := setupGatewayControlRouter(t, provider)
	tests := map[string]func() *http.Request{
		"missing": func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/api/gateway-control/routes?node_id=node-a", nil)
		},
		"malformed": func() *http.Request {
			r := signedGatewayControlRequest(t, http.MethodGet, "node-a", "", time.Now())
			r.Header.Set("X-Moox-Signature", "bad")
			return r
		},
		"expired": func() *http.Request {
			return signedGatewayControlRequest(t, http.MethodGet, "node-a", "", time.Now().Add(-5*time.Minute))
		},
		"wrong target": func() *http.Request {
			r := signedGatewayControlRequest(t, http.MethodGet, "node-a", "", time.Now())
			r.URL.RawQuery = "node_id=node-b"
			return r
		},
	}
	for name, makeRequest := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.buildControlRouter().ServeHTTP(recorder, makeRequest())
			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		})
	}
	replay := signedGatewayControlRequest(t, http.MethodGet, "node-a", "", time.Now())
	first := httptest.NewRecorder()
	router.buildControlRouter().ServeHTTP(first, replay)
	second := httptest.NewRecorder()
	router.buildControlRouter().ServeHTTP(second, replay.Clone(context.Background()))
	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusUnauthorized, second.Code)
}

func TestGatewayControlProviderErrorsAreClassified(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"missing":  {gatewayproxy.ErrGatewayNodeNotFound, http.StatusNotFound},
		"invalid":  {gatewayproxy.ErrInvalidGatewayRoute, http.StatusBadRequest},
		"internal": {errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			router, _ := setupGatewayControlRouter(t, &fakeGatewayControlProvider{err: tc.err})
			recorder := httptest.NewRecorder()
			router.buildControlRouter().ServeHTTP(recorder, signedGatewayControlRequest(t, http.MethodGet, "node-a", "", time.Now()))
			assert.Equal(t, tc.status, recorder.Code)
		})
	}
}

func TestGatewayControlStatusRejectsInvalidBodiesWithoutCallingProvider(t *testing.T) {
	tests := map[string]struct {
		body   string
		status int
	}{
		"malformed":       {`{"node_id":`, http.StatusBadRequest},
		"unknown":         {`{"node_id":"node-a","extra":true}`, http.StatusBadRequest},
		"trailing":        {`{"node_id":"node-a"}{}`, http.StatusBadRequest},
		"empty node":      {`{"node_id":""}`, http.StatusBadRequest},
		"uppercase hash":  {`{"node_id":"node-a","applied_route_hash":"` + strings.Repeat("A", 64) + `"}`, http.StatusBadRequest},
		"negative count":  {`{"node_id":"node-a","route_count":-1}`, http.StatusBadRequest},
		"excessive count": {`{"node_id":"node-a","route_count":10001}`, http.StatusBadRequest},
		"large error":     {`{"node_id":"node-a","last_error":"` + strings.Repeat("x", 1025) + `"}`, http.StatusBadRequest},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			provider := &fakeGatewayControlProvider{}
			router, _ := setupGatewayControlRouter(t, provider)
			recorder := httptest.NewRecorder()
			router.buildControlRouter().ServeHTTP(recorder, signedGatewayControlRequest(t, http.MethodPost, "node-a", tc.body, time.Now()))
			assert.Equal(t, tc.status, recorder.Code)
			assert.Empty(t, provider.reports)
		})
	}
	provider := &fakeGatewayControlProvider{}
	router, _ := setupGatewayControlRouter(t, provider)
	body := bytes.Repeat([]byte("x"), (64<<10)+1)
	recorder := httptest.NewRecorder()
	router.buildControlRouter().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/gateway-control/status", bytes.NewReader(body)))
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Empty(t, provider.reports)
}

func TestGatewayControlRoutesRejectsBody(t *testing.T) {
	provider := &fakeGatewayControlProvider{snapshots: map[string]gatewayproxy.Snapshot{"node-a": {NodeID: "node-a"}}}
	router, _ := setupGatewayControlRouter(t, provider)
	recorder := httptest.NewRecorder()
	req := signedGatewayControlRequest(t, http.MethodGet, "node-a", "unexpected", time.Now())
	router.buildControlRouter().ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestGatewayControlMissingSecretIsUnavailableAndMethodsStayNarrow(t *testing.T) {
	provider := &fakeGatewayControlProvider{snapshots: map[string]gatewayproxy.Snapshot{"node-a": {NodeID: "node-a"}}}
	router, _ := setupGatewayControlRouter(t, provider)
	t.Setenv("MOOX_GATEWAY_CONTROL_SECRET_KEY", "")
	recorder := httptest.NewRecorder()
	router.buildControlRouter().ServeHTTP(recorder, signedGatewayControlRequest(t, http.MethodGet, "node-a", "", time.Now()))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	for _, method := range []string{http.MethodOptions, http.MethodPut} {
		recorder = httptest.NewRecorder()
		router.buildControlRouter().ServeHTTP(recorder, httptest.NewRequest(method, "/api/gateway-control/routes", nil))
		assert.Equal(t, http.StatusNotFound, recorder.Code)
	}
}
