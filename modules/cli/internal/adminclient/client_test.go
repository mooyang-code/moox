package adminclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostJSONSendsSpaceHeader(t *testing.T) {
	var gotSpaceID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSpaceID = r.Header.Get("X-Space-Id")
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.SpaceID = "crypto"
	if _, err := client.postJSON(context.Background(), http.MethodPost, "/api/admin/cloudnode/ListCloudAccounts", map[string]string{}); err != nil {
		t.Fatalf("postJSON() error = %v", err)
	}
	if gotSpaceID != "crypto" {
		t.Fatalf("X-Space-Id = %q, want crypto", gotSpaceID)
	}
}
