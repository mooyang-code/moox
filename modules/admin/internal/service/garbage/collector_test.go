package garbage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakeResolver struct {
	detail gateway.ServiceDetail
	ok     bool
}

func (r fakeResolver) ResolveAdminServiceDetail(_ context.Context, adminNodeID, serviceID string) (gateway.ServiceDetail, bool) {
	if adminNodeID != "control" || serviceID != "cloudnode" {
		return gateway.ServiceDetail{}, false
	}
	return r.detail, r.ok
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_users (c_user_id, c_username, c_password_hash) VALUES ('u1', 'admin', 'x')`).Error)
	return db
}

func cloudNodeServer(t *testing.T, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &paths
}

func TestCollectorTrimsHistoryAndCallsCloudNode(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 10, 8, 3, 47, 0, 0, time.UTC)
	for _, row := range []struct{ when string }{{"2026-06-01 00:00:00"}, {"2026-10-01 00:00:00"}} {
		require.NoError(t, db.Exec(`INSERT INTO t_login_history (c_user_id, c_username, c_client_ip, c_login_result, c_ctime) VALUES ('u1', 'admin', '127.0.0.1', 'success', ?)`, row.when).Error)
	}
	for i, row := range []struct{ connect, close any }{
		{"2026-06-01 00:00:00", "2026-06-01 01:00:00"},
		{"2026-06-01 00:00:00", nil},
		{"2026-10-07 00:00:00", nil},
	} {
		require.NoError(t, db.Exec(`INSERT INTO t_ssh_session (c_session_id, c_host_id, c_host_address, c_connect_time, c_close_time) VALUES (?, 1, 'h', ?, ?)`,
			string(rune('a'+i)), row.connect, row.close).Error)
	}
	server, paths := cloudNodeServer(t, `{"ret_info":{"code":0,"msg":"ok"},"packages":2,"cos_bytes":"250"}`)
	address := strings.TrimPrefix(server.URL, "http://")
	collector, err := NewCollector(db, fakeResolver{detail: gateway.ServiceDetail{Address: address, Path: "trpc.moox.cloudnode.CloudNodeMgr"}, ok: true}, "control")
	require.NoError(t, err)
	collector.now = func() time.Time { return now }

	require.NoError(t, collector.Run(context.Background()))
	require.Equal(t, []string{"/trpc.moox.cloudnode.CloudNodeMgr/CollectGarbage"}, *paths)
	var logins, sessions int64
	require.NoError(t, db.Table("t_login_history").Count(&logins).Error)
	require.NoError(t, db.Table("t_ssh_session").Count(&sessions).Error)
	require.EqualValues(t, 1, logins, "only history within 90 days stays")
	require.EqualValues(t, 1, sessions, "a session left connected beyond the retention is removed too")
}

func TestCollectorReportsCloudNodeFailureAfterTrimmingHistory(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(`INSERT INTO t_login_history (c_user_id, c_username, c_client_ip, c_login_result, c_ctime) VALUES ('u1', 'admin', '127.0.0.1', 'success', '2026-01-01 00:00:00')`).Error)
	server, _ := cloudNodeServer(t, `{"ret_info":{"code":999,"msg":"list cloud accounts: boom"}}`)
	collector, err := NewCollector(db, fakeResolver{detail: gateway.ServiceDetail{Address: strings.TrimPrefix(server.URL, "http://"), Path: "trpc.moox.cloudnode.CloudNodeMgr"}, ok: true}, "control")
	require.NoError(t, err)

	err = collector.Run(context.Background())
	require.ErrorContains(t, err, "collect CloudNode garbage: list cloud accounts: boom")
	var logins int64
	require.NoError(t, db.Table("t_login_history").Count(&logins).Error)
	require.Zero(t, logins)
}

func TestCollectorRequiresACloudNodeDeployment(t *testing.T) {
	collector, err := NewCollector(newTestDB(t), fakeResolver{}, "control")
	require.NoError(t, err)
	require.ErrorContains(t, collector.Run(context.Background()), "cloudnode has no active deployment on the admin node")
}
