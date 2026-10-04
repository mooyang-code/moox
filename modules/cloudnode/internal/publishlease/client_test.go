package publishlease

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

func TestClientOperationClaimLifecycle(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "crypto", payload["spaceId"])
		require.Equal(t, "operation-1", payload["operationId"])
		require.Equal(t, "7", payload["fencingToken"])
		w.Header().Set("Content-Type", "application/json")
		active := r.URL.Path != endOperationPath
		_, _ = fmt.Fprintf(w, "{\"ret_info\":{\"code\":0,\"msg\":\"ok\"},\"active\":%t}", active)
	}))
	defer server.Close()

	client := &Client{
		baseURL: server.URL, targetNode: "cloudnode-a",
		credentials: gatewayauth.Credentials{KeyID: "key", Caller: "cloudnode", Secret: "secret"},
		httpClient:  server.Client(),
	}
	ctx := context.Background()
	require.NoError(t, client.BeginOperation(ctx, "crypto", "lease-1", 7, "operation-1"))
	require.NoError(t, client.RenewOperation(ctx, "crypto", "operation-1", 7))
	require.NoError(t, client.EndOperation(ctx, "crypto", "operation-1", 7))
	require.Equal(t, []string{beginOperationPath, renewOperationPath, endOperationPath}, methods)
}

func TestClientRecoveryLeaseLifecycle(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "crypto", payload["spaceId"])
		switch r.URL.Path {
		case acquireLeasePath:
			require.Equal(t, "cloudnode-recovery/job-1/item-1", payload["holderId"])
			require.Equal(t, "7", payload["expectedFencingToken"])
			_, _ = fmt.Fprint(w, `{"retInfo":{"code":0,"msg":"ok"},"spaceId":"crypto","leaseId":"lease-new","fencingToken":"8","expiresAt":"2026-10-03T12:02:00Z"}`)
		case renewLeasePath:
			require.Equal(t, "lease-new", payload["leaseId"])
			require.Equal(t, "8", payload["fencingToken"])
			_, _ = fmt.Fprint(w, `{"retInfo":{"code":0,"msg":"ok"},"spaceId":"crypto","leaseId":"lease-new","fencingToken":"8","expiresAt":"2026-10-03T12:04:00Z"}`)
		case releaseLeasePath:
			require.Equal(t, "lease-new", payload["leaseId"])
			_, _ = fmt.Fprint(w, `{"retInfo":{"code":0,"msg":"ok"},"released":true}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := &Client{
		baseURL: server.URL, targetNode: "cloudnode-a",
		credentials: gatewayauth.Credentials{KeyID: "key", Caller: "cloudnode", Secret: "secret"},
		httpClient:  server.Client(),
	}

	lease, err := client.AcquireLease(context.Background(), "crypto", "cloudnode-recovery/job-1/item-1", 7)
	require.NoError(t, err)
	require.Equal(t, &Lease{SpaceID: "crypto", LeaseID: "lease-new", FencingToken: 8}, lease)
	require.NoError(t, client.RenewLease(context.Background(), lease))
	require.NoError(t, client.ReleaseLease(context.Background(), lease))
	require.Equal(t, []string{acquireLeasePath, renewLeasePath, releaseLeasePath}, methods)
}

func TestClientClassifiesStaleAndHeldLeaseResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == acquireLeasePath {
			_, _ = fmt.Fprintf(w, `{"retInfo":{"code":%d,"msg":"collector publish lease is held"}}`, adminpb.ErrorCode_CONFLICT)
			return
		}
		_, _ = fmt.Fprintf(w, `{"retInfo":{"code":%d,"msg":"collector publish lease is expired or fenced"},"active":false}`, adminpb.ErrorCode_CONFLICT)
	}))
	defer server.Close()
	client := &Client{
		baseURL: server.URL, targetNode: "cloudnode-a",
		credentials: gatewayauth.Credentials{KeyID: "key", Caller: "cloudnode", Secret: "secret"},
		httpClient:  server.Client(),
	}

	_, err := client.AcquireLease(context.Background(), "crypto", "holder", 7)
	require.ErrorIs(t, err, ErrLeaseHeld)
	err = client.BeginOperation(context.Background(), "crypto", "old-lease", 7, "operation-1")
	require.ErrorIs(t, err, ErrLeaseStale)
	require.ErrorIs(t, leaseResponseError(&adminpb.RetInfo{Code: adminpb.ErrorCode_CONFLICT, Msg: ErrLeaseSuperseded.Error()}), ErrLeaseSuperseded)
}
