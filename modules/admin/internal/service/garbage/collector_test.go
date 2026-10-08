package garbage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/codec"
)

// fakeCloudNode 记录转发参数并返回预设的 JSON 响应（与 tRPC JSON 序列化的输出格式一致）。
type fakeCloudNode struct {
	calls int
	rsp   string
	err   error
}

func (f *fakeCloudNode) Forward(_ context.Context, servicePath, method string, serialization int, _ []byte, _ ...gatewayclient.CallOption) ([]byte, error) {
	f.calls++
	if servicePath != "trpc.moox.cloudnode.CloudNodeMgr" || method != "CollectGarbage" || serialization != codec.SerializationTypeJSON {
		return nil, errors.New("unexpected forward " + servicePath + "/" + method)
	}
	return []byte(f.rsp), f.err
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_users (c_user_id, c_username, c_password_hash) VALUES ('u1', 'admin', 'x')`).Error)
	return db
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
	cloudNode := &fakeCloudNode{rsp: `{"ret_info":{"code":0,"msg":""},"packages":2,"cos_objects":0,"cos_bytes":"250","deleted_nodes":0,"node_batches":0,"skipped":[]}`}
	collector, err := NewCollector(db, cloudNode)
	require.NoError(t, err)
	collector.now = func() time.Time { return now }

	require.NoError(t, collector.Run(context.Background()))
	require.Equal(t, 1, cloudNode.calls)
	var logins, sessions int64
	require.NoError(t, db.Table("t_login_history").Count(&logins).Error)
	require.NoError(t, db.Table("t_ssh_session").Count(&sessions).Error)
	require.EqualValues(t, 1, logins, "only history within 90 days stays")
	require.EqualValues(t, 1, sessions, "a session left connected beyond the retention is removed too")
}

func TestCollectorReportsCloudNodeFailureAfterTrimmingHistory(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(`INSERT INTO t_login_history (c_user_id, c_username, c_client_ip, c_login_result, c_ctime) VALUES ('u1', 'admin', '127.0.0.1', 'success', '2026-01-01 00:00:00')`).Error)
	collector, err := NewCollector(db, &fakeCloudNode{rsp: `{"ret_info":{"code":999,"msg":"list cloud accounts: boom"}}`})
	require.NoError(t, err)

	err = collector.Run(context.Background())
	require.ErrorContains(t, err, "collect CloudNode garbage: list cloud accounts: boom")
	var logins int64
	require.NoError(t, db.Table("t_login_history").Count(&logins).Error)
	require.Zero(t, logins)
}

func TestCollectorReportsMissingGatewayClient(t *testing.T) {
	collector, err := NewCollector(newTestDB(t), nil)
	require.NoError(t, err)
	require.ErrorContains(t, collector.Run(context.Background()), "gateway_client 没有配置")
}

func TestCollectorRejectsResponseWithoutReturnCode(t *testing.T) {
	collector, err := NewCollector(newTestDB(t), &fakeCloudNode{rsp: `{"ret_info":null}`})
	require.NoError(t, err)
	require.ErrorContains(t, collector.Run(context.Background()), "缺少返回码")
}

func TestCollectorReportsCloudNodeTransportError(t *testing.T) {
	collector, err := NewCollector(newTestDB(t), &fakeCloudNode{err: errors.New("服务 trpc.moox.cloudnode.CloudNodeMgr 没有已启用的部署")})
	require.NoError(t, err)
	require.ErrorContains(t, collector.Run(context.Background()), "没有已启用的部署")
}
