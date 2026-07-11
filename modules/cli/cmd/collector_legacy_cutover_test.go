package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func decodeJSONBody(t *testing.T, r *http.Request, dst any) {
	t.Helper()
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyCutoverDrainCancelsPendingAndRetainsNodes(t *testing.T) {
	var canceled string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Space-Id"); got != "crypto" {
			t.Fatalf("X-Space-Id = %q", got)
		}
		switch r.URL.Path {
		case "/api/admin/cloudnode/ListJobItems":
			var body struct {
				Status int `json:"status"`
			}
			decodeJSONBody(t, r, &body)
			if body.Status == jobItemPending {
				_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"space_id":"crypto","job_item_id":"old-1","status":1}],"page":{"has_more":false}}`))
			} else {
				_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[],"page":{"has_more":false}}`))
			}
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"space_id":"crypto","node_id":"legacy-scf"}],"page":{"has_more":false}}`))
		case "/api/admin/cloudnode/CancelJobItem":
			var body struct {
				JobItemID string `json:"job_item_id"`
			}
			decodeJSONBody(t, r, &body)
			canceled = body.JobItemID
			_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	summary, err := runLegacyCutover(cmd, "drain", "crypto", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PendingCanceled != 1 || canceled != "old-1" {
		t.Fatalf("summary=%+v canceled=%q", summary, canceled)
	}
	if len(summary.RetainedNodeIDs) != 1 || summary.RetainedNodeIDs[0] != "legacy-scf" {
		t.Fatalf("retained nodes = %#v", summary.RetainedNodeIDs)
	}
}

func TestLegacyCutoverRefusesRunningWork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/cloudnode/ListJobItems":
			var body struct {
				Status int `json:"status"`
			}
			decodeJSONBody(t, r, &body)
			if body.Status == jobItemRunning {
				_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"job_item_id":"running-1","status":2}],"page":{"has_more":false}}`))
			} else {
				_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[],"page":{"has_more":false}}`))
			}
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[],"page":{"has_more":false}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	_, err := runLegacyCutover(cmd, "preflight", "crypto", server.URL)
	if err == nil {
		t.Fatal("expected running legacy work to block cutover")
	}
}
