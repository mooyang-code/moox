package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAndLookupRawHandler_ShouldRoundTrip(t *testing.T) {
	h := RawHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	RegisterRawHandler("svc", "Method", h)
	t.Cleanup(func() {
		rawHandlersMutex.Lock()
		delete(rawHandlers, "svc")
		rawHandlersMutex.Unlock()
	})

	got, ok := LookupRawHandler("svc", "Method")
	require.True(t, ok)
	require.NotNil(t, got)
}

func TestRawAndServe_Miss_ShouldReturnFalse(t *testing.T) {
	assert.False(t, rawAndServe(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "missing", "method", nil))
}

func TestRawAndServe_Hit_ShouldInvokeHandler(t *testing.T) {
	RegisterRawHandler("raw", "Echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("echo"))
	})
	t.Cleanup(func() {
		rawHandlersMutex.Lock()
		delete(rawHandlers, "raw")
		rawHandlersMutex.Unlock()
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ok := rawAndServe(context.Background(), rr, req, "raw", "Echo", map[string]string{
		"space_id": "space-1", "trace_id": "trace-1",
	})
	assert.True(t, ok)
	assert.Equal(t, "echo", rr.Body.String())
}

func TestRawRouteRequiresMatchingOneTimeTicket(t *testing.T) {
	store, _, _, sid := setupRequestAuthTest(t)
	SetRawSessionOwnerVerifier(func(sessionID, userID string) bool { return sessionID == "ssh-1" && userID == "u1" })
	t.Cleanup(func() { SetRawSessionOwnerVerifier(nil) })
	store.tickets["ticket-1"] = authmodel.RawSessionTicket{TicketID: "ticket-1", SessionID: sid, ResourceSessionID: "ssh-1", UserID: "u1", Operation: "sftp_download", ExpiresAt: time.Now().Add(time.Minute)}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/ssh/SftpDownload?ticket=ticket-1&session_id=ssh-1", nil)
	claims, err := validateRawRouteTicket(r, "ssh", "SftpDownload")
	require.NoError(t, err)
	assert.Equal(t, "u1", claims.UserID)
	_, err = validateRawRouteTicket(r, "ssh", "SftpDownload")
	require.Error(t, err)
}

// 归档路由不看 SSH 会话归属，只核对 query 里的 space、dataset 与 ticket 资源一致；操作不同或资源不一致都拒绝。
func TestArchiveRawRouteTicketMatchesSpaceAndDataset(t *testing.T) {
	store, _, _, sid := setupRequestAuthTest(t)
	SetRawSessionOwnerVerifier(func(string, string) bool { return false })
	t.Cleanup(func() { SetRawSessionOwnerVerifier(nil) })
	issue := func(id, operation, resource string) {
		store.tickets[id] = authmodel.RawSessionTicket{TicketID: id, SessionID: sid, ResourceSessionID: resource, UserID: "u1", Operation: operation, ExpiresAt: time.Now().Add(time.Minute)}
	}
	issue("dl", authmodel.RawOperationArchiveDownload, "crypto/ds")
	r := httptest.NewRequest(http.MethodGet, "/api/admin/archive/PartitionDownload?ticket=dl&space=crypto&dataset=ds&partition=2026-10-15", nil)
	claims, err := validateRawRouteTicket(r, "archive", "PartitionDownload")
	require.NoError(t, err)
	assert.Equal(t, "u1", claims.UserID)
	_, err = validateRawRouteTicket(r, "archive", "PartitionDownload")
	require.Error(t, err, "ticket 只能用一次")

	issue("rc", authmodel.RawOperationArchiveRecent, "crypto/ds")
	_, err = validateRawRouteTicket(httptest.NewRequest(http.MethodGet, "/api/admin/archive/RecentRows?ticket=rc&space=crypto&dataset=ds&subject=BTC", nil), "archive", "RecentRows")
	require.NoError(t, err)

	for name, tc := range map[string]struct{ operation, resource, url, method string }{
		"other dataset":   {authmodel.RawOperationArchiveDownload, "crypto/ds", "/x?ticket=t&space=crypto&dataset=other&partition=p", "PartitionDownload"},
		"other space":     {authmodel.RawOperationArchiveDownload, "crypto/ds", "/x?ticket=t&space=stockcn&dataset=ds&partition=p", "PartitionDownload"},
		"slash in space":  {authmodel.RawOperationArchiveDownload, "a/b/ds", "/x?ticket=t&space=a/b&dataset=ds", "PartitionDownload"},
		"wrong operation": {authmodel.RawOperationArchiveRecent, "crypto/ds", "/x?ticket=t&space=crypto&dataset=ds", "PartitionDownload"},
		"ssh ticket":      {"sftp_download", "crypto/ds", "/x?ticket=t&space=crypto&dataset=ds&session_id=crypto/ds", "PartitionDownload"},
	} {
		issue("t", tc.operation, tc.resource)
		_, err := validateRawRouteTicket(httptest.NewRequest(http.MethodGet, tc.url, nil), "archive", tc.method)
		require.Error(t, err, name)
	}
	assert.True(t, isRawTicketPath("/api/admin/archive/PartitionDownload"))
	assert.True(t, isRawTicketPath("/api/admin/archive/RecentRows"))
}
